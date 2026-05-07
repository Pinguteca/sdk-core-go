// Package otel exports a Connect client interceptor that opens an OpenTelemetry
// span per RPC, propagates the W3C traceparent header, and forwards an
// X-Request-ID through the call chain (generating one when absent).
//
// The package only exposes a span lifecycle; structured logging is left to the
// caller. Hook into the returned span via [trace.SpanFromContext] inside your
// own logging or metrics code.
package otel

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	sdkerrors "github.com/Pinguteca/sdk-core-go/errors"
)

// Options configure [Interceptor].
type Options struct {
	// TracerName names the tracer in the global provider. Defaults to
	// "github.com/Pinguteca/sdk-core-go/otel".
	TracerName string
	// Propagator overrides the propagator used for traceparent. Defaults to
	// the global propagator.
	Propagator propagation.TextMapPropagator
	// RequestIDHeader names the correlation-ID header. Defaults to "X-Request-ID".
	RequestIDHeader string
	// GenerateRequestID is the source of new IDs when the inbound header is
	// missing. Defaults to UUIDv7. Override under test.
	GenerateRequestID func() string
}

// Interceptor returns a Connect interceptor that opens spans on outbound RPCs
// (client side) and on inbound RPCs (server side). For streaming RPCs the span
// covers the entire stream lifetime.
func Interceptor(opts Options) connect.Interceptor {
	if opts.TracerName == "" {
		opts.TracerName = "github.com/Pinguteca/sdk-core-go/otel"
	}
	if opts.Propagator == nil {
		opts.Propagator = otel.GetTextMapPropagator()
	}
	if opts.RequestIDHeader == "" {
		opts.RequestIDHeader = "X-Request-ID"
	}
	if opts.GenerateRequestID == nil {
		opts.GenerateRequestID = func() string {
			id, err := uuid.NewV7()
			if err != nil {
				return uuid.NewString()
			}
			return id.String()
		}
	}
	return &otelInterceptor{
		opts:   opts,
		tracer: otel.Tracer(opts.TracerName),
	}
}

type otelInterceptor struct {
	opts   Options
	tracer trace.Tracer
}

func (o *otelInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, span, requestID := o.start(ctx, req.Spec(), req.Header(), trace.SpanKindClient)
		defer span.End()

		resp, err := next(ctx, req)
		o.finish(span, err)
		if resp != nil {
			// Echo request id back to caller for log correlation.
			resp.Header().Set(o.opts.RequestIDHeader, requestID)
		}
		return resp, err
	}
}

func (o *otelInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		ctx, span, _ := o.start(ctx, spec, conn.RequestHeader(), trace.SpanKindClient)
		return &tracedClientConn{StreamingClientConn: conn, span: span, ctx: ctx, finish: o.finish}
	}
}

func (o *otelInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, span, _ := o.start(ctx, conn.Spec(), conn.RequestHeader(), trace.SpanKindServer)
		defer span.End()
		err := next(ctx, conn)
		o.finish(span, err)
		return err
	}
}

func (o *otelInterceptor) start(ctx context.Context, spec connect.Spec, hdr http.Header, kind trace.SpanKind) (context.Context, trace.Span, string) {
	// Inbound: extract upstream context. Outbound: continue current.
	if kind == trace.SpanKindServer {
		ctx = o.opts.Propagator.Extract(ctx, propagation.HeaderCarrier(hdr))
	}

	service, method := splitProcedure(spec.Procedure)
	ctx, span := o.tracer.Start(ctx, spec.Procedure,
		trace.WithSpanKind(kind),
		trace.WithAttributes(
			attribute.String("rpc.system", "connect_rpc"),
			attribute.String("rpc.service", service),
			attribute.String("rpc.method", method),
		),
	)

	// Forward / generate the correlation id.
	requestID := hdr.Get(o.opts.RequestIDHeader)
	if requestID == "" {
		requestID = o.opts.GenerateRequestID()
		hdr.Set(o.opts.RequestIDHeader, requestID)
	}
	span.SetAttributes(attribute.String("request.id", requestID))

	// Outbound: inject traceparent so the server sees the same trace.
	if kind == trace.SpanKindClient {
		o.opts.Propagator.Inject(ctx, propagation.HeaderCarrier(hdr))
	}
	return ctx, span, requestID
}

func (o *otelInterceptor) finish(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(attribute.String("rpc.connect.code", sdkerrors.Code(err).String()))
		return
	}
	span.SetStatus(codes.Ok, "")
}

func splitProcedure(p string) (service, method string) {
	// Connect procedures look like /package.Service/Method.
	p = strings.TrimPrefix(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i], p[i+1:]
	}
	return p, ""
}

type tracedClientConn struct {
	connect.StreamingClientConn
	span   trace.Span
	ctx    context.Context //nolint:containedctx // span lifetime mirrors stream lifetime by design
	finish func(trace.Span, error)
}

func (t *tracedClientConn) CloseRequest() error {
	err := t.StreamingClientConn.CloseRequest()
	if err != nil {
		t.finish(t.span, err)
	}
	return err
}

func (t *tracedClientConn) CloseResponse() error {
	err := t.StreamingClientConn.CloseResponse()
	t.finish(t.span, err)
	t.span.End()
	return err
}
