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

func newReq() *connect.Request[emptypb.Empty] {
	return connect.NewRequest(&emptypb.Empty{})
}

func TestStaticBearer_AttachesAuthorizationHeader(t *testing.T) {
	t.Parallel()
	src := auth.StaticBearer("abc123")
	ic, err := auth.Interceptor(auth.Options{Source: src})
	if err != nil {
		t.Fatalf("Interceptor: %v", err)
	}

	var seen string
	next := func(_ context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		seen = req.Header().Get("Authorization")
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if _, err := ic.WrapUnary(next)(context.Background(), newReq()); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	if want := "Bearer abc123"; seen != want {
		t.Fatalf("Authorization = %q, want %q", seen, want)
	}
}

func TestInterceptor_RejectsEmptySource(t *testing.T) {
	t.Parallel()
	if _, err := auth.Interceptor(auth.Options{}); err == nil {
		t.Fatalf("expected error when Source is nil")
	}
}

func TestStaticBearer_EmptyTokenIsError(t *testing.T) {
	t.Parallel()
	src := auth.StaticBearer("")
	ic, err := auth.Interceptor(auth.Options{Source: src})
	if err != nil {
		t.Fatalf("Interceptor: %v", err)
	}
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		t.Fatalf("next should not be called when token fails")
		return nil, nil //nolint:nilnil // unreachable test guard
	}
	_, err = ic.WrapUnary(next)(context.Background(), newReq())
	if err == nil {
		t.Fatalf("expected error from empty bearer token")
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected *connect.Error, got %T: %v", err, err)
	}
	if ce.Code() != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", ce.Code())
	}
}

func TestSkipFn_BypassesAttach(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	src := auth.TokenSourceFunc(func(context.Context) (string, error) {
		calls.Add(1)
		return "tok", nil
	})
	ic, err := auth.Interceptor(auth.Options{
		Source: src,
		Skip:   func(p string) bool { return true },
	})
	if err != nil {
		t.Fatalf("Interceptor: %v", err)
	}
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if _, err := ic.WrapUnary(next)(context.Background(), newReq()); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("Skip-ed call still fetched token: %d", got)
	}
}

func TestCustomHeaderAndFormatter(t *testing.T) {
	t.Parallel()
	src := auth.StaticBearer("k")
	ic, err := auth.Interceptor(auth.Options{
		Source:       src,
		HeaderName:   "X-Api-Key",
		FormatHeader: func(token string) string { return token },
	})
	if err != nil {
		t.Fatalf("Interceptor: %v", err)
	}
	req := newReq()
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if _, err := ic.WrapUnary(next)(context.Background(), req); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	if got := req.Header().Get("X-Api-Key"); got != "k" {
		t.Fatalf("X-Api-Key = %q, want k", got)
	}
}
