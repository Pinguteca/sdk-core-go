// Package idempotency attaches a stable Idempotency-Key header to mutating
// unary RPCs. The same key is reused across retry attempts so the server can
// safely deduplicate. Read-only methods are skipped by default.
//
// Composes with [github.com/Pinguteca/sdk-core-go/retry]: place this
// interceptor *before* retry so retried attempts pick up the cached key.
package idempotency

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
)

// Options configure [Interceptor].
type Options struct {
	// HeaderName overrides "Idempotency-Key". Required header per RFC 7240
	// extension; some IdPs use a vendor-specific name.
	HeaderName string
	// KeyFn generates a fresh key. Defaults to a UUIDv7 (time-ordered, 128-bit).
	// Override to inject a deterministic generator under test.
	KeyFn func() string
	// IsSafe returns true for procedures that do not need an idempotency key
	// (read-only RPCs). Default heuristic: procedure final segment starts with
	// Get, List, Read, Watch, Search, Query, or Lookup.
	IsSafe func(procedure string) bool
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
		// Cache the key on the context-scoped store so retry replays reuse it.
		key := keyFromContext(ctx)
		if key == "" {
			key = i.opts.KeyFn()
			ctx = withKey(ctx, key)
		}
		req.Header().Set(i.opts.HeaderName, key)
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

type keyCtxKey struct{}

func withKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, keyCtxKey{}, key)
}

func keyFromContext(ctx context.Context) string {
	v, _ := ctx.Value(keyCtxKey{}).(string)
	return v
}
