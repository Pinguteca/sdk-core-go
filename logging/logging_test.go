package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/Pinguteca/sdk-core-go/logging"
)

func newReq() *connect.Request[emptypb.Empty] {
	return connect.NewRequest(&emptypb.Empty{})
}

func newLoggerInto(buf *bytes.Buffer) *slog.Logger {
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h)
}

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	return m
}

func TestNilLoggerErrors(t *testing.T) {
	t.Parallel()
	if _, err := logging.Interceptor(logging.Config{}); err == nil {
		t.Fatalf("expected error for nil Logger")
	}
}

func TestSuccessRecord(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	cfg := logging.DefaultConfig()
	cfg.Logger = newLoggerInto(&buf)
	ic, err := logging.Interceptor(cfg)
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}

	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if _, err := ic.WrapUnary(next)(context.Background(), newReq()); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}

	rec := decode(t, &buf)
	if rec["level"] != "INFO" {
		t.Fatalf("level = %v, want INFO", rec["level"])
	}
	if rec["msg"] != "rpc" {
		t.Fatalf("msg = %v, want rpc", rec["msg"])
	}
	if rec["rpc.code"] != "OK" {
		t.Fatalf("rpc.code = %v, want OK", rec["rpc.code"])
	}
	if rec["rpc.system"] != "connect_rpc" {
		t.Fatalf("rpc.system = %v, want connect_rpc", rec["rpc.system"])
	}
	if _, ok := rec["rpc.duration_ms"]; !ok {
		t.Fatalf("missing rpc.duration_ms")
	}
}

func TestErrorRecord(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	cfg := logging.DefaultConfig()
	cfg.Logger = newLoggerInto(&buf)
	ic, err := logging.Interceptor(cfg)
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}

	wantErr := connect.NewError(connect.CodeUnavailable, errors.New("upstream"))
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, wantErr
	}
	_, _ = ic.WrapUnary(next)(context.Background(), newReq())

	rec := decode(t, &buf)
	if rec["level"] != "ERROR" {
		t.Fatalf("level = %v, want ERROR", rec["level"])
	}
	if rec["rpc.code"] != "unavailable" {
		t.Fatalf("rpc.code = %v, want unavailable", rec["rpc.code"])
	}
	if _, ok := rec["error"]; !ok {
		t.Fatalf("missing error attr")
	}
}

func TestRequestIDPickup(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	cfg := logging.DefaultConfig()
	cfg.Logger = newLoggerInto(&buf)
	ic, err := logging.Interceptor(cfg)
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}

	req := newReq()
	req.Header().Set("X-Request-ID", "abc-123")
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if _, err := ic.WrapUnary(next)(context.Background(), req); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	rec := decode(t, &buf)
	if rec["request.id"] != "abc-123" {
		t.Fatalf("request.id = %v, want abc-123", rec["request.id"])
	}
}

func TestAddResponseAttrs(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	cfg := logging.DefaultConfig()
	cfg.Logger = newLoggerInto(&buf)
	cfg.AddResponseAttrs = func(context.Context, connect.AnyResponse, error) []slog.Attr {
		return []slog.Attr{slog.String("tenant", "acme")}
	}
	ic, err := logging.Interceptor(cfg)
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}

	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if _, err := ic.WrapUnary(next)(context.Background(), newReq()); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	rec := decode(t, &buf)
	if rec["tenant"] != "acme" {
		t.Fatalf("tenant = %v, want acme", rec["tenant"])
	}
}

func TestRedactedHeaders(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	cfg := logging.DefaultConfig()
	cfg.Logger = newLoggerInto(&buf)
	cfg.LogHeaders = true
	ic, err := logging.Interceptor(cfg)
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}

	req := newReq()
	req.Header().Set("Authorization", "Bearer secret")
	req.Header().Set("X-Custom", "visible")
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if _, err := ic.WrapUnary(next)(context.Background(), req); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	rec := decode(t, &buf)
	hdrs, ok := rec["rpc.headers"].(map[string]any)
	if !ok {
		t.Fatalf("rpc.headers missing or wrong type: %T", rec["rpc.headers"])
	}
	auth, _ := hdrs["Authorization"].([]any)
	if len(auth) != 1 || auth[0] != "[REDACTED]" {
		t.Fatalf("Authorization not redacted: %v", auth)
	}
	custom, _ := hdrs["X-Custom"].([]any)
	if len(custom) != 1 || custom[0] != "visible" {
		t.Fatalf("X-Custom not preserved: %v", custom)
	}
}

func TestHeadersOmittedWhenLogHeadersFalse(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	cfg := logging.DefaultConfig()
	cfg.Logger = newLoggerInto(&buf)
	// LogHeaders left at default false
	ic, err := logging.Interceptor(cfg)
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}

	req := newReq()
	req.Header().Set("Authorization", "Bearer secret")
	next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	if _, err := ic.WrapUnary(next)(context.Background(), req); err != nil {
		t.Fatalf("WrapUnary: %v", err)
	}
	rec := decode(t, &buf)
	if _, present := rec["rpc.headers"]; present {
		t.Fatalf("rpc.headers must be absent when LogHeaders=false")
	}
}
