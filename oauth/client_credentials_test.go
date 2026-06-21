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

func newClientCredsSource(t *testing.T, srv *httptest.Server) *oauth.ClientCredentialsSource {
	t.Helper()
	src, err := oauth.NewClientCredentialsSource(oauth.ClientCredentialsConfig{
		Client:        srv.Client(),
		ClientID:      "svc",
		ClientSecret:  testFakeClientSecret,
		TokenEndpoint: srv.URL + "/token",
		Scopes:        []string{"read", "write"},
		AuthMode:      oauth.ClientAuthBasic,
	})
	if err != nil {
		t.Fatalf("NewClientCredentialsSource: %v", err)
	}
	return src
}

func TestClientCredentialsSource_PostsGrantAndCachesAccessToken(t *testing.T) {
	var calls atomic.Int32
	var captured url.Values
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		captured, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"a-1","token_type":"Bearer","expires_in":3600}`)
	}))
	defer srv.Close()
	src := newClientCredsSource(t, srv)

	for i := range 3 {
		tok, err := src.Token(context.Background())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if tok != "a-1" {
			t.Errorf("attempt %d: token = %q, want a-1", i, tok)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("token endpoint hit %d times, want 1 (cache should serve)", got)
	}
	if captured.Get("grant_type") != "client_credentials" {
		t.Errorf("grant_type = %q", captured.Get("grant_type"))
	}
	if captured.Get("scope") != "read write" {
		t.Errorf("scope = %q", captured.Get("scope"))
	}
}

func TestClientCredentialsSource_InvalidateForcesRefresh(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			_, _ = io.WriteString(w, `{"access_token":"first","token_type":"Bearer","expires_in":3600}`)
		default:
			_, _ = io.WriteString(w, `{"access_token":"second","token_type":"Bearer","expires_in":3600}`)
		}
	}))
	defer srv.Close()
	src := newClientCredsSource(t, srv)

	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("first Token: %v", err)
	}
	src.Invalidate()
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("post-invalidate Token: %v", err)
	}
	if tok != "second" {
		t.Errorf("token = %q, want second", tok)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("token endpoint hit %d times, want 2 after Invalidate", got)
	}
}

func TestClientCredentialsSource_PassesEndpointParams(t *testing.T) {
	var captured url.Values
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"abc","token_type":"Bearer"}`)
	}))
	defer srv.Close()
	src, err := oauth.NewClientCredentialsSource(oauth.ClientCredentialsConfig{
		Client:         srv.Client(),
		ClientID:       "svc",
		ClientSecret:   testFakeClientSecret,
		TokenEndpoint:  srv.URL + "/token",
		EndpointParams: url.Values{"audience": {"https://api.example.com"}},
		AuthMode:       oauth.ClientAuthBasic,
	})
	if err != nil {
		t.Fatalf("NewClientCredentialsSource: %v", err)
	}
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if captured.Get("audience") != "https://api.example.com" {
		t.Errorf("audience = %q", captured.Get("audience"))
	}
}

func TestClientCredentialsSource_SurfacesTokenEndpointError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"bad creds"}`)
	}))
	defer srv.Close()
	src := newClientCredsSource(t, srv)

	_, err := src.Token(context.Background())
	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeInvalidClient {
		t.Fatalf("expected InvalidClient, got %v", err)
	}
	if oe.HTTPStatus != http.StatusUnauthorized {
		t.Errorf("HTTPStatus = %d", oe.HTTPStatus)
	}
}

func TestNewClientCredentialsSource_ValidatesConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  oauth.ClientCredentialsConfig
	}{
		{"missing client", oauth.ClientCredentialsConfig{ClientID: "c", TokenEndpoint: testIdpExchange, AuthMode: oauth.ClientAuthBasic, ClientSecret: testFakeClientSecret}},
		{"missing client_id", oauth.ClientCredentialsConfig{Client: http.DefaultClient, TokenEndpoint: testIdpExchange, AuthMode: oauth.ClientAuthBasic, ClientSecret: testFakeClientSecret}},
		{"missing token_endpoint", oauth.ClientCredentialsConfig{Client: http.DefaultClient, ClientID: "c", AuthMode: oauth.ClientAuthBasic, ClientSecret: testFakeClientSecret}},
		{"plaintext token_endpoint", oauth.ClientCredentialsConfig{Client: http.DefaultClient, ClientID: "c", TokenEndpoint: testPlaintextExchange, AuthMode: oauth.ClientAuthBasic, ClientSecret: testFakeClientSecret}},
		{"basic without secret", oauth.ClientCredentialsConfig{Client: http.DefaultClient, ClientID: "c", TokenEndpoint: testIdpExchange, AuthMode: oauth.ClientAuthBasic}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := oauth.NewClientCredentialsSource(tc.cfg)
			var oe *oauth.OAuthError
			if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeInvalidRequest {
				t.Fatalf("expected InvalidRequest, got %v", err)
			}
		})
	}
}
