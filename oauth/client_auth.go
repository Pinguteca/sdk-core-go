package oauth

// ClientAuthMode selects how client credentials reach the token
// endpoint per RFC 6749 §2.3 and RFC 8705 §2. Values match the
// vocabulary the OIDC discovery document's
// token_endpoint_auth_methods_supported field uses.
type ClientAuthMode string

const (
	// ClientAuthBasic puts client_id and client_secret in an HTTP
	// Basic Authorization header per RFC 6749 §2.3.1. Default when a
	// client_secret is configured and mTLS is not in use.
	ClientAuthBasic ClientAuthMode = "basic"
	// ClientAuthFormPost puts client_id and client_secret in the
	// token endpoint request body per RFC 6749 §2.3.1 fallback. Used
	// when the IdP rejects Basic.
	ClientAuthFormPost ClientAuthMode = "form_post"
	// ClientAuthNone is for public clients that prove identity via
	// PKCE alone, with no client_secret.
	ClientAuthNone ClientAuthMode = "none"
	// ClientAuthMtls uses mTLS at the TLS layer (RFC 8705 §2). The
	// SDK presents the client certificate via the http.Client's
	// Transport config; no client_secret is sent.
	ClientAuthMtls ClientAuthMode = "mtls"
)
