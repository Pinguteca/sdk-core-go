package oauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// AuthorizationCodeConfig parameterises an authorization_code grant
// flow (RFC 6749 §4.1) with PKCE (RFC 7636). Use
// [AuthorizationCodeFromIssuerConfig] together with
// [NewAuthorizationCodeFlowFromIssuer] when discovery should fill
// the endpoint fields automatically.
type AuthorizationCodeConfig struct {
	Client                *http.Client
	ClientID              string
	ClientSecret          string
	RedirectURI           string
	AuthorizationEndpoint string
	TokenEndpoint         string
	AuthMode              ClientAuthMode
	Scopes                []string
}

// AuthorizationCodeFromIssuerConfig is the discovery-driven variant
// of [AuthorizationCodeConfig]. The SDK fetches OIDC metadata once
// and copies the endpoints over, routing through the mTLS endpoint
// alias when AuthMode is mTLS and the alias is present (RFC 8705).
type AuthorizationCodeFromIssuerConfig struct {
	Client       *http.Client
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURI  string
	AuthMode     ClientAuthMode
	Scopes       []string
}

// AuthorizationCodeFlow is a stateless helper around the three
// authorization_code endpoints: building the authorization URL,
// exchanging a code for tokens, and refreshing on expiry. Pair it
// with [AuthorizationCodeSource] to expose a [auth.RotatingTokenSource]
// that caches and rotates automatically.
type AuthorizationCodeFlow struct {
	cfg AuthorizationCodeConfig
}

// NewAuthorizationCodeFlow constructs a flow against endpoints the
// caller already knows. Returns [*OAuthError] with a recoverable
// code when validation fails.
func NewAuthorizationCodeFlow(cfg AuthorizationCodeConfig) (*AuthorizationCodeFlow, error) {
	if err := validateAuthCodeConfig(cfg); err != nil {
		return nil, err
	}
	return &AuthorizationCodeFlow{cfg: cfg}, nil
}

// NewAuthorizationCodeFlowFromIssuer runs OIDC discovery against
// the issuer (RFC 8414 §3.3) and constructs a flow from the
// resulting metadata. When AuthMode is mTLS and the metadata
// includes mtls_endpoint_aliases.token_endpoint, the flow routes
// token endpoint calls through the alias per RFC 8705 §5.
func NewAuthorizationCodeFlowFromIssuer(
	ctx context.Context,
	cfg AuthorizationCodeFromIssuerConfig,
) (*AuthorizationCodeFlow, error) {
	if cfg.Client == nil {
		return nil, &OAuthError{
			Code:        ErrorCodeInvalidRequest,
			Description: "AuthorizationCodeFromIssuerConfig.Client is required",
		}
	}
	md, err := Discover(ctx, DiscoverConfig{Client: cfg.Client, Issuer: cfg.Issuer})
	if err != nil {
		return nil, err
	}
	tokenEndpoint := md.TokenEndpoint
	if cfg.AuthMode == ClientAuthMtls && md.MtlsEndpointAliases != nil && md.MtlsEndpointAliases.TokenEndpoint != "" {
		tokenEndpoint = md.MtlsEndpointAliases.TokenEndpoint
	}
	return NewAuthorizationCodeFlow(AuthorizationCodeConfig{
		Client:                cfg.Client,
		ClientID:              cfg.ClientID,
		ClientSecret:          cfg.ClientSecret,
		RedirectURI:           cfg.RedirectURI,
		AuthorizationEndpoint: md.AuthorizationEndpoint,
		TokenEndpoint:         tokenEndpoint,
		Scopes:                cfg.Scopes,
		AuthMode:              cfg.AuthMode,
	})
}

