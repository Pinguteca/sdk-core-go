package auth_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/Pinguteca/sdk-core-go/auth"
)

// fakeRotatingSource is a minimal RotatingTokenSource for tests.
type fakeRotatingSource struct {
	tokens          []string
	idx             atomic.Int32
	invalidateCount atomic.Int32
}

func (f *fakeRotatingSource) Token(context.Context) (string, error) {
	i := int(f.idx.Add(1)) - 1
	if i < len(f.tokens) {
		return f.tokens[i], nil
	}
	return f.tokens[len(f.tokens)-1], nil
}

func (f *fakeRotatingSource) Invalidate() {
	f.invalidateCount.Add(1)
}

func newRotReq() *connect.Request[emptypb.Empty] {
	return connect.NewRequest(&emptypb.Empty{})
}

func TestRotationInterceptor_NilSourceErrors(t *testing.T) {
	t.Parallel()
	if _, err := auth.RotationInterceptor(auth.RotationOptions{}); err == nil {
		t.Fatalf("expected error for nil Source")
	}
}

func TestRotationInterceptor_RetriesOn401(t *testing.T) {
	t.Parallel()
	src := &fakeRotatingSource{tokens: []string{"t1", "t2"}}
	ic, err := auth.RotationInterceptor(auth.RotationOptions{
		Source:             src,
		AllowNonIdempotent: true, // skip the gate so the test doesn't depend on Spec
	})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}

	var calls atomic.Int32
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		n := calls.Add(1)
		if n == 1 {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("expired"))
		}
		return connect.NewResponse(&emptypb.Empty{}), nil
	}

	if _, err := ic.WrapUnary(next)(context.Background(), newRotReq()); err != nil {
		t.Fatalf("expected success on second attempt, got %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
	if got := src.invalidateCount.Load(); got != 1 {
		t.Fatalf("invalidate count = %d, want 1", got)
	}
}

func TestRotationInterceptor_DoesNotRetryOnNon401(t *testing.T) {
	t.Parallel()
	src := &fakeRotatingSource{tokens: []string{"t1"}}
	ic, err := auth.RotationInterceptor(auth.RotationOptions{
		Source:             src,
		AllowNonIdempotent: true,
	})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}

	var calls atomic.Int32
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		calls.Add(1)
		return nil, connect.NewError(connect.CodeInternal, errors.New("boom"))
	}

	_, err = ic.WrapUnary(next)(context.Background(), newRotReq())
	if err == nil {
		t.Fatalf("expected error to propagate")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1 (non-401 must not trigger rotation)", got)
	}
	if got := src.invalidateCount.Load(); got != 0 {
		t.Fatalf("invalidate count = %d, want 0", got)
	}
}

func TestRotationInterceptor_RetriesAtMostOnce(t *testing.T) {
	t.Parallel()
	src := &fakeRotatingSource{tokens: []string{"t1", "t2", "t3"}}
	ic, err := auth.RotationInterceptor(auth.RotationOptions{
		Source:             src,
		AllowNonIdempotent: true,
	})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}

	var calls atomic.Int32
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		calls.Add(1)
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("still expired"))
	}

	_, err = ic.WrapUnary(next)(context.Background(), newRotReq())
	if err == nil {
		t.Fatalf("expected error on second 401")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2 (must not loop beyond one rotation)", got)
	}
	if got := src.invalidateCount.Load(); got != 1 {
		t.Fatalf("invalidate count = %d, want 1", got)
	}
}

func TestRotationInterceptor_GatesOnIdempotencyUnknown(t *testing.T) {
	t.Parallel()
	src := &fakeRotatingSource{tokens: []string{"t1"}}
	// AllowNonIdempotent left at default (false). Default Spec.IdempotencyLevel
	// from connect.NewRequest is Unknown, so the gate must skip rotation.
	ic, err := auth.RotationInterceptor(auth.RotationOptions{Source: src})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}

	var calls atomic.Int32
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		calls.Add(1)
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("expired"))
	}

	_, err = ic.WrapUnary(next)(context.Background(), newRotReq())
	if err == nil {
		t.Fatalf("expected error to propagate")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1 (gate must block rotation on Unknown idempotency)", got)
	}
	if got := src.invalidateCount.Load(); got != 0 {
		t.Fatalf("invalidate count = %d, want 0 (gate must block invalidate)", got)
	}
}
