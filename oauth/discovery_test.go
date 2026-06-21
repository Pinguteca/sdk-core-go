package oauth_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Pinguteca/sdk-core-go/oauth"
)

// startDiscoveryServer returns a TLS test server that serves the
// supplied response body at /.well-known/openid-configuration.
// {{URL}} placeholders in body are substituted with the server's
// own base URL so the handler can self-reference.
func startDiscoveryServer(t *testing.T, status int, body string) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	resolved := strings.ReplaceAll(body, "{{URL}}", srv.URL)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, resolved)
	})
	return srv, srv.URL
}

func TestDiscover_HappyPath(t *testing.T) {
	srv, url := startDiscoveryServer(t, http.StatusOK, `{
		"issuer": "{{URL}}",
		"authorization_endpoint": "{{URL}}/authorize",
		"token_endpoint": "{{URL}}/token",
		"mtls_endpoint_aliases": {
			"token_endpoint": "{{URL}}/mtls/token"
		},
		"code_challenge_methods_supported": ["S256"]
	}`)

	md, err := oauth.Discover(context.Background(), oauth.DiscoverConfig{
		Issuer: url,
		Client: srv.Client(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if md.Issuer != url {
		t.Errorf("Issuer = %q, want %q", md.Issuer, url)
	}
	if md.TokenEndpoint != url+"/token" {
		t.Errorf("TokenEndpoint = %q", md.TokenEndpoint)
	}
	if md.MtlsEndpointAliases == nil || md.MtlsEndpointAliases.TokenEndpoint != url+"/mtls/token" {
		t.Errorf("mtls token endpoint not parsed: %+v", md.MtlsEndpointAliases)
	}
	if len(md.CodeChallengeMethodsSupported) != 1 || md.CodeChallengeMethodsSupported[0] != "S256" {
		t.Errorf("PKCE method support not parsed: %+v", md.CodeChallengeMethodsSupported)
	}
}

func TestDiscover_IssuerMismatch(t *testing.T) {
	srv, url := startDiscoveryServer(t, http.StatusOK, `{"issuer":"https://other.example.com"}`)

	_, err := oauth.Discover(context.Background(), oauth.DiscoverConfig{
		Issuer: url,
		Client: srv.Client(),
	})
	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeInvalidIssuer {
		t.Fatalf("expected ErrorCodeInvalidIssuer, got %v", err)
	}
}

func TestDiscover_NonOKStatus(t *testing.T) {
	srv, url := startDiscoveryServer(t, http.StatusNotFound, `not found`)

	_, err := oauth.Discover(context.Background(), oauth.DiscoverConfig{
		Issuer: url,
		Client: srv.Client(),
	})
	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeInvalidIssuer {
		t.Fatalf("expected ErrorCodeInvalidIssuer, got %v", err)
	}
	if oe.HTTPStatus != http.StatusNotFound {
		t.Errorf("HTTPStatus = %d, want %d", oe.HTTPStatus, http.StatusNotFound)
	}
}

func TestDiscover_RejectsPlaintextIssuer(t *testing.T) {
	_, err := oauth.Discover(context.Background(), oauth.DiscoverConfig{
		Issuer: "http://idp.example.com",
		Client: http.DefaultClient,
	})
	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeInvalidIssuer {
		t.Fatalf("expected plaintext rejection with ErrorCodeInvalidIssuer, got %v", err)
	}
}

func TestDiscover_RequiresClient(t *testing.T) {
	_, err := oauth.Discover(context.Background(), oauth.DiscoverConfig{
		Issuer: "https://idp.example.com",
	})
	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeInvalidRequest {
		t.Fatalf("expected ErrorCodeInvalidRequest, got %v", err)
	}
}

func TestDiscover_RequiresIssuer(t *testing.T) {
	_, err := oauth.Discover(context.Background(), oauth.DiscoverConfig{
		Client: http.DefaultClient,
	})
	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeInvalidIssuer {
		t.Fatalf("expected ErrorCodeInvalidIssuer, got %v", err)
	}
}
