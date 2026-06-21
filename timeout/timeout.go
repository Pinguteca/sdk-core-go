// Package timeout provides a Connect client interceptor that
// guarantees every outgoing call carries a deadline.
//
// The cross-SDK contract (default duration, never-extend rule,
// composition order relative to retry) is pinned in
// sdk-scaffold/docs/rfc/0021-timeout-interceptor.md.
//
// Go-specific note: callers can already pass a context with their
// own deadline via [context.WithTimeout]. The interceptor's
// primary value in Go is the default ceiling when the caller
// passes a deadline-less context. When the caller already
// supplied a tighter deadline, the interceptor is a no-op for
// that call.
package timeout

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	sdkclock "github.com/Pinguteca/sdk-core-go/clock"
)

// DefaultTimeout is the configured ceiling applied to calls whose
// context carries no deadline. Pinned at 30 seconds by RFC 0021.
const DefaultTimeout = 30 * time.Second

// Config tunes the interceptor. Use [DefaultConfig] for the
// RFC 0021 baseline.
type Config struct {
	// Clock supplies the current time. Defaults to [sdkclock.Real]
	// when zero-valued.
	Clock sdkclock.Clock
	// Default is the ceiling applied when the caller's context
	// carries no deadline. When zero, [DefaultTimeout] is used.
	Default time.Duration
}

// DefaultConfig returns the RFC 0021 baseline configuration.
func DefaultConfig() Config {
	return Config{Default: DefaultTimeout}
}

// Interceptor returns a Connect interceptor that ensures every
// outgoing call carries an effective deadline of
// min(caller-deadline, now + cfg.Default).
//
// The interceptor never extends a deadline the caller set. A
// caller who passed a 5-second [context.WithTimeout] never waits
// longer than 5 seconds even if cfg.Default is 30s.
func Interceptor(cfg Config) connect.Interceptor {
	if cfg.Clock == nil {
		cfg.Clock = sdkclock.Real()
	}
	if cfg.Default <= 0 {
		cfg.Default = DefaultTimeout
	}
	return &timeoutInterceptor{cfg: cfg}
}

type timeoutInterceptor struct {
	cfg Config
}

// WrapUnary wraps the supplied [connect.UnaryFunc] with the
// effective-deadline policy.
func (t *timeoutInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, cancel := t.apply(ctx)
		defer cancel()
		resp, err := next(ctx, req)
		if err != nil {
			return nil, t.translateDeadline(ctx, err)
		}
		return resp, nil
	}
}

// WrapStreamingClient wraps the supplied [connect.StreamingClientFunc].
func (t *timeoutInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		ctx, _ = t.apply(ctx)
		return next(ctx, spec)
	}
}

// WrapStreamingHandler wraps the supplied [connect.StreamingHandlerFunc].
func (t *timeoutInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, cancel := t.apply(ctx)
		defer cancel()
		err := next(ctx, conn)
		if err != nil {
			return t.translateDeadline(ctx, err)
		}
		return nil
	}
}

// apply derives a child context whose deadline is the smaller of
// the caller's deadline and now + cfg.Default. Returns a
// no-op cancel when the caller's deadline is already tighter.
func (t *timeoutInterceptor) apply(parent context.Context) (context.Context, context.CancelFunc) {
	configured := t.cfg.Clock.Now().Add(t.cfg.Default)
	if existing, ok := parent.Deadline(); ok && !existing.After(configured) {
		return parent, func() {}
	}
	return context.WithDeadline(parent, configured)
}

// translateDeadline returns a typed Connect error when the
// underlying transport returned [context.DeadlineExceeded] under a
// deadline this interceptor imposed. Other errors propagate
// untouched.
func (t *timeoutInterceptor) translateDeadline(ctx context.Context, err error) error {
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if ctx.Err() == nil {
		return err
	}
	return connect.NewError(connect.CodeDeadlineExceeded, err)
}
