// Package oauth implements the OAuth 2.0 + OIDC surface defined by
// sdk-scaffold RFC 0017 (in-SDK grants) and RFC 0019 (Direct vs
// Broker deployment presets).
//
// The package ships the same [auth.RotatingTokenSource] contract so
// existing rotation interceptors compose over OAuth-backed sources
// without translation.
//
// Direct vs Broker mode (RFC 0019) is selected by which TokenSource
// implementation the caller wires.
package oauth