// BuildAuthorizationURL produces the URL the consumer redirects the
// user to (RFC 6749 §4.1.1). State must be supplied by the caller
// and is bound to consumer-side session state. The SDK rejects
// empty state per RFC 0017.
func (f *AuthorizationCodeFlow) BuildAuthorizationURL(state string, pkce PkcePair) (string, error) {
	if state == "" {
		return "", &OAuthError{Code: ErrorCodeInvalidRequest, Description: "state is required"}
	}
	if pkce.Challenge == "" {
		return "", &OAuthError{Code: ErrorCodeInvalidVerifier, Description: "PKCE challenge is empty"}
	}
	u, err := url.Parse(f.cfg.AuthorizationEndpoint)
	if err != nil {
		return "", &OAuthError{
			Code:        ErrorCodeInvalidRequest,
			Description: "authorization endpoint is not a valid URL: " + err.Error(),
		}
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", f.cfg.ClientID)
	q.Set("redirect_uri", f.cfg.RedirectURI)
	q.Set("state", state)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", pkce.Method())
	if len(f.cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(f.cfg.Scopes, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Exchange swaps an authorization code for tokens (RFC 6749 §4.1.3).
// Verifier is the PKCE verifier paired with the code_challenge sent
// on the original [AuthorizationCodeFlow.BuildAuthorizationURL] call.
func (f *AuthorizationCodeFlow) Exchange(ctx context.Context, code, verifier string) (*TokenResponse, error) {
	if code == "" {
		return nil, &OAuthError{Code: ErrorCodeInvalidGrant, Description: "code is empty"}
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", f.cfg.RedirectURI)
	form.Set("code_verifier", verifier)
	return f.postToken(ctx, form)
}

// Refresh exchanges a refresh token for a new access token
// (RFC 6749 §6).
func (f *AuthorizationCodeFlow) Refresh(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	if refreshToken == "" {
		return nil, &OAuthError{Code: ErrorCodeInvalidGrant, Description: "refresh_token is empty"}
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	if len(f.cfg.Scopes) > 0 {
		form.Set("scope", strings.Join(f.cfg.Scopes, " "))
	}
	return f.postToken(ctx, form)
}

// tokenEndpointBodyLimit caps how much of the token endpoint
// response we ingest so a misbehaving server cannot pin large
// bodies in memory through the SDK.
const tokenEndpointBodyLimit = 1 << 20 // 1 MiB

func (f *AuthorizationCodeFlow) postToken(ctx context.Context, form url.Values) (*TokenResponse, error) {
	f.attachClientAuth(form)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.cfg.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oauth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if f.cfg.AuthMode == ClientAuthBasic {
		req.SetBasicAuth(f.cfg.ClientID, f.cfg.ClientSecret)
	}

	resp, err := f.cfg.Client.Do(req)
	if err != nil {
		return nil, FromTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, tokenEndpointBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("oauth: read token response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, FromTokenEndpointError(resp.StatusCode, body)
	}
	return ParseTokenResponse(body)
}

// attachClientAuth puts the client credentials into the form when
// AuthMode demands the form-post style. Basic-mode credentials go
// into the Authorization header inside postToken; mTLS uses the
// TLS layer; None sends nothing beyond client_id.
func (f *AuthorizationCodeFlow) attachClientAuth(form url.Values) {
	form.Set("client_id", f.cfg.ClientID)
	if f.cfg.AuthMode == ClientAuthFormPost {
		form.Set("client_secret", f.cfg.ClientSecret)
	}
}

func validateAuthCodeConfig(cfg AuthorizationCodeConfig) error {
	if cfg.Client == nil {
		return &OAuthError{Code: ErrorCodeInvalidRequest, Description: "AuthorizationCodeConfig.Client is required"}
	}
	for field, value := range map[string]string{
		"client_id":              cfg.ClientID,
		"redirect_uri":           cfg.RedirectURI,
		"authorization_endpoint": cfg.AuthorizationEndpoint,
		"token_endpoint":         cfg.TokenEndpoint,
	} {
		if value == "" {
			return &OAuthError{Code: ErrorCodeInvalidRequest, Description: field + " is required"}
		}
	}
	if err := validateHTTPSEndpoint("authorization_endpoint", cfg.AuthorizationEndpoint); err != nil {
		return err
	}
	if err := validateHTTPSEndpoint("token_endpoint", cfg.TokenEndpoint); err != nil {
		return err
	}
	return validateAuthMode(cfg)
}

func validateHTTPSEndpoint(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return &OAuthError{Code: ErrorCodeInvalidRequest, Description: name + " is not a valid URL: " + err.Error()}
	}
	if u.Scheme != "https" {
		return &OAuthError{Code: ErrorCodeInvalidRequest, Description: name + " must use https"}
	}
	return nil
}

func validateAuthMode(cfg AuthorizationCodeConfig) error {
	switch cfg.AuthMode {
	case ClientAuthBasic, ClientAuthFormPost:
		if cfg.ClientSecret == "" {
			return &OAuthError{
				Code:        ErrorCodeInvalidRequest,
				Description: fmt.Sprintf("client_secret is required for AuthMode %q", cfg.AuthMode),
			}
		}
	case ClientAuthNone, ClientAuthMtls:
		// No client_secret needed in either mode.
	case "":
		return &OAuthError{Code: ErrorCodeInvalidRequest, Description: "AuthMode is required"}
	default:
		return &OAuthError{
			Code:        ErrorCodeInvalidRequest,
			Description: fmt.Sprintf("unknown AuthMode %q", cfg.AuthMode),
		}
	}
	return nil
}
