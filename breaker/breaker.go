// Package breaker implements a Connect client interceptor that provides the
// classic three-state circuit breaker (Closed -> Open -> HalfOpen -> Closed).
//
// When the breaker is open it short-circuits with a connect.CodeUnavailable
// error carrying a google.rpc.RetryInfo detail. The retry interceptor
// composes naturally: it sees the RetryInfo and waits for the suggested
// cooldown before the next attempt.
//
// Mesh coexistence: do NOT wire this interceptor when the SDK runs behind a
// service mesh that already breaks circuits at the data plane (Envoy,
// Linkerd, Consul, Cilium). Doubled CB amplifies recovery time and the mesh
// has better signal because it sees aggregated traffic from every client.
// Use the presets package to select the right combination per deployment.
package breaker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/types/known/durationpb"

	sdkclock "github.com/Pinguteca/sdk-core-go/clock"
	sdkerrors "github.com/Pinguteca/sdk-core-go/errors"
)

// State enumerates the breaker's lifecycle.
type State int

const (
	// StateClosed: requests pass through; failures are tracked.
	StateClosed State = iota
	// StateOpen: requests fail fast.
	StateOpen
	// StateHalfOpen: a bounded number of trial requests pass through.
	StateHalfOpen
)

// String returns the human-readable state name.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// Config tunes breaker behaviour. Use [DefaultConfig] for sensible defaults.
type Config struct {
	Clock            sdkclock.Clock
	IsFailure        func(err error) bool
	FailureCodes     []connect.Code
	OpenDuration     time.Duration
	WindowSize       int
	FailureThreshold int
	SuccessThreshold int
	HalfOpenTrials   int
}

// Default tuning values for [DefaultConfig].
const (
	DefaultWindowSize       = 20
	DefaultFailureThreshold = 5
	DefaultSuccessThreshold = 3
	DefaultHalfOpenTrials   = 1
	DefaultOpenDuration     = 5 * time.Second
	// halfOpenRejectionDelay is the RetryInfo value advertised when the
	// half-open trial budget is exhausted. Short on purpose: the trial in
	// flight is about to close or reopen the breaker.
	halfOpenRejectionDelay = 100 * time.Millisecond
)

// DefaultConfig returns the recommended starting point: 5 failures in a 20-call
// sliding window trip the breaker; 3 consecutive half-open successes close it;
// 5s open cooldown; one trial at a time during half-open.
func DefaultConfig() Config {
	return Config{
		WindowSize:       DefaultWindowSize,
		FailureThreshold: DefaultFailureThreshold,
		SuccessThreshold: DefaultSuccessThreshold,
		HalfOpenTrials:   DefaultHalfOpenTrials,
		OpenDuration:     DefaultOpenDuration,
		FailureCodes: []connect.Code{
			connect.CodeUnavailable,
			connect.CodeDeadlineExceeded,
			connect.CodeResourceExhausted,
			connect.CodeInternal,
		},
	}
}

// New constructs a Breaker. Most callers should use [Interceptor] instead;
// New is exported for tests and callers that want to inspect [Breaker.State].
func New(cfg Config) *Breaker {
	if cfg.WindowSize < 1 {
		cfg.WindowSize = DefaultWindowSize
	}
	if cfg.FailureThreshold < 1 {
		cfg.FailureThreshold = DefaultFailureThreshold
	}
	if cfg.SuccessThreshold < 1 {
		cfg.SuccessThreshold = DefaultSuccessThreshold
	}
	if cfg.HalfOpenTrials < 1 {
		cfg.HalfOpenTrials = DefaultHalfOpenTrials
	}
	if cfg.OpenDuration <= 0 {
		cfg.OpenDuration = DefaultOpenDuration
	}
	if cfg.Clock == nil {
		cfg.Clock = sdkclock.Real()
	}
	return &Breaker{
		cfg:    cfg,
		window: make([]bool, cfg.WindowSize),
	}
}

// Interceptor returns the breaker as a Connect client interceptor. Streaming
// RPCs are passed through; CB on streams is a separate problem.
func Interceptor(cfg Config) connect.Interceptor {
	return &interceptor{b: New(cfg)}
}

