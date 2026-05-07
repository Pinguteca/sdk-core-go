// Package errors centralizes inspection of Connect errors: code classification,
// retry-info extraction, and detail decoding. Higher-level interceptors compose
// these helpers instead of poking at *connect.Error directly.
package errors

import (
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/proto"
)

// AsConnect unwraps to a *connect.Error or returns false. Convenience over [errors.As].
func AsConnect(err error) (*connect.Error, bool) {
	if err == nil {
		return nil, false
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}

// Code returns the Connect code for err, or [connect.CodeUnknown] if err is nil
// or not a Connect error.
func Code(err error) connect.Code {
	ce, ok := AsConnect(err)
	if !ok {
		return connect.CodeUnknown
	}
	return ce.Code()
}

// RetryDelay extracts a server-suggested retry delay from a [google.rpc.RetryInfo]
// detail attached to err. Returns (0, false) when no RetryInfo is present.
//
// Servers should attach RetryInfo to UNAVAILABLE / RESOURCE_EXHAUSTED responses
// when they know how long the client should back off.
func RetryDelay(err error) (time.Duration, bool) {
	ce, ok := AsConnect(err)
	if !ok {
		return 0, false
	}
	for _, d := range ce.Details() {
		msg, derr := d.Value()
		if derr != nil {
			continue
		}
		if ri, ok := msg.(*errdetails.RetryInfo); ok {
			return ri.GetRetryDelay().AsDuration(), true
		}
	}
	return 0, false
}

// FieldViolations returns every [google.rpc.BadRequest_FieldViolation] attached
// to err. Empty slice when err carries no BadRequest detail.
func FieldViolations(err error) []*errdetails.BadRequest_FieldViolation {
	ce, ok := AsConnect(err)
	if !ok {
		return nil
	}
	var out []*errdetails.BadRequest_FieldViolation
	for _, d := range ce.Details() {
		msg, derr := d.Value()
		if derr != nil {
			continue
		}
		if br, ok := msg.(*errdetails.BadRequest); ok {
			out = append(out, br.GetFieldViolations()...)
		}
	}
	return out
}

// Detail returns the first detail of type T attached to err, or (zero, false).
func Detail[T proto.Message](err error) (T, bool) {
	var zero T
	ce, ok := AsConnect(err)
	if !ok {
		return zero, false
	}
	for _, d := range ce.Details() {
		msg, derr := d.Value()
		if derr != nil {
			continue
		}
		if v, ok := msg.(T); ok {
			return v, true
		}
	}
	return zero, false
}
