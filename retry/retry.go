// Package retry provides a Connect client interceptor implementing exponential
// backoff with jitter and respect for server-provided RetryInfo. Streaming RPCs
// are passed through untouched: a stream cannot be replayed safely.
package retry

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"connectrpc.com/connect"

	sdkclock "github.com/Pinguteca/sdk-core-go/clock"
	sdkerrors "github.com/Pinguteca/sdk-core-go/errors"
)

// Config tunes retry behaviour. Use [DefaultConfig] for sensible defaults.
type Config struct {
	// MaxAttempts caps total attempts including the first. <2 disables retries.
	MaxAttempts int
	// Initial is the first backoff before any multiplier or jitter.
	Initial time.Duration
	// Max bounds any single backoff after multiplier and jitter.
	Max time.Duration
	// Multiplier scales the backoff each subsequent attempt.
	Multiplier float64
	// Jitter is the fraction of the computed backoff to randomize (0.0 to 1.0).
	Jitter float64
	// RetryableCodes are the Connect codes that trigger another attempt.
	RetryableCodes []connect.Code
	// IsRetryable, when set, takes precedence over RetryableCodes for fine-grained
	// per-error decisions (e.g. inspect ErrorInfo.reason).
	IsRetryable func(err error) bool
	// Clock is the time source. Defaults to [sdkclock.Real].
	Clock sdkclock.Clock
	// Rand seeds jitter. Defaults to a per-process source. Override for tests.
	Rand *rand.Rand
}

// DefaultConfig is the recommended starting point: 4 attempts, 100ms initial,
// 30s max, 2x growth, 25% jitter, retry on the codes that almost always indicate
// transient failures.
func DefaultConfig() Config {
	return Config{
		MaxAttempts: 4,
		Initial:     100 * time.Millisecond,
		Max:         30 * time.Second,
		Multiplier:  2.0,
		Jitter:      0.25,
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
	if cfg.MaxAttempts < 2 {
		cfg.MaxAttempts = 2
	}
	if cfg.Multiplier < 1 {
		cfg.Multiplier = 1
	}
	if cfg.Jitter < 0 {
		cfg.Jitter = 0
	}
	if cfg.Jitter > 1 {
		cfg.Jitter = 1
	}
	if cfg.Clock == nil {
		cfg.Clock = sdkclock.Real()
	}
	return &retryInterceptor{cfg: cfg}
}

type retryInterceptor struct{ cfg Config }

func (r *retryInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		var (
			resp    connect.AnyResponse
			err     error
			backoff = r.cfg.Initial
		)
		for attempt := 1; attempt <= r.cfg.MaxAttempts; attempt++ {
			resp, err = next(ctx, req)
			if err == nil || !r.shouldRetry(err) || attempt == r.cfg.MaxAttempts {
				return resp, err
			}
			delay := r.nextDelay(err, backoff)
			if !r.sleep(ctx, delay) {
				return resp, err
			}
			backoff = r.grow(backoff)
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
	for _, c := range r.cfg.RetryableCodes {
		if c == code {
			return true
		}
	}
	return false
}

func (r *retryInterceptor) nextDelay(err error, backoff time.Duration) time.Duration {
	// Server hints win over local backoff.
	if d, ok := sdkerrors.RetryDelay(err); ok && d > 0 {
		if d > r.cfg.Max {
			d = r.cfg.Max
		}
		return d
	}
	d := backoff
	if d > r.cfg.Max {
		d = r.cfg.Max
	}
	if r.cfg.Jitter > 0 {
		span := float64(d) * r.cfg.Jitter
		offset := time.Duration(r.float64()*2*span - span)
		d += offset
	}
	if d < 0 {
		d = 0
	}
	return d
}

func (r *retryInterceptor) grow(backoff time.Duration) time.Duration {
	next := time.Duration(float64(backoff) * r.cfg.Multiplier)
	if next > r.cfg.Max {
		next = r.cfg.Max
	}
	return next
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

func (r *retryInterceptor) float64() float64 {
	if r.cfg.Rand != nil {
		return r.cfg.Rand.Float64()
	}
	return rand.Float64()
}

// IsClientGuard is exported so tests in other packages can confirm streaming
// pass-through without touching internals.
var ErrStreamingNotRetried = errors.New("retry: streaming RPCs are not retried")