// Breaker tracks the state of a single circuit. Safe for concurrent use.
type Breaker struct {
	openedAt             time.Time
	window               []bool
	cfg                  Config
	state                State
	windowIdx            int
	consecutiveSuccesses int
	halfOpenInFlight     int
	mu                   sync.Mutex
	windowFilled         bool
}

// State returns a snapshot of the current state. Useful for metrics.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// allow returns nil if the call may proceed; otherwise a connect.Error with
// CodeUnavailable and a RetryInfo detail.
func (b *Breaker) allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		return nil
	case StateOpen:
		if b.cfg.Clock.Now().Sub(b.openedAt) >= b.cfg.OpenDuration {
			b.state = StateHalfOpen
			b.halfOpenInFlight = 1
			b.consecutiveSuccesses = 0
			return nil
		}
		remaining := b.cfg.OpenDuration - b.cfg.Clock.Now().Sub(b.openedAt)
		return openError(b.state, remaining)
	case StateHalfOpen:
		if b.halfOpenInFlight >= b.cfg.HalfOpenTrials {
			return openError(b.state, halfOpenRejectionDelay)
		}
		b.halfOpenInFlight++
		return nil
	default:
		return nil
	}
}

// record processes the result of a call.
func (b *Breaker) record(failed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		b.window[b.windowIdx] = !failed
		b.windowIdx = (b.windowIdx + 1) % b.cfg.WindowSize
		if !b.windowFilled && b.windowIdx == 0 {
			b.windowFilled = true
		}
		if b.failureCountLocked() >= b.cfg.FailureThreshold {
			b.tripOpenLocked()
		}
	case StateHalfOpen:
		if b.halfOpenInFlight > 0 {
			b.halfOpenInFlight--
		}
		if failed {
			b.tripOpenLocked()
		} else {
			b.consecutiveSuccesses++
			if b.consecutiveSuccesses >= b.cfg.SuccessThreshold {
				b.closeLocked()
			}
		}
	case StateOpen:
		// no-op; transition handled via allow
	}
}

func (b *Breaker) failureCountLocked() int {
	n := b.cfg.WindowSize
	if !b.windowFilled {
		n = b.windowIdx
	}
	fails := 0
	for i := range n {
		if !b.window[i] {
			fails++
		}
	}
	return fails
}

func (b *Breaker) tripOpenLocked() {
	b.state = StateOpen
	b.openedAt = b.cfg.Clock.Now()
	b.halfOpenInFlight = 0
	b.consecutiveSuccesses = 0
}

func (b *Breaker) closeLocked() {
	b.state = StateClosed
	clear(b.window)
	b.windowIdx = 0
	b.windowFilled = false
	b.consecutiveSuccesses = 0
	b.halfOpenInFlight = 0
}

// isFailure decides whether an err counts against the breaker.
func (b *Breaker) isFailure(err error) bool {
	if err == nil {
		return false
	}
	if b.cfg.IsFailure != nil {
		return b.cfg.IsFailure(err)
	}
	code := sdkerrors.Code(err)
	return slices.Contains(b.cfg.FailureCodes, code)
}

// openError builds the Unavailable error returned when a call is short-circuited.
func openError(state State, retryAfter time.Duration) error {
	if retryAfter < 0 {
		retryAfter = 0
	}
	ce := connect.NewError(connect.CodeUnavailable, fmt.Errorf("breaker: %s", state))
	detail, derr := connect.NewErrorDetail(&errdetails.RetryInfo{
		RetryDelay: durationpb.New(retryAfter),
	})
	if derr != nil {
		// Should never happen for a stable proto; return the bare error.
		return ce
	}
	ce.AddDetail(detail)
	return ce
}

// ErrShortCircuited is sentinel-friendly: errors.Is(err, ErrShortCircuited)
// returns true for any error generated by the breaker.
var ErrShortCircuited = errors.New("breaker: short-circuited")

type interceptor struct{ b *Breaker }

func (i *interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := i.b.allow(); err != nil {
			return nil, err
		}
		resp, err := next(ctx, req)
		i.b.record(i.b.isFailure(err))
		return resp, err
	}
}

func (i *interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
