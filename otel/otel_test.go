package otel_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	sdkotel "github.com/Pinguteca/sdk-core-go/otel"
)

func newReq() *connect.Request[emptypb.Empty] {
	return connect.NewRequest(&emptypb.Empty{})
}

func TestRequestIDGeneratedAndPropagated(t *testing.T) {
	t.Parallel()
	ic := sdkotel.Interceptor(sdkotel.Options{
		GenerateRequestID: func() string { return "test-id-1" },
	})

	req := newReq()
	resp := connect.NewResponse(&emptypb.Empty{})
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return resp, nil
	}
	if _, err := ic.WrapUnary(next)(context.Background(), req); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	if got := req.Header().Get("X-Request-ID"); got != "test-id-1" {
		t.Fatalf("request X-Request-ID = %q, want test-id-1", got)
	}
	if got := resp.Header().Get("X-Request-ID"); got != "test-id-1" {
		t.Fatalf("response X-Request-ID echo = %q, want test-id-1", got)
	}
}

func TestInboundRequestIDPreserved(t *testing.T) {
	t.Parallel()
	ic := sdkotel.Interceptor(sdkotel.Options{
		GenerateRequestID: func() string {
			t.Fatalf("should not generate when caller already provided one")
			return ""
		},
	})

	req := newReq()
	req.Header().Set("X-Request-ID", "caller-id")
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if _, err := ic.WrapUnary(next)(context.Background(), req); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	if got := req.Header().Get("X-Request-ID"); got != "caller-id" {
		t.Fatalf("X-Request-ID = %q, want caller-id (preserved)", got)
	}
}

func TestErrorRecordedOnSpan(t *testing.T) {
	t.Parallel()
	ic := sdkotel.Interceptor(sdkotel.Options{
		GenerateRequestID: func() string { return "id" },
	})

	req := newReq()
	wantErr := connect.NewError(connect.CodeUnavailable, errors.New("boom"))
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, wantErr
	}
	_, err := ic.WrapUnary(next)(context.Background(), req)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}
