package oauth

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Pinguteca/sdk-core-go/clock"
)

// Broker-origin error codes per sdk-scaffold RFC 0019. Surfaced on
// [*OAuthError] when a Broker-mode source fails so consumers can
// branch on cause without importing broker-specific types.
const (
	ErrorCodeBrokerUnavailable  = "broker_unavailable"
	ErrorCodeBrokerUnauthorised = "broker_unauthorised"
	ErrorCodeBrokerProtocol     = "broker_protocol"
)

// brokerCacheMax is the upper bound on how long a broker-issued
// token stays cached in the SDK, regardless of the broker-supplied
// expires_in. RFC 0019 pins this at 30s because the broker can
// rotate tokens out from under the SDK without warning, and tight
// caching keeps the SDK aligned. Consumers that know their broker
// guarantees longer validity widen this via configuration.
const brokerCacheMax = 30 * time.Second

// BrokerSource is the contract every Broker-mode token source
// satisfies (RFC 0019). Implementations MUST NOT run OIDC
// discovery, sign DPoP proofs, present mTLS client certificates,
// or talk to the IdP directly. They forward tokens issued upstream.
//
// Any [BrokerSource] also satisfies [auth.RotatingTokenSource] via
// the shared Token + Invalidate shape.
type BrokerSource interface {
	Token(ctx context.Context) (string, error)
	Invalidate()
	// Origin returns a stable label identifying the broker
	// implementation, used in error messages and observability.
	Origin() string
}

// LocalEndpointBrokerConfig parameterises a [LocalEndpointBrokerSource].
// The blessed transport: POST a form to a broker-supplied HTTP
// endpoint (typically a sidecar on loopback) and parse the response
// as an OAuth [TokenResponse]. Broker implementations that speak a
// different protocol family implement [BrokerSource] directly.
type LocalEndpointBrokerConfig struct {
	Client           *http.Client
	EndpointParams   url.Values
	Endpoint         string
	Audience         string
	Scopes           []string
	MaxCacheDuration time.Duration
}

// LocalEndpointBrokerSource is the blessed Broker-mode source that
// POSTs to a localhost broker endpoint and forwards the issued
// token on outgoing calls.
type LocalEndpointBrokerSource struct {
	asOf  time.Time
	clk   clock.Clock
	cache *TokenResponse
	cfg   LocalEndpointBrokerConfig
	mu    sync.Mutex
}

// NewLocalEndpointBrokerSource validates the config and returns a
// new source. The first [LocalEndpointBrokerSource.Token] call
// performs the initial broker exchange.
func NewLocalEndpointBrokerSource(cfg LocalEndpointBrokerConfig) (*LocalEndpointBrokerSource, error) {
	if cfg.Client == nil {
		return nil, &OAuthError{Code: ErrorCodeInvalidRequest, Description: "LocalEndpointBrokerConfig.Client is required"}
	}
	if cfg.Endpoint == "" {
		return nil, &OAuthError{Code: ErrorCodeInvalidRequest, Description: "Endpoint is required"}
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, &OAuthError{
			Code:        ErrorCodeInvalidRequest,
			Description: "Endpoint is not a valid URL: " + err.Error(),
		}
	}
	if u.Scheme != schemeHTTPS && !isLoopback(u.Host) {
		return nil, &OAuthError{
			Code:        ErrorCodeInvalidRequest,
			Description: "Endpoint must use https or a loopback host",
		}
	}
	return &LocalEndpointBrokerSource{cfg: cfg, clk: clock.Real()}, nil
}

// Origin implements [BrokerSource].
func (*LocalEndpointBrokerSource) Origin() string { return "local-endpoint" }

// Token implements [BrokerSource]. Returns the cached broker token
// when still valid, otherwise exchanges with the broker.
func (s *LocalEndpointBrokerSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cache != nil && !s.expired() {
		return s.cache.AccessToken, nil
	}
	return s.exchangeLocked(ctx)
}

// Invalidate implements [BrokerSource]. Drops the cached token so
// the next [Token] call performs a fresh broker exchange.
func (s *LocalEndpointBrokerSource) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = nil
}

