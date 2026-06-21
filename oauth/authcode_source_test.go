package oauth_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Pinguteca/sdk-core-go/oauth"
)

func TestAuthorizationCodeSource_ServesCachedTokenUntilExpiry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"refreshed","token_type":"Bearer","expires_in":3600}`)
	}))
	defer srv.Close()
	flow := newCodeFlow(t, srv, oauth.ClientAuthBasic)

	initial := &oauth.TokenResponse{
		AccessToken:  "initial",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
		RefreshToken: "rt-initial",
	}
	src, err := oauth.NewAuthorizationCodeSource(flow, initial)
	if err != nil {
		t.Fatalf("NewAuthorizationCodeSource: %v", err)
	}

	for i := range 3 {
		tok, err := src.Token(context.Background())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if tok != "initial" {
			t.Errorf("attempt %d: token = %q, want initial", i, tok)
		}
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("token endpoint hit %d times, want 0 (cache should serve)", got)
	}
}

func TestAuthorizationCodeSource_InvalidateForcesRefresh(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"refreshed","token_type":"Bearer","expires_in":3600,"refresh_token":"rt-rolled"}`)
	}))
	defer srv.Close()
	flow := newCodeFlow(t, srv, oauth.ClientAuthBasic)

	initial := &oauth.TokenResponse{
		AccessToken:  "initial",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
		RefreshToken: "rt-initial",
	}
	src, err := oauth.NewAuthorizationCodeSource(flow, initial)
	if err != nil {
		t.Fatalf("NewAuthorizationCodeSource: %v", err)
	}

	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("first Token: %v", err)
	}
	src.Invalidate()
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("post-invalidate Token: %v", err)
	}
	if tok != "refreshed" {
		t.Errorf("token = %q, want refreshed", tok)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("token endpoint hit %d times, want 1 after Invalidate", got)
	}
}

func TestAuthorizationCodeSource_RequiresFlowAndInitial(t *testing.T) {
	if _, err := oauth.NewAuthorizationCodeSource(nil, &oauth.TokenResponse{}); err == nil {
		t.Error("expected error for nil flow")
	}
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	flow := newCodeFlow(t, srv, oauth.ClientAuthBasic)
	if _, err := oauth.NewAuthorizationCodeSource(flow, nil); err == nil {
		t.Error("expected error for nil initial")
	}
}

func TestAuthorizationCodeSource_ReturnsInvalidGrantWhenRefreshAbsent(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	flow := newCodeFlow(t, srv, oauth.ClientAuthBasic)

	// ExpiresIn -1 forces expired() to want refresh; RefreshToken
	// empty means no refresh token is available.
	initial := &oauth.TokenResponse{AccessToken: "stale", TokenType: "Bearer", ExpiresIn: 1}
	src, err := oauth.NewAuthorizationCodeSource(flow, initial)
	if err != nil {
		t.Fatalf("NewAuthorizationCodeSource: %v", err)
	}
	src.Invalidate()
	_, err = src.Token(context.Background())
	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeInvalidGrant {
		t.Fatalf("expected InvalidGrant, got %v", err)
	}
}
