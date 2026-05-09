// Package hedge implements a Connect client interceptor that races multiple
// parallel attempts of the same RPC and returns the first successful response,
// cancelling the others. Hedged requests cut tail latency at the cost of N
// times the backend load. They are unsafe for non-idempotent RPCs because the
// server might process more than one attempt before the client cancels the
// losers, so the interceptor refuses to hedge any RPC whose proto schema does
// not declare it side-effect-free.
//
// Defaults: 3 total attempts, 50 ms between launches, hedge only RPCs marked
// `option idempotency_level = NO_SIDE_EFFECTS` in the schema. Methods marked
// `IDEMPOTENT` are still skipped unless [Config.HedgeIdempotent] is true.
//
// Composition: place this interceptor INSIDE retry. Each hedge attempt runs
// the rest of the interceptor chain, so retry sees each hedge attempt as a
// separate retry attempt. When hedging is on, lower the retry MaxAttempts
// accordingly. Streaming RPCs pass through unchanged: a stream cannot be
// replayed safely. See docs/adr/0005-hedged-requests.md for the full
// trade-off analysis.
package hedge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	sdkclock "github.com/Pinguteca/sdk-core-go/clock"
)

// Config tunes hedge behaviour. Use [DefaultConfig] for sensible defaults.
type Config struct {
	Clock sdkclock.Clock
	// Delay is the time between successive launches. The first hedge attempt
	// fires Delay after the primary attempt starts, the second fires Delay
	// after that, and so on. Pick a value close to the typical p50 for the
	// target RPC; smaller values amplify load, larger values give the primary
	// more chance to win on its own.
	Delay time.Duration
	// MaxAttempts caps the total number of in-flight attempts (primary plus
	// hedges). 1 disables hedging.
	MaxAttempts int
	// HedgeIdempotent enables hedging for IDEMPOTENT RPCs in addition to the
	// always-on NO_SIDE_EFFECTS ones. Default false because IDEMPOTENT only
	// guarantees a method tolerates duplicate calls, not that the duplicates
	// are free; hedging an IDEMPOTENT write doubles billing-relevant load.
	HedgeIdempotent bool
}

// Default tuning values for [DefaultConfig].
const (
	DefaultMaxAttempts = 3
	DefaultDelay       = 50 * time.Millisecond
)

// DefaultConfig returns the recommended starting point: 3 attempts total, 50ms
// between launches, hedge only NO_SIDE_EFFECTS RPCs.
func DefaultConfig() Config {
	return Config{
		MaxAttempts: DefaultMaxAttempts,
		Delay:       DefaultDelay,
	}
}

// Interceptor returns the hedge interceptor. The provided cfg is copied; later
// mutations are ignored.
func Interceptor(cfg Config) connect.Interceptor {
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}
	if cfg.Delay < 0 {
		cfg.Delay = 0
	}
	if cfg.Clock == nil {
		cfg.Clock = sdkclock.Real()
	}
	return &interceptor{cfg: cfg}
}

type interceptor struct{ cfg Config }

func (h *interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !h.shouldHedge(req) || h.cfg.MaxAttempts <= 1 {
			return next(ctx, req)
		}
		return h.runHedged(ctx, req, next)
	}
}

func (h *interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (h *interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// shouldHedge gates the policy: NO_SIDE_EFFECTS is always eligible; IDEMPOTENT
// only when the caller opts in. Unknown is never hedged.
func (h *interceptor) shouldHedge(req connect.AnyRequest) bool {
	switch req.Spec().IdempotencyLevel {
	case connect.IdempotencyNoSideEffects:
		return true
	case connect.IdempotencyIdempotent:
		return h.cfg.HedgeIdempotent
	default:
		return false
	}
}

type result struct {
	resp connect.AnyResponse
	err  error
}

// runHedged orchestrates the staggered launch and the first-wins race.
//
// Buffering the results channel to MaxAttempts is deliberate: it lets in-flight
// goroutines complete and report results even after the function has returned
// with an early winner, so they do not leak waiting on an unread channel.
func (h *interceptor) runHedged(
	ctx context.Context,
	req connect.AnyRequest,
	next connect.UnaryFunc,
) (connect.AnyResponse, error) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	state := &hedgeState{
		cfg:     &h.cfg,
		results: make(chan result, h.cfg.MaxAttempts),
	}
	state.fire = func() {
		go func() {
			resp, err := next(cctx, req)
			state.results <- result{resp: resp, err: err}
		}()
	}

	state.fire()
	state.launched = 1

	for state.completed < state.launched || state.launched < h.cfg.MaxAttempts {
		if final := state.tick(ctx); final != nil {
			return final.resp, final.err
		}
	}
	if state.lastErr == nil {
		// Defensive: should never happen because we only exit the loop after
		// exhausting attempts, all of which produced a result.
		return nil, errors.New("hedge: no result")
	}
	return nil, state.lastErr
}

// hedgeState carries the orchestration state across iterations of [runHedged]
// so the loop body can live in a separate method, keeping cyclomatic
// complexity bounded.
type hedgeState struct {
	cfg       *Config
	fire      func()
	results   chan result
	lastErr   error
	launched  int
	completed int
}

// tick advances one iteration of the orchestration loop. A non-nil return
// signals the caller should return immediately with the contained resp/err.
// A nil return means the loop should continue.
func (s *hedgeState) tick(ctx context.Context) *result {
	var tickC <-chan time.Time
	var timer sdkclock.Timer
	if s.launched < s.cfg.MaxAttempts {
		timer = s.cfg.Clock.NewTimer(s.cfg.Delay)
		tickC = timer.C()
	}

	select {
	case <-ctx.Done():
		if timer != nil {
			timer.Stop()
		}
		return &result{err: fmt.Errorf("hedge: %w", ctx.Err())}
	case r := <-s.results:
		if timer != nil {
			timer.Stop()
		}
		s.completed++
		if r.err == nil {
			return &r
		}
		s.lastErr = r.err
		return nil
	case <-tickC:
		s.fire()
		s.launched++
		return nil
	}
}
