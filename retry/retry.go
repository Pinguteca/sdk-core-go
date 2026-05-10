// Package retry provides a Connect client interceptor implementing exponential
// backoff with jitter and respect for server-provided RetryInfo. Streaming RPCs
// are passed through untouched: a stream cannot be replayed safely.
//
// The user-visible contract (algorithm, defaults, retryable code set, server-
// hint precedence, idempotency safety gate, composition order) is pinned at
// the cross-SDK level in sdk-scaffold/docs/rfc/0006-retry-behavioural-contract.md.
// Every SDK must implement that RFC; this package is the Go-side implementation.
// Local ADR 0001 (RNG and jitter) and ADR 0002 (resilience and mesh
// coexistence) cover Go-specific implementation details.
//
// Two jitter strategies are supported (see [Strategy]):
//
//   - [StrategyFull] (default): the classic AWS "full jitter" scheme.
//     delay = rand(0, min(Max, Initial * Multiplier^(N-1)))
//   - [StrategyDecorrelated]: AWS "decorrelated jitter" bounds the next
//     delay relative to the previous one rather than the attempt counter.
//     delay = rand(Initial, min(Max, prev * DecorrelationFactor))
//
// Both schemes outperform proportional jitter at desynchronizing
// retry storms (see https://aws.amazon.com/builders-library/timeouts-retries-and-backoff-with-jitter/).
package retry

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"slices"
	"time"

	"connectrpc.com/connect"

	sdkclock "github.com/Pinguteca/sdk-core-go/clock"
	sdkerrors "github.com/Pinguteca/sdk-core-go/errors"
)

// Constants for the crypto/rand-driven uniform float64 conversion in
// [DefaultJitterSource].
const (
	// float64SignificandBits is the IEEE 754 double-precision significand width.
	// Reading 53 bits and dividing by 1<<53 yields a uniform float64 in [0, 1)
	// without bias.
	float64SignificandBits = 53
	// uint64Bits is the bit width of uint64; used to compute the right shift
	// that keeps only the top significand bits.
	uint64Bits = 64
	// jitterFallback is returned when the kernel CSPRNG fails. Mid-range value
	// avoids skewing the retry distribution.
	jitterFallback = 0.5
)

// Strategy selects the jitter scheme used to compute each retry delay.
type Strategy int

const (
	// StrategyFull is the AWS "full jitter" scheme:
	// delay = rand(0, min(Max, Initial * Multiplier^(N-1))).
	// Best general-purpose choice; matches gRPC-Go and Connect-Go conventions.
	StrategyFull Strategy = iota

	// StrategyDecorrelated bounds the next delay relative to the previous one,
	// not the attempt counter:
	// delay = rand(Initial, min(Max, prev * DecorrelationFactor)).
	// Useful under sustained load where the attempt counter loses meaning.
	StrategyDecorrelated
)

// Config tunes retry behaviour. Use [DefaultConfig] for sensible defaults.
type Config struct {
	Clock          sdkclock.Clock
	IsRetryable    func(err error) bool
	JitterSource   func() float64
	RetryableCodes []connect.Code
	Initial        time.Duration
	Max            time.Duration
	// MinDelay is an optional floor for [StrategyFull]. When >0 the formula
	// becomes MinDelay + rand(0, max(0, ceiling-MinDelay)). Defaults to 0
	// (classic AWS full jitter, allowing zero-wait retries). Decorrelated
	// jitter ignores this field; its floor is always [Config.Initial].
	MinDelay            time.Duration
	Multiplier          float64
	DecorrelationFactor float64
	MaxAttempts         int
	Strategy            Strategy
	// AllowNonIdempotent disables the safety gate that skips retries when the
	// proto schema does not declare the method idempotent (i.e.
	// [connect.IdempotencyUnknown]). Default false: methods without
	// `option idempotency_level = IDEMPOTENT;` (or NO_SIDE_EFFECTS) are not
	// retried, to avoid double-charging on transient failures of mutating RPCs.
	// Set true only when paired with the idempotency-key interceptor and a
	// server that deduplicates by key.
	AllowNonIdempotent bool
}

// Default tuning values for [DefaultConfig]. Exported so callers can build a
// derived [Config] without re-declaring the constants.
const (
	DefaultMaxAttempts         = 4
	DefaultInitialBackoff      = 100 * time.Millisecond
	DefaultMaxBackoff          = 30 * time.Second
	DefaultMultiplier          = 2.0
	DefaultDecorrelationFactor = 3.0
	DefaultStrategy            = StrategyFull
	minMaxAttempts             = 2
	minMultiplier              = 1.0
)

// DefaultConfig is the recommended starting point: 4 attempts, 100ms initial,
// 30s max, full jitter, retry on the codes that almost always indicate
// transient failures.
func DefaultConfig() Config {
	return Config{
		Strategy:            DefaultStrategy,
		MaxAttempts:         DefaultMaxAttempts,
		Initial:             DefaultInitialBackoff,
		Max:                 DefaultMaxBackoff,
		Multiplier:          DefaultMultiplier,
		DecorrelationFactor: DefaultDecorrelationFactor,
		RetryableCodes: []connect.Code{
			connect.CodeUnavailable,
			connect.CodeResourceExhausted,
			connect.CodeAborted,
			connect.CodeDeadlineExceeded,
		},
	}
}

