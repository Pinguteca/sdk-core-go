package timeout_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/Pinguteca/sdk-core-go/timeout"
)

type (
	stubReq  struct{ connect.AnyRequest }
	stubResp struct{ connect.AnyResponse }
)

func newReq() connect.AnyRequest { return stubReq{} }

func TestInterceptor_AppliesDefaultWhenNoDeadline(t *testing.T) {
	ic := timeout.Interceptor(timeout.Config{Default: 250 * time.Millisecond})

	var observed time.Duration
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		dl, ok := ctx.Deadline()
		if !ok {
			t.Fatal("inner ctx has no deadline; interceptor did not apply default")
		}
		observed = time.Until(dl)
		return stubResp{}, nil
	}

	if _, err := ic.WrapUnary(next)(context.Background(), newReq()); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	if observed > 250*time.Millisecond || observed < 200*time.Millisecond {
		t.Errorf("observed deadline %v, want close to 250ms", observed)
	}
}

func TestInterceptor_DoesNotExtendTighterCallerDeadline(t *testing.T) {
	ic := timeout.Interceptor(timeout.Config{Default: 10 * time.Second})

	tighten := 50 * time.Millisecond
	parent, cancel := context.WithTimeout(context.Background(), tighten)
	defer cancel()

	var observed time.Duration
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		dl, _ := ctx.Deadline()
		observed = time.Until(dl)
		return stubResp{}, nil
	}
	if _, err := ic.WrapUnary(next)(parent, newReq()); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	if observed > tighten {
		t.Errorf("observed deadline %v exceeds caller's %v", observed, tighten)
	}
}

func TestInterceptor_TightensLooseCallerDeadline(t *testing.T) {
	ic := timeout.Interceptor(timeout.Config{Default: 100 * time.Millisecond})

	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var observed time.Duration
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		dl, _ := ctx.Deadline()
		observed = time.Until(dl)
		return stubResp{}, nil
	}
	if _, err := ic.WrapUnary(next)(parent, newReq()); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	if observed > 100*time.Millisecond {
		t.Errorf("observed deadline %v, expected tightened to ~100ms", observed)
	}
}

func TestInterceptor_TranslatesDeadlineExceededToConnectError(t *testing.T) {
	ic := timeout.Interceptor(timeout.Config{Default: 1 * time.Millisecond})

	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	_, err := ic.WrapUnary(next)(context.Background(), newReq())
	if err == nil {
		t.Fatal("expected error from expired deadline, got nil")
	}
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeDeadlineExceeded {
		t.Fatalf("expected connect.CodeDeadlineExceeded, got %v", err)
	}
}

func TestInterceptor_PropagatesNonDeadlineErrors(t *testing.T) {
	ic := timeout.Interceptor(timeout.DefaultConfig())
	sentinel := errors.New("downstream failure")
	next := func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, sentinel
	}
	_, err := ic.WrapUnary(next)(context.Background(), newReq())
	if !errors.Is(err, sentinel) {
		t.Fatalf("non-deadline error should propagate untouched, got %v", err)
	}
}

func TestDefaultConfig_UsesPinnedDefault(t *testing.T) {
	cfg := timeout.DefaultConfig()
	if cfg.Default != timeout.DefaultTimeout {
		t.Errorf("Default = %v, want %v", cfg.Default, timeout.DefaultTimeout)
	}
	if timeout.DefaultTimeout != 30*time.Second {
		t.Errorf("DefaultTimeout = %v, RFC 0021 pins 30s", timeout.DefaultTimeout)
	}
}
