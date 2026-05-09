package auth

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	sdkerrors "github.com/Pinguteca/sdk-core-go/errors"
)

// RotationOptions configure [RotationInterceptor].
type RotationOptions struct {
	// Source provides the token and exposes invalidation. Required.
	Source RotatingTokenSource
	// AllowNonIdempotent disables the safety gate that skips rotation+retry
	// when the proto schema does not declare the method idempotent.
	//
	// Default false. The original RPC may have been processed server-side
	// before it returned CodeUnauthenticated (the auth check could happen
	// after the mutation in poorly-implemented servers, or the 401 could
	// arrive on the response path while the mutation completed). Retrying a
	// non-idempotent mutation after rotation would risk a duplicate write.
	//
	// Set true only when paired with the idempotency-key interceptor and a
	// server that deduplicates by key.
	AllowNonIdempotent bool
}

// RotationInterceptor returns a Connect interceptor that, on a single
// CodeUnauthenticated response, calls [RotatingTokenSource.Invalidate] on
// Source and retries the call exactly once. The retry attempt re-runs the
// inner auth interceptor, which now sees the invalidated cache and fetches a
// fresh token.
//
// Composition: place this interceptor INSIDE the retry interceptor (closer to
// the network than retry, but still outside the auth interceptor that
// actually attaches the bearer header). The canonical preset order is
// otel, breaker, idempotency, retry, rotation, auth, network.
//
// If the second attempt also fails, the error is returned unchanged. We do
// not loop: a persistent CodeUnauthenticated after rotation indicates bad
// credentials or misconfiguration, not credential expiry, and is unlikely to
// recover from further retries.
//
// Streaming RPCs pass through unchanged: a stream cannot be replayed, so
// rotation does not apply to long-lived bidirectional connections.
func RotationInterceptor(opts RotationOptions) (connect.Interceptor, error) {
	if opts.Source == nil {
		return nil, errors.New("auth: rotation Source is required")
	}
	return &rotationInterceptor{opts: opts}, nil
}

type rotationInterceptor struct{ opts RotationOptions }

func (r *rotationInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !r.opts.AllowNonIdempotent && req.Spec().IdempotencyLevel == connect.IdempotencyUnknown {
			return next(ctx, req)
		}

		resp, err := next(ctx, req)
		if err == nil || sdkerrors.Code(err) != connect.CodeUnauthenticated {
			return resp, err
		}

		// Token was rejected. Drop the cached one so the next call (and the
		// inner auth interceptor) fetches fresh.
		r.opts.Source.Invalidate()
		return next(ctx, req)
	}
}

func (r *rotationInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (r *rotationInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
