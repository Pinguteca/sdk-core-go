package oauth_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/Pinguteca/sdk-core-go/oauth"
)

func TestLocalEndpointBrokerSource_ExchangesAndCaches(t *testing.T) {
	var calls atomic.Int32
	var captured url.Values
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		captured, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"broker-issued","token_type":"Bearer","expires_in":3600}`)
	}))
	defer srv.Close()

	src, err := oauth.NewLocalEndpointBrokerSource(oauth.LocalEndpointBrokerConfig{
		Client:   srv.Client(),
		Endpoint: srv.URL,
		Audience: "https://api.example.com",
		Scopes:   []string{"read"},
	})
	if err != nil {
		t.Fatalf("NewLocalEndpointBrokerSource: %v", err)
	}

	for range 3 {
		tok, err := src.Token(context.Background())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if tok != "broker-issued" {
			t.Errorf("token = %q", tok)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("broker hit %d times, want 1 (cache should serve)", got)
	}
	if captured.Get("audience") != "https://api.example.com" {
		t.Errorf("audience = %q", captured.Get("audience"))
	}
	if captured.Get("scope") != "read" {
		t.Errorf("scope = %q", captured.Get("scope"))
	}
	if src.Origin() != "local-endpoint" {
		t.Errorf("Origin = %q", src.Origin())
	}
}

func TestLocalEndpointBrokerSource_SurfacesBrokerUnauthorised(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `not authorised`)
	}))
	defer srv.Close()

	src, err := oauth.NewLocalEndpointBrokerSource(oauth.LocalEndpointBrokerConfig{
		Client:   srv.Client(),
		Endpoint: srv.URL,
	})
	if err != nil {
		t.Fatalf("NewLocalEndpointBrokerSource: %v", err)
	}
	_, err = src.Token(context.Background())
	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeBrokerUnauthorised {
		t.Fatalf("expected ErrorCodeBrokerUnauthorised, got %v", err)
	}
}

func TestLocalEndpointBrokerSource_SurfacesBrokerUnavailable(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `boom`)
	}))
	defer srv.Close()

	src, err := oauth.NewLocalEndpointBrokerSource(oauth.LocalEndpointBrokerConfig{
		Client:   srv.Client(),
		Endpoint: srv.URL,
	})
	if err != nil {
		t.Fatalf("NewLocalEndpointBrokerSource: %v", err)
	}
	_, err = src.Token(context.Background())
	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeBrokerUnavailable {
		t.Fatalf("expected ErrorCodeBrokerUnavailable, got %v", err)
	}
}

func TestLocalEndpointBrokerSource_RejectsPlaintextNonLoopback(t *testing.T) {
	_, err := oauth.NewLocalEndpointBrokerSource(oauth.LocalEndpointBrokerConfig{
		Client:   http.DefaultClient,
		Endpoint: "http://broker.example.com/token",
	})
	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeInvalidRequest {
		t.Fatalf("expected InvalidRequest, got %v", err)
	}
}

func TestLocalEndpointBrokerSource_AllowsPlaintextLoopback(t *testing.T) {
	_, err := oauth.NewLocalEndpointBrokerSource(oauth.LocalEndpointBrokerConfig{
		Client:   http.DefaultClient,
		Endpoint: "http://127.0.0.1:8200/v1/auth/token",
	})
	if err != nil {
		t.Fatalf("loopback endpoint should be accepted, got %v", err)
	}
}

func TestLocalEndpointBrokerSource_InvalidateForcesRefresh(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"t","token_type":"Bearer","expires_in":3600}`)
	}))
	defer srv.Close()

	src, _ := oauth.NewLocalEndpointBrokerSource(oauth.LocalEndpointBrokerConfig{
		Client:   srv.Client(),
		Endpoint: srv.URL,
	})
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("first Token: %v", err)
	}
	src.Invalidate()
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("post-invalidate Token: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("broker hit %d times, want 2 after Invalidate", got)
	}
}

func TestHeaderPassthroughSource_ServesAndUpdates(t *testing.T) {
	src := oauth.NewHeaderPassthroughSource("first")
	if src.Origin() != "header-passthrough" {
		t.Errorf("Origin = %q", src.Origin())
	}
	tok, err := src.Token(context.Background())
	if err != nil || tok != "first" {
		t.Errorf("Token = (%q, %v), want (first, nil)", tok, err)
	}
	src.SetToken("second")
	tok, _ = src.Token(context.Background())
	if tok != "second" {
		t.Errorf("after SetToken: token = %q, want second", tok)
	}
}

func TestHeaderPassthroughSource_EmptyTokenSurfacesUnauthorised(t *testing.T) {
	src := oauth.NewHeaderPassthroughSource("")
	_, err := src.Token(context.Background())
	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeBrokerUnauthorised {
		t.Fatalf("expected ErrorCodeBrokerUnauthorised, got %v", err)
	}
}

func TestHeaderPassthroughSource_InvalidateClearsToken(t *testing.T) {
	src := oauth.NewHeaderPassthroughSource("seeded")
	src.Invalidate()
	_, err := src.Token(context.Background())
	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeBrokerUnauthorised {
		t.Fatalf("expected ErrorCodeBrokerUnauthorised after Invalidate, got %v", err)
	}
}

func TestBrokerSource_BothImplementInterface(t *testing.T) {
	// Compile-time check that both concrete types satisfy
	// [BrokerSource]. If either drops a method this fails to build.
	var _ oauth.BrokerSource = (*oauth.LocalEndpointBrokerSource)(nil)
	var _ oauth.BrokerSource = (*oauth.HeaderPassthroughSource)(nil)
}
