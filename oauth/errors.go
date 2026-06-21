package oauth

import (
	"encoding/json"
	"errors"
	"fmt"
)

// OAuthError is the typed boundary returned by every operation in
// this package. It carries the RFC 6749 §5.2 error code along with
// the optional description and uri fields, the HTTP status of the
// failing request, and any wrapped transport error.
//
// SDK consumers branch on Code to decide whether the fault is
// recoverable. invalid_grant means re-auth is required, not a
// retry of the same request.
// Field order is governed by govet's fieldalignment check:
// pointer-bearing fields first to shorten the GC scan mask.
type OAuthError struct {
	// Err wraps the underlying transport error when the failure
	// was network-level. Nil when the failure was a server-returned
	// OAuth error.
	Err error
	// Code is the RFC 6749 §5.2 token endpoint error code, an OIDC
	// discovery code, a PKCE validation code, or a broker-origin
	// code defined by RFC 0019.
	Code string
	// Description is the optional human-readable explanation from
	// the server (RFC 6749 §5.2 error_description).
	Description string
	// URI is the optional reference URL from the server
	// (RFC 6749 §5.2 error_uri).
	URI string
	// HTTPStatus is the HTTP status of the failing token endpoint
	// call. Zero when the failure was network-level.
	HTTPStatus int
}

// Standard RFC 6749 §5.2 token endpoint error codes plus the
// non-server codes the SDK adds for discovery, PKCE, and transport
// failures.
const (
	ErrorCodeInvalidRequest       = "invalid_request"
	ErrorCodeInvalidClient        = "invalid_client"
	ErrorCodeInvalidGrant         = "invalid_grant"
	ErrorCodeUnauthorizedClient   = "unauthorized_client"
	ErrorCodeUnsupportedGrantType = "unsupported_grant_type"
	ErrorCodeInvalidScope         = "invalid_scope"

	// ErrorCodeInvalidIssuer marks an OIDC discovery response whose
	// issuer field does not exactly match the requested issuer per
	// RFC 8414 §3.3.
	ErrorCodeInvalidIssuer = "invalid_issuer"
	// ErrorCodeInvalidVerifier marks a PKCE verifier that does not
	// satisfy the RFC 7636 §4.1 length constraints.
	ErrorCodeInvalidVerifier = "invalid_verifier"
	// ErrorCodeTransport marks a network-level failure with no
	// server-side OAuth error payload.
	ErrorCodeTransport = "transport"
)

// Error implements [error].
func (e *OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("oauth: %s: %s", e.Code, e.Description)
	}
	return "oauth: " + e.Code
}

// Unwrap returns the underlying transport error when present so
// callers can chain through [errors.Is] and [errors.As].
func (e *OAuthError) Unwrap() error { return e.Err }

// Is reports whether target is another *OAuthError with the same
// Code. The check ignores Description, URI, HTTPStatus, and Err.
func (e *OAuthError) Is(target error) bool {
	var other *OAuthError
	if !errors.As(target, &other) {
		return false
	}
	return e.Code == other.Code
}

// FromTokenEndpointError parses an RFC 6749 §5.2 error body
// returned by a non-2xx response from the token endpoint. Falls
// back to invalid_request with the raw body in Description when
// the body is not the expected JSON shape (which servers
// occasionally return on infrastructure-level failures).
func FromTokenEndpointError(httpStatus int, body []byte) *OAuthError {
	var payload struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		ErrorURI         string `json:"error_uri"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Error == "" {
		return &OAuthError{
			Code:        ErrorCodeInvalidRequest,
			Description: string(body),
			HTTPStatus:  httpStatus,
		}
	}
	return &OAuthError{
		Code:        payload.Error,
		Description: payload.ErrorDescription,
		URI:         payload.ErrorURI,
		HTTPStatus:  httpStatus,
	}
}

// FromTransportError wraps a network-level failure in an
// [OAuthError] with code [ErrorCodeTransport]. The original error
// is preserved through [errors.Is] via Unwrap.
func FromTransportError(err error) *OAuthError {
	return &OAuthError{
		Code: ErrorCodeTransport,
		Err:  err,
	}
}
