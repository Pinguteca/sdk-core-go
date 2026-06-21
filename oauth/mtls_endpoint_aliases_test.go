package oauth_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Pinguteca/sdk-core-go/oauth"
)

// startDualEndpointServer simulates an IdP that exposes both the
// standard token_endpoint and an mTLS-bound alias per RFC 8705 §5.
// Returns counters for each path so tests can assert which one
// received the token request.
func startDualEndpointServer(t *testing.T) (srv *httptest.Server, baseHits, aliasHits *atomic.Int32) {
	t.Helper()
	baseHits = &atomic.Int32{}
	aliasHits = &atomic.Int32{}

	srv = httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{
				"issuer": %q,
				"authorization_endpoint": %q,
				"token_endpoint": %q,
				"mtls_endpoint_aliases": {
					"token_endpoint": %q
				}
			}`,
				srv.URL,
				srv.URL+"/authorize",
				srv.URL+"/token",
				srv.URL+"/mtls/token",
			)
		case "/token":
			baseHits.Add(1)
			tokenOK(w)
		case "/mtls/token":
			aliasHits.Add(1)
			tokenOK(w)
		default:
			http.NotFound(w, r)
		}
	})
	return srv, baseHits, aliasHits
}

func tokenOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"access_token":"abc","token_type":"Bearer","expires_in":3600}`)
}

func TestClientCredentialsFromIssuer_RoutesThroughMtlsAlias(t *testing.T) {
	srv, base, alias := startDualEndpointServer(t)

	src, err := oauth.NewClientCredentialsSourceFromIssuer(context.Background(), oauth.ClientCredentialsFromIssuerConfig{
		Client:   srv.Client(),
		Issuer:   srv.URL,
		ClientID: "svc",
		AuthMode: oauth.ClientAuthMtls,
	})
	if err != nil {
		t.Fatalf("NewClientCredentialsSourceFromIssuer: %v", err)
	}
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got := alias.Load(); got != 1 {
		t.Errorf("mtls alias hit %d times, want 1", got)
	}
	if got := base.Load(); got != 0 {
		t.Errorf("base token endpoint hit %d times, want 0", got)
	}
}

func TestClientCredentialsFromIssuer_UsesBaseTokenEndpointWithoutMtls(t *testing.T) {
	srv, base, alias := startDualEndpointServer(t)

	src, err := oauth.NewClientCredentialsSourceFromIssuer(context.Background(), oauth.ClientCredentialsFromIssuerConfig{
		Client:       srv.Client(),
		Issuer:       srv.URL,
		ClientID:     "svc",
		ClientSecret: testFakeClientSecret,
		AuthMode:     oauth.ClientAuthBasic,
	})
	if err != nil {
		t.Fatalf("NewClientCredentialsSourceFromIssuer: %v", err)
	}
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got := base.Load(); got != 1 {
		t.Errorf("base token endpoint hit %d times, want 1", got)
	}
	if got := alias.Load(); got != 0 {
		t.Errorf("mtls alias hit %d times, want 0", got)
	}
}

func TestAuthorizationCodeFromIssuer_RoutesThroughMtlsAlias(t *testing.T) {
	srv, base, alias := startDualEndpointServer(t)

	flow, err := oauth.NewAuthorizationCodeFlowFromIssuer(context.Background(), oauth.AuthorizationCodeFromIssuerConfig{
		Client:      srv.Client(),
		Issuer:      srv.URL,
		ClientID:    "svc",
		RedirectURI: testRedirectURL,
		AuthMode:    oauth.ClientAuthMtls,
	})
	if err != nil {
		t.Fatalf("NewAuthorizationCodeFlowFromIssuer: %v", err)
	}
	if _, err := flow.Exchange(context.Background(), "code-123", "verifier-xyz"); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if got := alias.Load(); got != 1 {
		t.Errorf("mtls alias hit %d times, want 1", got)
	}
	if got := base.Load(); got != 0 {
		t.Errorf("base token endpoint hit %d times, want 0", got)
	}
}

func TestAuthorizationCodeFromIssuer_FallsBackToBaseWhenAliasAbsent(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	var aliasHits, baseHits atomic.Int32

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			// No mtls_endpoint_aliases. Even with AuthMode=Mtls the
			// SDK should fall back to the standard token_endpoint.
			_, _ = fmt.Fprintf(w, `{
				"issuer": %q,
				"authorization_endpoint": %q,
				"token_endpoint": %q
			}`, srv.URL, srv.URL+"/authorize", srv.URL+"/token")
		case strings.HasPrefix(r.URL.Path, "/mtls/"):
			aliasHits.Add(1)
			tokenOK(w)
		case r.URL.Path == "/token":
			baseHits.Add(1)
			tokenOK(w)
		default:
			http.NotFound(w, r)
		}
	})

	flow, err := oauth.NewAuthorizationCodeFlowFromIssuer(context.Background(), oauth.AuthorizationCodeFromIssuerConfig{
		Client:      srv.Client(),
		Issuer:      srv.URL,
		ClientID:    "svc",
		RedirectURI: testRedirectURL,
		AuthMode:    oauth.ClientAuthMtls,
	})
	if err != nil {
		t.Fatalf("NewAuthorizationCodeFlowFromIssuer: %v", err)
	}
	if _, err := flow.Exchange(context.Background(), "code", "verifier"); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if got := baseHits.Load(); got != 1 {
		t.Errorf("base hit %d times, want 1 (fallback when alias absent)", got)
	}
	if got := aliasHits.Load(); got != 0 {
		t.Errorf("alias hit %d times, want 0", got)
	}
}
