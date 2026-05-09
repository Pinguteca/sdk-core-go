package breaker_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/Pinguteca/sdk-core-go/breaker"
	sdkclock "github.com/Pinguteca/sdk-core-go/clock"
	sdkerrors "github.com/Pinguteca/sdk-core-go/errors"
)

type fakeClock struct {
	now time.Time
	mu  sync.Mutex
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) NewTimer(time.Duration) sdkclock.Timer {
	ch := make(chan time.Time, 1)
	ch <- time.Now()
	return readyTimer{ch: ch}
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

type readyTimer struct{ ch chan time.Time }

func (r readyTimer) C() <-chan time.Time { return r.ch }
func (readyTimer) Stop() bool            { return true }

func newReq() *connect.Request[emptypb.Empty] {
	return connect.NewRequest(&emptypb.Empty{})
}

func unavailable() error {
	return connect.NewError(connect.CodeUnavailable, errors.New("upstream down"))
}

func TestClosedTripsToOpenAtThreshold(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	cfg := breaker.DefaultConfig()
	cfg.Clock = clk
	cfg.FailureThreshold = 3
	cfg.WindowSize = 10
	ic := breaker.Interceptor(cfg)

	failing := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, unavailable()
	}
	wrapped := ic.WrapUnary(failing)

	for range 3 {
		if _, err := wrapped(context.Background(), newReq()); err == nil {
			t.Fatalf("expected failure")
		}
	}
	// Next call should be short-circuited (state is now Open).
	_, err := wrapped(context.Background(), newReq())
	if err == nil {
		t.Fatalf("expected open-circuit error")
	}
	if sdkerrors.Code(err) != connect.CodeUnavailable {
		t.Fatalf("err code = %v, want Unavailable", sdkerrors.Code(err))
	}
	delay, ok := sdkerrors.RetryDelay(err)
	if !ok {
		t.Fatalf("open error must carry RetryInfo")
	}
	if delay <= 0 || delay > cfg.OpenDuration {
		t.Fatalf("RetryDelay = %v, want (0, %v]", delay, cfg.OpenDuration)
	}
}

func TestOpenTransitionsToHalfOpenAfterDuration(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	cfg := breaker.DefaultConfig()
	cfg.Clock = clk
	cfg.FailureThreshold = 1
	cfg.OpenDuration = 5 * time.Second
	cfg.HalfOpenTrials = 1
	cfg.SuccessThreshold = 1
	ic := breaker.Interceptor(cfg)

	var nextErr error
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		if nextErr != nil {
			return nil, nextErr
		}
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	wrapped := ic.WrapUnary(next)

	// Trip the breaker.
	nextErr = unavailable()
	if _, err := wrapped(context.Background(), newReq()); err == nil {
		t.Fatalf("expected failure")
	}

	// Still open: short-circuited.
	if _, err := wrapped(context.Background(), newReq()); err == nil {
		t.Fatalf("expected open-circuit error")
	}

	// Advance past the open duration; first call should be allowed (half-open trial).
	clk.Advance(cfg.OpenDuration + time.Second)
	nextErr = nil
	if _, err := wrapped(context.Background(), newReq()); err != nil {
		t.Fatalf("expected half-open trial to succeed: %v", err)
	}
}

func TestHalfOpenLimitsConcurrentTrials(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	cfg := breaker.DefaultConfig()
	cfg.Clock = clk
	cfg.FailureThreshold = 1
	cfg.OpenDuration = time.Second
	cfg.HalfOpenTrials = 1
	ic := breaker.Interceptor(cfg)

	// Block the trial call so we can test concurrent limit.
	gate := make(chan struct{})
	released := make(chan struct{})
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		<-gate
		close(released)
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	failing := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, unavailable()
	}

	// Trip the breaker via a separate wrap (same breaker via the same Interceptor
	// call would share state, but we need different next funcs).
	// Re-using the same interceptor instance here.
	// First, trip with failing next.
	if _, err := ic.WrapUnary(failing)(context.Background(), newReq()); err == nil {
		t.Fatalf("expected failure to trip breaker")
	}
	clk.Advance(cfg.OpenDuration + time.Millisecond)

	// Start a half-open trial that blocks.
	wrapped := ic.WrapUnary(next)
	go func() {
		_, _ = wrapped(context.Background(), newReq())
	}()
	// Wait briefly for the goroutine to consume the trial slot.
	time.Sleep(50 * time.Millisecond)

	// Second concurrent call should be rejected (trial budget=1).
	_, err := wrapped(context.Background(), newReq())
	if err == nil {
		t.Fatalf("expected half-open rejection")
	}
	if sdkerrors.Code(err) != connect.CodeUnavailable {
		t.Fatalf("err code = %v, want Unavailable", sdkerrors.Code(err))
	}

	close(gate)
	<-released
}

func TestHalfOpenSuccessClosesAfterThreshold(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	cfg := breaker.DefaultConfig()
	cfg.Clock = clk
	cfg.FailureThreshold = 1
	cfg.OpenDuration = time.Second
	cfg.HalfOpenTrials = 5
	cfg.SuccessThreshold = 3
	ic := breaker.Interceptor(cfg)

	failing := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, unavailable()
	}
	if _, err := ic.WrapUnary(failing)(context.Background(), newReq()); err == nil {
		t.Fatalf("expected failure")
	}
	clk.Advance(cfg.OpenDuration + time.Millisecond)

	ok := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	wrapped := ic.WrapUnary(ok)
	for range cfg.SuccessThreshold {
		if _, err := wrapped(context.Background(), newReq()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	// Now closed: failing again should not short-circuit (window resets).
	if _, err := ic.WrapUnary(failing)(context.Background(), newReq()); err == nil {
		t.Fatalf("expected failure (passed through, not short-circuited)")
	}
}

func TestHalfOpenFailureReopens(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	cfg := breaker.DefaultConfig()
	cfg.Clock = clk
	cfg.FailureThreshold = 1
	cfg.OpenDuration = time.Second
	cfg.HalfOpenTrials = 1
	cfg.SuccessThreshold = 1
	ic := breaker.Interceptor(cfg)

	failing := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, unavailable()
	}
	if _, err := ic.WrapUnary(failing)(context.Background(), newReq()); err == nil {
		t.Fatalf("expected failure")
	}
	clk.Advance(cfg.OpenDuration + time.Millisecond)

	// Half-open trial fails -> reopens.
	if _, err := ic.WrapUnary(failing)(context.Background(), newReq()); err == nil {
		t.Fatalf("expected failure")
	}

	// Should be Open again, short-circuit immediately.
	_, err := ic.WrapUnary(failing)(context.Background(), newReq())
	if err == nil {
		t.Fatalf("expected open-circuit error")
	}
	if _, ok := sdkerrors.RetryDelay(err); !ok {
		t.Fatalf("expected RetryInfo on open-circuit error")
	}
}