// Interceptor returns the retry interceptor. cfg is copied; later mutations are ignored.
func Interceptor(cfg Config) connect.Interceptor {
	if cfg.MaxAttempts < minMaxAttempts {
		cfg.MaxAttempts = minMaxAttempts
	}
	if cfg.Multiplier < minMultiplier {
		cfg.Multiplier = minMultiplier
	}
	if cfg.DecorrelationFactor < minMultiplier {
		cfg.DecorrelationFactor = DefaultDecorrelationFactor
	}
	if cfg.Initial <= 0 {
		cfg.Initial = DefaultInitialBackoff
	}
	if cfg.Max < cfg.Initial {
		cfg.Max = cfg.Initial
	}
	if cfg.Clock == nil {
		cfg.Clock = sdkclock.Real()
	}
	if cfg.JitterSource == nil {
		cfg.JitterSource = DefaultJitterSource
	}
	return &retryInterceptor{cfg: cfg}
}

// DefaultJitterSource reads 8 bytes from crypto/rand and converts to a uniform
// float64 in [0, 1). Aligns with FIPS 140-3 audits and Go's GOFIPS140 build mode.
//
// Returns 0.5 if the kernel CSPRNG fails (essentially never on
// Linux/macOS/Windows); degrading to mid-jitter is acceptable for backoff
// purposes.
func DefaultJitterSource() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return jitterFallback
	}
	u := binary.LittleEndian.Uint64(b[:]) >> (uint64Bits - float64SignificandBits)
	return float64(u) / float64(uint64(1)<<float64SignificandBits)
}

type retryInterceptor struct{ cfg Config }

func (r *retryInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		// Idempotency safety gate: never retry methods the schema does not
		// declare safe, unless the caller explicitly opted out.
		if !r.cfg.AllowNonIdempotent && req.Spec().IdempotencyLevel == connect.IdempotencyUnknown {
			return next(ctx, req)
		}
		var (
			resp    connect.AnyResponse
			err     error
			ceiling = r.cfg.Initial // grows by Multiplier each retry, used by StrategyFull
			prev    = r.cfg.Initial // last applied delay, used by StrategyDecorrelated
		)
		for attempt := 1; attempt <= r.cfg.MaxAttempts; attempt++ {
			resp, err = next(ctx, req)
			if err == nil || !r.shouldRetry(err) || attempt == r.cfg.MaxAttempts {
				return resp, err
			}
			delay := r.nextDelay(err, ceiling, prev)
			if !r.sleep(ctx, delay) {
				return resp, err
			}
			prev = delay
			ceiling = r.grow(ceiling)
		}
		return resp, err
	}
}

func (r *retryInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (r *retryInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func (r *retryInterceptor) shouldRetry(err error) bool {
	if r.cfg.IsRetryable != nil {
		return r.cfg.IsRetryable(err)
	}
	code := sdkerrors.Code(err)
	return slices.Contains(r.cfg.RetryableCodes, code)
}

// nextDelay returns the delay to wait before the next attempt. Server-provided
// RetryInfo always wins over the local strategy.
func (r *retryInterceptor) nextDelay(err error, ceiling, prev time.Duration) time.Duration {
	if d, ok := sdkerrors.RetryDelay(err); ok && d > 0 {
		return min(d, r.cfg.Max)
	}
	switch r.cfg.Strategy {
	case StrategyDecorrelated:
		return r.decorrelatedDelay(prev)
	case StrategyFull:
		fallthrough
	default:
		return r.fullDelay(ceiling)
	}
}

// fullDelay implements AWS full jitter: delay = rand(0, min(Max, ceiling)).
// When [Config.MinDelay] is set, the formula becomes
// MinDelay + rand(0, max(0, upper-MinDelay)) so retries never fire instantly.
func (r *retryInterceptor) fullDelay(ceiling time.Duration) time.Duration {
	upper := min(ceiling, r.cfg.Max)
	if upper <= 0 {
		return 0
	}
	if r.cfg.MinDelay > 0 {
		if upper <= r.cfg.MinDelay {
			return r.cfg.MinDelay
		}
		spread := float64(upper - r.cfg.MinDelay)
		return r.cfg.MinDelay + time.Duration(r.cfg.JitterSource()*spread)
	}
	return time.Duration(r.cfg.JitterSource() * float64(upper))
}

// decorrelatedDelay implements AWS decorrelated jitter:
// delay = rand(Initial, min(Max, prev * DecorrelationFactor)).
func (r *retryInterceptor) decorrelatedDelay(prev time.Duration) time.Duration {
	upper := min(time.Duration(float64(prev)*r.cfg.DecorrelationFactor), r.cfg.Max)
	if upper <= r.cfg.Initial {
		return r.cfg.Initial
	}
	spread := float64(upper - r.cfg.Initial)
	return r.cfg.Initial + time.Duration(r.cfg.JitterSource()*spread)
}

func (r *retryInterceptor) grow(ceiling time.Duration) time.Duration {
	return min(time.Duration(float64(ceiling)*r.cfg.Multiplier), r.cfg.Max)
}

// sleep returns false when the context is cancelled before the timer fires.
func (r *retryInterceptor) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := r.cfg.Clock.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C():
		return true
	}
}
