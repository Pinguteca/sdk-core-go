package retry_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"

	sdkclock "github.com/Pinguteca/sdk-core-go/clock"
	"github.com/Pinguteca/sdk-core-go/retry"
)

func newReq() *connect.Request[emptypb.Empty] {
	return connect.NewRequest(&emptypb.Empty{})
}

// instantClock returns immediately when NewTimer is created so retry sleeps
// don't slow down tests.
type instantClock struct{}

func (instantClock) Now() time.Time { return time.Now() }
func (instantClock) NewTimer(time.Duration) sdkclock.Timer {
	ch := make(chan time.Time, 1)
	ch <- time.Now()
	return instantTimer{ch: ch}
}

type instantTimer struct{ ch chan time.Time }

func (i instantTimer) C() <-chan time.Time { return i.ch }
func (instantTimer) Stop() bool            { return true }

func TestRetriesUntilSuccess(t *testing.T) {
	t.Parallel()
	cfg := retry.DefaultConfig()
	cfg.Clock = instantClock{}
	cfg.MaxAttempts = 4
	cfg.Initial = time.Microsecond
	ic := retry.Interceptor(cfg)

	var attempts atomic.Int32
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		n := attempts.Add(1)
		if n < 3 {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("unavailable"))
		}
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if _, err := ic.WrapUnary(next)(context.Background(), newReq()); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestStopsOnNonRetryableCode(t *testing.T) {
	t.Parallel()
	cfg := retry.DefaultConfig()
	cfg.Clock = instantClock{}
	cfg.Initial = time.Microsecond
	ic := retry.Interceptor(cfg)

	var attempts atomic.Int32
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		attempts.Add(1)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("bad"))
	}
	_, err := ic.WrapUnary(next)(context.Background(), newReq())
	if err == nil {
		t.Fatalf("expected error")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("non-retryable code retried: attempts = %d", got)
	}
}

func TestStopsAfterMaxAttempts(t *testing.T) {
	t.Parallel()
	cfg := retry.DefaultConfig()
	cfg.Clock = instantClock{}
	cfg.MaxAttempts = 3
	cfg.Initial = time.Microsecond
	ic := retry.Interceptor(cfg)

	var attempts atomic.Int32
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		attempts.Add(1)
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("unavailable"))
	}
	_, err := ic.WrapUnary(next)(context.Background(), newReq())
	if err == nil {
		t.Fatalf("expected final error")
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3 (MaxAttempts)", got)
	}
}

func TestHonoursRetryInfo(t *testing.T) {
	t.Parallel()

	ce := connect.NewError(connect.CodeResourceExhausted, errors.New("exhausted"))
	det, err := connect.NewErrorDetail(&errdetails.RetryInfo{
		RetryDelay: durationpb.New(50 * time.Millisecond),
	})
	if err != nil {
		t.Fatalf("NewErrorDetail: %v", err)
	}
	ce.AddDetail(det)

	cfg := retry.DefaultConfig()
	cfg.Clock = instantClock{}
	cfg.MaxAttempts = 2
	ic := retry.Interceptor(cfg)

	var attempts atomic.Int32
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		attempts.Add(1)
		return nil, ce
	}
	if _, err := ic.WrapUnary(next)(context.Background(), newReq()); err == nil {
		t.Fatalf("expected error")
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestContextCancellationStopsRetry(t *testing.T) {
	t.Parallel()
	cfg := retry.DefaultConfig()
	cfg.MaxAttempts = 5
	cfg.Initial = 1 * time.Hour // would block test if cancellation didn't fire
	ic := retry.Interceptor(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var attempts atomic.Int32
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		attempts.Add(1)
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("unavailable"))
	}
	_, err := ic.WrapUnary(next)(ctx, newReq())
	if err == nil {
		t.Fatalf("expected error")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("cancelled context should stop after first attempt, got %d", got)
	}
}
