package ergo

import (
	"context"
	"testing"
)

func TestWithIdempotencyKey_RoundTrip(t *testing.T) {
	ctx := WithIdempotencyKey(context.Background(), "abc")
	got, ok := IdempotencyKey(ctx)
	if !ok || got != "abc" {
		t.Fatalf("got (%q, %v), want (\"abc\", true)", got, ok)
	}
}

func TestWithIdempotencyKey_Empty_IsNoOp(t *testing.T) {
	base := context.Background()
	ctx := WithIdempotencyKey(base, "")
	if ctx != base {
		t.Fatal("empty key should not allocate a new context")
	}
	if _, ok := IdempotencyKey(ctx); ok {
		t.Fatal("expected no key")
	}
}

func TestWithCorrelationID_RoundTrip(t *testing.T) {
	ctx := WithCorrelationID(context.Background(), "trace-1")
	got, ok := CorrelationID(ctx)
	if !ok || got != "trace-1" {
		t.Fatalf("got (%q, %v), want (\"trace-1\", true)", got, ok)
	}
}

func TestWithCorrelationID_Empty_IsNoOp(t *testing.T) {
	base := context.Background()
	ctx := WithCorrelationID(base, "")
	if ctx != base {
		t.Fatal("empty id should not allocate a new context")
	}
}
