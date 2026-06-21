package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// DiscoverConfig parameterises the OIDC discovery call.
type DiscoverConfig struct {
	// Client is the HTTP client used for the discovery request.
	// Required. The SDK does not assume a default so timeouts and
	// TLS config stay under caller control.
	Client *http.Client
	// Issuer is the OIDC issuer URL. Must use https. The discovery
	// call appends /.well-known/openid-configuration and validates
	// that the response's issuer field matches this value exactly
	// per RFC 8414 §3.3.
	Issuer string
}

// OidcMtlsEndpointAliases mirrors the optional metadata field
// pinned by RFC 8705 §5. When present, the resource server signals
// that mTLS-authenticated callers should use the aliased endpoints
// instead of the standard ones.
type OidcMtlsEndpointAliases struct {
	TokenEndpoint         string `json:"token_endpoint,omitempty"`
	RevocationEndpoint    string `json:"revocation_endpoint,omitempty"`
	IntrospectionEndpoint string `json:"introspection_endpoint,omitempty"`
	UserInfoEndpoint      string `json:"userinfo_endpoint,omitempty"`
}

// OidcMetadata is the parsed OIDC discovery document. Only the
// fields this SDK consumes are typed; the rest are dropped.
//
// Field order is governed by govet's fieldalignment check: the
// nested pointer first, then strings, then slices.
type OidcMetadata struct {
	MtlsEndpointAliases               *OidcMtlsEndpointAliases `json:"mtls_endpoint_aliases,omitempty"`
	Issuer                            string                   `json:"issuer"`
	AuthorizationEndpoint             string                   `json:"authorization_endpoint,omitempty"`
	TokenEndpoint                     string                   `json:"token_endpoint,omitempty"`
	UserInfoEndpoint                  string                   `json:"userinfo_endpoint,omitempty"`
	JwksURI                           string                   `json:"jwks_uri,omitempty"`
	IntrospectionEndpoint             string                   `json:"introspection_endpoint,omitempty"`
	RevocationEndpoint                string                   `json:"revocation_endpoint,omitempty"`
	EndSessionEndpoint                string                   `json:"end_session_endpoint,omitempty"`
	ScopesSupported                   []string                 `json:"scopes_supported,omitempty"`
	ResponseTypesSupported            []string                 `json:"response_types_supported,omitempty"`
	GrantTypesSupported               []string                 `json:"grant_types_supported,omitempty"`
	TokenEndpointAuthMethodsSupported []string                 `json:"token_endpoint_auth_methods_supported,omitempty"`
	CodeChallengeMethodsSupported     []string                 `json:"code_challenge_methods_supported,omitempty"`
}

// Discover fetches the OIDC discovery document at
// <issuer>/.well-known/openid-configuration, validates that the
// returned issuer matches the configured value exactly per RFC
// 8414 §3.3, and returns the parsed metadata.
//
// Discovery is uncached at the SDK layer per RFC 0017. Consumers
// wrap with their own cache when TTL policy matters.
//
// All errors surface as [*OAuthError] except for context
// cancellation and HTTP request build failures, which are wrapped
// with [fmt.Errorf] so callers can branch on errors.Is.
func Discover(ctx context.Context, cfg DiscoverConfig) (*OidcMetadata, error) {
	if err := validateDiscoverConfig(cfg); err != nil {
		return nil, err
	}

	docURL := strings.TrimRight(cfg.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, docURL, nil)
	if err != nil {
		return nil, fmt.Errorf("oauth: build discovery request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := cfg.Client.Do(req)
	if err != nil {
		return nil, FromTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	md, err := parseDiscoveryResponse(resp, cfg.Issuer)
	if err != nil {
		return nil, err
	}
	return md, nil
}

func validateDiscoverConfig(cfg DiscoverConfig) error {
	if cfg.Issuer == "" {
		return &OAuthError{Code: ErrorCodeInvalidIssuer, Description: "issuer is required"}
	}
	if cfg.Client == nil {
		return &OAuthError{Code: ErrorCodeInvalidRequest, Description: "DiscoverConfig.Client is required"}
	}
	issuerURL, err := url.Parse(cfg.Issuer)
	if err != nil {
		return &OAuthError{Code: ErrorCodeInvalidIssuer, Description: "issuer is not a valid URL: " + err.Error()}
	}
	if issuerURL.Scheme != "https" {
		return &OAuthError{Code: ErrorCodeInvalidIssuer, Description: "issuer must use https"}
	}
	return nil
}

// discoveryErrorBodyLimit caps how much of an error body we ingest
// into the OAuthError description so a misbehaving server cannot
// pin large response bodies in memory through the SDK.
const discoveryErrorBodyLimit = 4096

func parseDiscoveryResponse(resp *http.Response, expectedIssuer string) (*OidcMetadata, error) {
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, discoveryErrorBodyLimit))
		return nil, &OAuthError{
			Code:        ErrorCodeInvalidIssuer,
			Description: fmt.Sprintf("discovery returned HTTP %d: %s", resp.StatusCode, string(body)),
			HTTPStatus:  resp.StatusCode,
		}
	}

	var md OidcMetadata
	if err := json.NewDecoder(resp.Body).Decode(&md); err != nil {
		return nil, fmt.Errorf("oauth: decode discovery document: %w", err)
	}
	if md.Issuer != expectedIssuer {
		return nil, &OAuthError{
			Code:        ErrorCodeInvalidIssuer,
			Description: fmt.Sprintf("issuer mismatch: requested %q, document returned %q", expectedIssuer, md.Issuer),
		}
	}
	return &md, nil
}
