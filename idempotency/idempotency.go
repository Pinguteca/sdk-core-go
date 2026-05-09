// Package idempotency attaches a stable Idempotency-Key header to mutating
// unary RPCs. The same key is reused across retry attempts so the server can
// safely deduplicate.
//
// Composition: register this interceptor *before* the retry interceptor so
// it fires once per logical call. The key is stored on the request header,
// which retry preserves across attempts because it reuses the same Request
// object. Read-only methods are skipped by default.
package idempotency

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
)

// Options configure [Interceptor].
type Options struct {
	KeyFn      func() string
	IsSafe     func(procedure string) bool
	HeaderName string
}

// Interceptor returns the idempotency-key interceptor. Streaming RPCs are
// passed through untouched.
func Interceptor(opts Options) connect.Interceptor {
	if opts.HeaderName == "" {
		opts.HeaderName = "Idempotency-Key"
	}
	if opts.KeyFn == nil {
		opts.KeyFn = defaultKey
	}
	if opts.IsSafe == nil {
		opts.IsSafe = defaultIsSafe
	}
	return &idempotencyInterceptor{opts: opts}
}

type idempotencyInterceptor struct{ opts Options }

func (i *idempotencyInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if i.opts.IsSafe(req.Spec().Procedure) {
			return next(ctx, req)
		}
		// Reuse any header the caller already attached, or any key set by an
		// earlier wrap. Retry replays the same Request, so the header survives
		// across attempts and the server sees a stable key.
		if req.Header().Get(i.opts.HeaderName) == "" {
			req.Header().Set(i.opts.HeaderName, i.opts.KeyFn())
		}
		return next(ctx, req)
	}
}

func (i *idempotencyInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *idempotencyInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func defaultKey() string {
	id, err := uuid.NewV7()
	if err != nil {
		// uuid.NewV7 only fails when the system clock is broken; fall back to a
		// classic random v4 so the request still ships.
		return uuid.NewString()
	}
	return id.String()
}

// defaultIsSafe heuristically skips read-only procedures. Override IsSafe to
// honour service-specific naming.
func defaultIsSafe(procedure string) bool {
	method := procedure
	if i := strings.LastIndex(procedure, "/"); i >= 0 {
		method = procedure[i+1:]
	}
	for _, prefix := range readOnlyPrefixes {
		if strings.HasPrefix(method, prefix) {
			return true
		}
	}
	return false
}

var readOnlyPrefixes = []string{"Get", "List", "Read", "Watch", "Search", "Query", "Lookup"}
