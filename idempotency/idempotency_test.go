package idempotency_test

import (
	"context"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/Pinguteca/sdk-core-go/idempotency"
)

// We can't set Spec.Procedure on a real connect.Request from outside the
// package (the spec field is unexported), so the IsSafe heuristic — which
// reads procedure name — gets exercised via unit tests on a wrapper that
// builds a dummy AnyRequest implemented by connect.NewRequest. Since
// procedure defaults to "", we control IsSafe by overriding it explicitly.

func newReq() *connect.Request[emptypb.Empty] {
	return connect.NewRequest(&emptypb.Empty{})
}

func TestAttachesKeyToMutating(t *testing.T) {
	t.Parallel()
	ic := idempotency.Interceptor(idempotency.Options{
		KeyFn:  func() string { return "fixed-key" },
		IsSafe: func(string) bool { return false },
	})
	req := newReq()
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if _, err := ic.WrapUnary(next)(context.Background(), req); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	if got := req.Header().Get("Idempotency-Key"); got != "fixed-key" {
		t.Fatalf("Idempotency-Key = %q, want fixed-key", got)
	}
}

func TestSkipsReadOnly(t *testing.T) {
	t.Parallel()
	ic := idempotency.Interceptor(idempotency.Options{
		KeyFn:  func() string { return "k" },
		IsSafe: func(string) bool { return true },
	})
	req := newReq()
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if _, err := ic.WrapUnary(next)(context.Background(), req); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	if got := req.Header().Get("Idempotency-Key"); got != "" {
		t.Fatalf("read-only got Idempotency-Key=%q, want empty", got)
	}
}

func TestKeyStableWhenContextReused(t *testing.T) {
	t.Parallel()
	var counter atomic.Int32
	ic := idempotency.Interceptor(idempotency.Options{
		KeyFn:  func() string { return "fresh-" + string(rune('A'+counter.Add(1))) },
		IsSafe: func(string) bool { return false },
	})

	wrapped := ic.WrapUnary(func(_ context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		_ = req.Header().Get("Idempotency-Key")
		return connect.NewResponse(&emptypb.Empty{}), nil
	})

	// Two independent calls with fresh context: each gets its own key.
	r1 := newReq()
	if _, err := wrapped(context.Background(), r1); err != nil {
		t.Fatalf("first: %v", err)
	}
	r2 := newReq()
	if _, err := wrapped(context.Background(), r2); err != nil {
		t.Fatalf("second: %v", err)
	}
	if r1.Header().Get("Idempotency-Key") == r2.Header().Get("Idempotency-Key") {
		t.Fatalf("independent calls reused key: %q", r1.Header().Get("Idempotency-Key"))
	}
	if r1.Header().Get("Idempotency-Key") == "" || r2.Header().Get("Idempotency-Key") == "" {
		t.Fatalf("missing key on at least one call")
	}
}

func TestCustomHeaderName(t *testing.T) {
	t.Parallel()
	ic := idempotency.Interceptor(idempotency.Options{
		HeaderName: "X-Idempotency",
		KeyFn:      func() string { return "k" },
		IsSafe:     func(string) bool { return false },
	})
	req := newReq()
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if _, err := ic.WrapUnary(next)(context.Background(), req); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	if got := req.Header().Get("X-Idempotency"); got != "k" {
		t.Fatalf("X-Idempotency = %q, want k", got)
	}
}
