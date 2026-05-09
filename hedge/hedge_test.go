package hedge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	sdkclock "github.com/Pinguteca/sdk-core-go/clock"
)

// instantClock fires every timer immediately. With Delay=0 plus this clock,
// hedged attempts launch as fast as the loop can reach them. Tests use this
// to exercise the multi-attempt path deterministically.
type instantClock struct{}

func (instantClock) Now() time.Time { return time.Now() }
func (instantClock) NewTimer(time.Duration) sdkclock.Timer {
	ch := make(chan time.Time, 1)
	ch <- time.Now()
	return readyTimer{ch: ch}
}

type readyTimer struct{ ch chan time.Time }

func (r readyTimer) C() <-chan time.Time { return r.ch }
func (readyTimer) Stop() bool            { return true }

// neverClock holds every timer open. Used to confirm that pass-through paths
// avoid the hedging loop entirely.
type neverClock struct{}

func (neverClock) Now() time.Time { return time.Now() }
func (neverClock) NewTimer(time.Duration) sdkclock.Timer {
	return readyTimer{ch: make(chan time.Time)} // never fires
}

func newReq() *connect.Request[emptypb.Empty] {
	return connect.NewRequest(&emptypb.Empty{})
}

func TestPassThroughOnUnknownIdempotency(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.Clock = neverClock{}
	cfg.Delay = 0
	ic := Interceptor(cfg)

	var calls atomic.Int32
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		calls.Add(1)
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if _, err := ic.WrapUnary(next)(context.Background(), newReq()); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1 (Unknown idempotency must pass through)", got)
	}
}

func TestMaxAttemptsOneIsPassThrough(t *testing.T) {
	t.Parallel()
	cfg := Config{MaxAttempts: 1, HedgeIdempotent: true}
	ic := Interceptor(cfg)

	var calls atomic.Int32
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		calls.Add(1)
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if _, err := ic.WrapUnary(next)(context.Background(), newReq()); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
}

// TestFirstSuccessWins exercises the inner runHedged directly, bypassing the
// idempotency gate (Spec is package-private so tests cannot mark a request as
// NO_SIDE_EFFECTS). The gate behaviour is covered separately above.
func TestFirstSuccessWins(t *testing.T) {
	t.Parallel()
	cfg := Config{
		MaxAttempts: 3,
		Delay:       time.Microsecond,
		Clock:       instantClock{},
	}
	h := &interceptor{cfg: cfg}

	var attempts atomic.Int32
	first := make(chan struct{})
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		n := attempts.Add(1)
		if n == 1 {
			// Block primary until we let it finish — but second/third should win.
			select {
			case <-first:
				return connect.NewResponse(&emptypb.Empty{}), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		// Hedge attempts return immediately with success.
		return connect.NewResponse(&emptypb.Empty{}), nil
	}

	resp, err := h.runHedged(context.Background(), newReq(), next)
	if err != nil {
		t.Fatalf("runHedged: %v", err)
	}
	if resp == nil {
		t.Fatalf("nil response")
	}
	close(first) // let primary finish so its goroutine doesn't leak
}

func TestAllFailReturnsLastError(t *testing.T) {
	t.Parallel()
	cfg := Config{
		MaxAttempts: 3,
		Delay:       time.Microsecond,
		Clock:       instantClock{},
	}
	h := &interceptor{cfg: cfg}

	wantErr := connect.NewError(connect.CodeUnavailable, errors.New("down"))
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, wantErr
	}

	_, err := h.runHedged(context.Background(), newReq(), next)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestParentContextCancellationAborts(t *testing.T) {
	t.Parallel()
	cfg := Config{
		MaxAttempts: 3,
		Delay:       time.Hour, // would block forever without cancellation
		Clock:       sdkclock.Real(),
	}
	h := &interceptor{cfg: cfg}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	released := make(chan struct{})
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		<-ctx.Done()
		released <- struct{}{}
		return nil, ctx.Err()
	}

	_, err := h.runHedged(ctx, newReq(), next)
	if err == nil {
		t.Fatalf("expected ctx error")
	}
	<-released // primary goroutine sees cancel and exits
}