func (s *LocalEndpointBrokerSource) exchangeLocked(ctx context.Context) (string, error) {
	form := url.Values{}
	if s.cfg.Audience != "" {
		form.Set("audience", s.cfg.Audience)
	}
	if len(s.cfg.Scopes) > 0 {
		form.Set("scope", strings.Join(s.cfg.Scopes, " "))
	}
	maps.Copy(form, s.cfg.EndpointParams)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("oauth: build broker request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		return "", &OAuthError{
			Code:        ErrorCodeBrokerUnavailable,
			Description: "broker endpoint unreachable: " + err.Error(),
			Err:         err,
		}
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, tokenEndpointBodyLimit))
	if err != nil {
		return "", fmt.Errorf("oauth: read broker response: %w", err)
	}
	if statusErr := brokerStatusError(resp.StatusCode, body); statusErr != nil {
		return "", statusErr
	}

	tr, err := ParseTokenResponse(body)
	if err != nil {
		return "", &OAuthError{
			Code:        ErrorCodeBrokerProtocol,
			Description: "broker response is not a valid token response: " + err.Error(),
		}
	}
	s.cache = tr
	s.asOf = s.clk.Now()
	return tr.AccessToken, nil
}

func (s *LocalEndpointBrokerSource) expired() bool {
	if s.cache.ExpiresIn <= 0 {
		return false
	}
	maxDur := s.cfg.MaxCacheDuration
	if maxDur == 0 {
		maxDur = brokerCacheMax
	}
	supplied := time.Duration(s.cache.ExpiresIn) * time.Second
	if supplied < maxDur {
		maxDur = supplied
	}
	// No skew window. The Direct path uses skew to refresh before the
	// IdP's fixed expiry; broker tokens can rotate at any moment so
	// the 30s cap (or consumer override) is the freshness guarantee.
	return s.clk.Now().After(s.asOf.Add(maxDur))
}

// HeaderPassthroughSource forwards a bound token that an upstream
// broker (typically a reverse proxy) attached to the consumer's
// incoming request. Use SetToken to update the cached value when a
// new request arrives. The source MUST NOT contact any IdP and
// performs no caching policy of its own; the broker upstream owns
// rotation.
type HeaderPassthroughSource struct {
	token string
	mu    sync.RWMutex
}

// NewHeaderPassthroughSource returns a source seeded with the
// supplied bound token. The empty string is allowed so consumers
// can construct an unbound instance early and populate it via
// SetToken on each inbound request.
func NewHeaderPassthroughSource(token string) *HeaderPassthroughSource {
	return &HeaderPassthroughSource{token: token}
}

// Origin implements [BrokerSource].
func (*HeaderPassthroughSource) Origin() string { return "header-passthrough" }

// Token implements [BrokerSource]. Returns the currently held
// bound token. Surfaces [ErrorCodeBrokerUnauthorised] when no
// token has been set; the broker-upstream rejection is the
// authoritative signal.
func (s *HeaderPassthroughSource) Token(_ context.Context) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.token == "" {
		return "", &OAuthError{
			Code:        ErrorCodeBrokerUnauthorised,
			Description: "no broker-supplied token; expecting upstream Authorization header",
		}
	}
	return s.token, nil
}

// Invalidate implements [BrokerSource]. Clears the held token so a
// subsequent [SetToken] call must arrive before [Token] succeeds.
func (s *HeaderPassthroughSource) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
}

// SetToken replaces the held bound token. Call this from the
// consumer's HTTP middleware as each inbound request arrives with
// a fresh broker-issued token.
func (s *HeaderPassthroughSource) SetToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = token
}

// brokerStatusError maps the broker's HTTP status to a typed
// [*OAuthError]. Returns nil when the response is in the 2xx range.
func brokerStatusError(status int, body []byte) error {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return &OAuthError{
			Code:        ErrorCodeBrokerUnauthorised,
			Description: fmt.Sprintf("broker rejected the request: HTTP %d: %s", status, string(body)),
			HTTPStatus:  status,
		}
	case status < http.StatusOK || status >= http.StatusMultipleChoices:
		return &OAuthError{
			Code:        ErrorCodeBrokerUnavailable,
			Description: fmt.Sprintf("broker returned HTTP %d: %s", status, string(body)),
			HTTPStatus:  status,
		}
	}
	return nil
}

// isLoopback returns true when host is a literal loopback address
// (with optional port). The broker contract allows plaintext
// loopback for local sidecar transports while rejecting plaintext
// non-loopback at construction.
func isLoopback(host string) bool {
	hostOnly := host
	if i := strings.LastIndex(host, ":"); i >= 0 {
		hostOnly = host[:i]
	}
	switch hostOnly {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	return false
}
