// Package logging emits one structured slog record per Connect RPC at
// completion. It implements the canonical-log-and-wide-event pattern
// (see https://loggingsucks.com/): instead of N debug lines per request,
// one wide event per request that carries every attribute needed to
// investigate that call's lifecycle. Tracing remains the right place for
// the causal graph; this is the event store.
//
// Use stdlib log/slog with whatever Handler the caller picks (JSON, text,
// custom). The package emits no logs other than the per-RPC summary.
// Streaming RPCs pass through unchanged: per-message logging contradicts
// the canonical-log model and is left for a future ADR.
package logging

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/trace"

	sdkerrors "github.com/Pinguteca/sdk-core-go/errors"
)

// redactedValue replaces sensitive header values when LogHeaders is enabled.
const redactedValue = "[REDACTED]"

// initialAttrCapacity sizes the slog.Attr slice we build per RPC. Five base
// attrs plus correlation IDs, two caller-hook batches, optional error, and
// optional headers — twelve covers the typical case without re-allocation.
const initialAttrCapacity = 12

// Config tunes the logging interceptor. Use [DefaultConfig] for sensible
// defaults; only Logger is mandatory.
type Config struct {
	Logger           *slog.Logger
	AddRequestAttrs  func(ctx context.Context, req connect.AnyRequest) []slog.Attr
	AddResponseAttrs func(ctx context.Context, resp connect.AnyResponse, err error) []slog.Attr
	Message          string
	RequestIDHeader  string
	RedactHeaders    []string
	LogHeaders       bool
	SuccessLevel     slog.Level
	ErrorLevel       slog.Level
}

// DefaultConfig returns a starting Config with everything except the Logger
// pre-populated. Callers fill Logger and any optional hooks.
func DefaultConfig() Config {
	return Config{
		Message:         "rpc",
		RequestIDHeader: "X-Request-ID",
		SuccessLevel:    slog.LevelInfo,
		ErrorLevel:      slog.LevelError,
		RedactHeaders: []string{
			"authorization",
			"cookie",
			"set-cookie",
			"proxy-authorization",
			"x-api-key",
		},
	}
}

// Interceptor returns the logging interceptor. cfg.Logger is required.
func Interceptor(cfg Config) (connect.Interceptor, error) {
	if cfg.Logger == nil {
		return nil, errors.New("logging: Logger is required")
	}
	if cfg.Message == "" {
		cfg.Message = "rpc"
	}
	if cfg.RequestIDHeader == "" {
		cfg.RequestIDHeader = "X-Request-ID"
	}
	// Normalize redaction list to lower-case for case-insensitive matching.
	for i, h := range cfg.RedactHeaders {
		cfg.RedactHeaders[i] = strings.ToLower(h)
	}
	return &interceptor{cfg: cfg}, nil
}

type interceptor struct{ cfg Config }

func (l *interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := time.Now()
		resp, err := next(ctx, req)
		l.emit(ctx, req, resp, err, time.Since(start))
		return resp, err
	}
}

func (l *interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (l *interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// emit builds and writes the one canonical log record for this RPC.
func (l *interceptor) emit(
	ctx context.Context,
	req connect.AnyRequest,
	resp connect.AnyResponse,
	err error,
	dur time.Duration,
) {
	service, method := splitProcedure(req.Spec().Procedure)
	code := codeString(err)

	// Build attrs in a stable order: identity, timing, status, correlation,
	// caller hooks, redacted headers last (large; least useful for grepping).
	attrs := make([]slog.Attr, 0, initialAttrCapacity)
	attrs = append(attrs,
		slog.String("rpc.system", "connect_rpc"),
		slog.String("rpc.service", service),
		slog.String("rpc.method", method),
		slog.Int64("rpc.duration_ms", dur.Milliseconds()),
		slog.String("rpc.code", code),
	)

	if rid := req.Header().Get(l.cfg.RequestIDHeader); rid != "" {
		attrs = append(attrs, slog.String("request.id", rid))
	}

	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		attrs = append(attrs,
			slog.String("trace.id", sc.TraceID().String()),
			slog.String("span.id", sc.SpanID().String()),
		)
	}

	if l.cfg.AddRequestAttrs != nil {
		attrs = append(attrs, l.cfg.AddRequestAttrs(ctx, req)...)
	}
	if l.cfg.AddResponseAttrs != nil {
		attrs = append(attrs, l.cfg.AddResponseAttrs(ctx, resp, err)...)
	}

	if err != nil {
		attrs = append(attrs, slog.String("error", err.Error()))
	}

	if l.cfg.LogHeaders {
		attrs = append(attrs, slog.Any("rpc.headers", l.redactHeaders(req.Header())))
	}

	level := l.cfg.SuccessLevel
	if err != nil {
		level = l.cfg.ErrorLevel
	}
	l.cfg.Logger.LogAttrs(ctx, level, l.cfg.Message, attrs...)
}

// redactHeaders returns a sanitized copy with sensitive values masked.
func (l *interceptor) redactHeaders(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, vs := range h {
		if l.shouldRedact(k) {
			masked := make([]string, len(vs))
			for i := range vs {
				masked[i] = redactedValue
			}
			out[k] = masked
			continue
		}
		out[k] = vs
	}
	return out
}

func (l *interceptor) shouldRedact(name string) bool {
	lc := strings.ToLower(name)
	return slices.Contains(l.cfg.RedactHeaders, lc)
}

func splitProcedure(p string) (service, method string) {
	p = strings.TrimPrefix(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i], p[i+1:]
	}
	return p, ""
}

// codeString produces a short stable status string suitable for log
// indexing. Wraps the existing sdkerrors.Code helper into a string with the
// "OK" convention for nil errors.
func codeString(err error) string {
	if err == nil {
		return "OK"
	}
	return sdkerrors.Code(err).String()
}
