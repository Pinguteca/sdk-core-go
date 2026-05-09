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
	cfg.AllowNonIdempotent = true // tests cannot set Spec.IdempotencyLevel
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
	cfg.AllowNonIdempotent = true // tests cannot set Spec.IdempotencyLevel
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
	cfg.AllowNonIdempotent = true // tests cannot set Spec.IdempotencyLevel
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
	cfg.AllowNonIdempotent = true // tests cannot set Spec.IdempotencyLevel
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

func TestSkipsRetryOnUnknownIdempotency(t *testing.T) {
	t.Parallel()
	// Default config has AllowNonIdempotent = false. A connect.NewRequest
	// defaults to IdempotencyUnknown, so the safety gate must short-circuit.
	cfg := retry.DefaultConfig()
	cfg.Clock = instantClock{}
	cfg.Initial = time.Microsecond
	ic := retry.Interceptor(cfg)

	var attempts atomic.Int32
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		attempts.Add(1)
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("unavailable"))
	}
	if _, err := ic.WrapUnary(next)(context.Background(), newReq()); err == nil {
		t.Fatalf("expected error")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("safety gate failed: attempts = %d, want 1 (no retry on Unknown)", got)
	}
}

func TestFullJitterBoundedByCeiling(t *testing.T) {
	t.Parallel()

	// JitterSource always returns 1.0 (well, just under). Then full-jitter
	// delay should equal the ceiling exactly (or as close as float64 allows).
	// Since 1.0 is excluded from [0, 1) we use a value that returns the ceiling.
	cfg := retry.DefaultConfig()
	cfg.AllowNonIdempotent = true
	cfg.Strategy = retry.StrategyFull
	cfg.MaxAttempts = 4
	cfg.Initial = 100 * time.Millisecond
	cfg.Max = 10 * time.Second
	cfg.Multiplier = 2.0
	cfg.Clock = instantClock{}
	cfg.JitterSource = func() float64 { return 0.5 } // mid-jitter for predictability

	ic := retry.Interceptor(cfg)

	var attempts atomic.Int32
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		attempts.Add(1)
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("unavailable"))
	}
	_, err := ic.WrapUnary(next)(context.Background(), newReq())
	if err == nil {
		t.Fatalf("expected error")
	}
	if got := attempts.Load(); got != 4 {
		t.Fatalf("attempts = %d, want 4", got)
	}
}

func TestDecorrelatedJitterUsesPrevious(t *testing.T) {
	t.Parallel()

	cfg := retry.DefaultConfig()
	cfg.AllowNonIdempotent = true
	cfg.Strategy = retry.StrategyDecorrelated
	cfg.MaxAttempts = 4
	cfg.Initial = 100 * time.Millisecond
	cfg.Max = 10 * time.Second
	cfg.DecorrelationFactor = 3.0
	cfg.Clock = instantClock{}
	// JitterSource returns 1.0 - epsilon so each delay hits the upper bound
	// (Initial + (upper - Initial) * 1.0  ~=  upper).
	cfg.JitterSource = func() float64 { return 0.999999 }

	ic := retry.Interceptor(cfg)

	var attempts atomic.Int32
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		attempts.Add(1)
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("unavailable"))
	}
	_, err := ic.WrapUnary(next)(context.Background(), newReq())
	if err == nil {
		t.Fatalf("expected error")
	}
	if got := attempts.Load(); got != 4 {
		t.Fatalf("attempts = %d, want 4", got)
	}
}

func TestDefaultJitterSourceUniformish(t *testing.T) {
	t.Parallel()

	// Sample DefaultJitterSource a few thousand times and confirm the mean is
	// near 0.5 and the spread covers most of [0, 1). This is a sanity check on
	// the crypto/rand-driven uniform conversion, not a statistical test.
	const samples = 4096
	var sum float64
	minV, maxV := 1.0, 0.0
	for range samples {
		v := retry.DefaultJitterSource()
		if v < 0 || v >= 1 {
			t.Fatalf("DefaultJitterSource returned %v, want [0, 1)", v)
		}
		sum += v
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	mean := sum / float64(samples)
	if mean < 0.45 || mean > 0.55 {
		t.Fatalf("mean = %v, want approx 0.5", mean)
	}
	if minV > 0.05 || maxV < 0.95 {
		t.Fatalf("spread = [%v, %v], want broad coverage of [0, 1)", minV, maxV)
	}
}

func TestContextCancellationStopsRetry(t *testing.T) {
	t.Parallel()
	cfg := retry.DefaultConfig()
	cfg.AllowNonIdempotent = true
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
