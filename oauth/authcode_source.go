package oauth

import (
	"context"
	"sync"
	"time"
)

// skewWindow is the clock-skew tolerance applied when deciding
// whether the cached access token has expired, pinned by RFC 0017.
const skewWindow = 30 * time.Second

// AuthorizationCodeSource is a [auth.RotatingTokenSource] that
// wraps an [AuthorizationCodeFlow] and the [TokenResponse] returned
// by the original [AuthorizationCodeFlow.Exchange] call. It caches
// the access token until expiry (minus the [skewWindow]), refreshes
// via the stored refresh_token on expiry, and serialises refresh
// under a single-flight mutex so concurrent Token callers produce
// one network call.
//
// Refresh tokens are held in memory only. Consumers that need
// durable storage are expected to wrap this source with their own
// persistence layer; RFC 0017 explicitly leaves backend choice to
// the consumer.
type AuthorizationCodeSource struct {
	flow    *AuthorizationCodeFlow
	now     func() time.Time
	cache   *TokenResponse
	asOf    time.Time
	refresh string
	mu      sync.Mutex
}

// NewAuthorizationCodeSource constructs a rotating token source on
// top of the initial token exchange. Pass the [TokenResponse] from
// [AuthorizationCodeFlow.Exchange] as initial.
func NewAuthorizationCodeSource(flow *AuthorizationCodeFlow, initial *TokenResponse) (*AuthorizationCodeSource, error) {
	if flow == nil {
		return nil, &OAuthError{Code: ErrorCodeInvalidRequest, Description: "AuthorizationCodeFlow is required"}
	}
	if initial == nil {
		return nil, &OAuthError{Code: ErrorCodeInvalidRequest, Description: "initial TokenResponse is required"}
	}
	return &AuthorizationCodeSource{
		flow:    flow,
		now:     time.Now,
		cache:   initial,
		asOf:    time.Now(),
		refresh: initial.RefreshToken,
	}, nil
}

// Token returns a valid access token. Refreshes via the stored
// refresh_token when the cached one is expired or absent.
func (s *AuthorizationCodeSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cache != nil && !s.expired() {
		return s.cache.AccessToken, nil
	}
	if s.refresh == "" {
		return "", &OAuthError{
			Code:        ErrorCodeInvalidGrant,
			Description: "no refresh_token available; re-authentication required",
		}
	}
	resp, err := s.flow.Refresh(ctx, s.refresh)
	if err != nil {
		return "", err
	}
	s.cache = resp
	s.asOf = s.now()
	if resp.RefreshToken != "" {
		s.refresh = resp.RefreshToken
	}
	return resp.AccessToken, nil
}

// Invalidate drops the cached access token. The next [Token] call
// triggers a refresh.
func (s *AuthorizationCodeSource) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = nil
}

func (s *AuthorizationCodeSource) expired() bool {
	if s.cache.ExpiresIn <= 0 {
		return false
	}
	expiry := s.asOf.Add(time.Duration(s.cache.ExpiresIn) * time.Second)
	return s.now().Add(skewWindow).After(expiry)
}
