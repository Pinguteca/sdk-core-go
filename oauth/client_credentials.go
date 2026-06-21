package oauth

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Pinguteca/sdk-core-go/clock"
)

// ClientCredentialsConfig parameterises a client_credentials grant
// flow (RFC 6749 §4.4). For service-to-service callers with their
// own credentials, this is the simplest grant. Pair with
// [NewClientCredentialsSource] for caching and rotation.
type ClientCredentialsConfig struct {
	Client         *http.Client
	EndpointParams url.Values
	ClientID       string
	ClientSecret   string
	TokenEndpoint  string
	AuthMode       ClientAuthMode
	Scopes         []string
}

// ClientCredentialsFromIssuerConfig is the discovery-driven variant
// of [ClientCredentialsConfig]. The SDK fetches OIDC metadata once
// and routes through the mTLS endpoint alias when AuthMode is mTLS
// and the alias is present (RFC 8705 §5).
type ClientCredentialsFromIssuerConfig struct {
	Client         *http.Client
	EndpointParams url.Values
	Issuer         string
	ClientID       string
	ClientSecret   string
	AuthMode       ClientAuthMode
	Scopes         []string
}

// ClientCredentialsSource is a [auth.RotatingTokenSource] backed by
// the client_credentials grant. It caches the access token until
// expiry (minus the [skewWindow]), serialises refresh under a
// single-flight mutex so concurrent Token callers produce one
// network call, and exposes Invalidate so the rotation interceptor
// can drop the cache after a server-side rejection.
type ClientCredentialsSource struct {
	asOf  time.Time
	clk   clock.Clock
	cache *TokenResponse
	cfg   ClientCredentialsConfig
	mu    sync.Mutex
}

// NewClientCredentialsSource constructs a source against endpoints
// the caller already knows. The first [ClientCredentialsSource.Token]
// call performs the initial token endpoint POST.
func NewClientCredentialsSource(cfg ClientCredentialsConfig) (*ClientCredentialsSource, error) {
	if err := validateClientCredentialsConfig(cfg); err != nil {
		return nil, err
	}
	return &ClientCredentialsSource{cfg: cfg, clk: clock.Real()}, nil
}

// NewClientCredentialsSourceFromIssuer runs OIDC discovery against
// the issuer and constructs a source from the resulting metadata.
// Routes token endpoint calls through mtls_endpoint_aliases.token_endpoint
// when AuthMode is mTLS and the alias is present (RFC 8705 §5).
func NewClientCredentialsSourceFromIssuer(
	ctx context.Context,
	cfg ClientCredentialsFromIssuerConfig,
) (*ClientCredentialsSource, error) {
	if cfg.Client == nil {
		return nil, &OAuthError{
			Code:        ErrorCodeInvalidRequest,
			Description: "ClientCredentialsFromIssuerConfig.Client is required",
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
	return NewClientCredentialsSource(ClientCredentialsConfig{
		Client:         cfg.Client,
		EndpointParams: cfg.EndpointParams,
		Scopes:         cfg.Scopes,
		ClientID:       cfg.ClientID,
		ClientSecret:   cfg.ClientSecret,
		TokenEndpoint:  tokenEndpoint,
		AuthMode:       cfg.AuthMode,
	})
}

// Token returns a valid access token, refreshing via the token
// endpoint when the cached one is expired or absent.
func (s *ClientCredentialsSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cache != nil && !s.expired() {
		return s.cache.AccessToken, nil
	}
	return s.refreshLocked(ctx)
}

// Invalidate drops the cached access token. The next [Token] call
// triggers a fresh client_credentials exchange.
func (s *ClientCredentialsSource) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = nil
}

func (s *ClientCredentialsSource) refreshLocked(ctx context.Context) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	if len(s.cfg.Scopes) > 0 {
		form.Set("scope", strings.Join(s.cfg.Scopes, " "))
	}
	maps.Copy(form, s.cfg.EndpointParams)

	resp, err := postTokenRequest(
		ctx,
		s.cfg.Client,
		s.cfg.TokenEndpoint,
		form,
		s.cfg.AuthMode,
		s.cfg.ClientID,
		s.cfg.ClientSecret,
	)
	if err != nil {
		return "", err
	}
	s.cache = resp
	s.asOf = s.clk.Now()
	return resp.AccessToken, nil
}

func (s *ClientCredentialsSource) expired() bool {
	if s.cache.ExpiresIn <= 0 {
		return false
	}
	expiry := s.asOf.Add(time.Duration(s.cache.ExpiresIn) * time.Second)
	return s.clk.Now().Add(skewWindow).After(expiry)
}

func validateClientCredentialsConfig(cfg ClientCredentialsConfig) error {
	if cfg.Client == nil {
		return &OAuthError{Code: ErrorCodeInvalidRequest, Description: "ClientCredentialsConfig.Client is required"}
	}
	if cfg.ClientID == "" {
		return &OAuthError{Code: ErrorCodeInvalidRequest, Description: "client_id is required"}
	}
	if cfg.TokenEndpoint == "" {
		return &OAuthError{Code: ErrorCodeInvalidRequest, Description: "token_endpoint is required"}
	}
	if err := validateHTTPSEndpoint("token_endpoint", cfg.TokenEndpoint); err != nil {
		return err
	}
	return validateClientAuthMode(cfg.AuthMode, cfg.ClientSecret)
}
