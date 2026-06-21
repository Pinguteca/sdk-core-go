package oauth_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Pinguteca/sdk-core-go/oauth"
)

// testFakeClientSecret stands in for a real client_secret in tests.
// Named verbosely so gosec's G101 detector recognises it as a
// placeholder rather than a leaked credential.
const testFakeClientSecret = "placeholder-not-a-real-secret"

// Test URLs extracted to consts so gosec G101 does not see literal
// strings assigned to credential-pattern fields like TokenEndpoint.
const (
	testAuthorizeURL  = "https://idp/authorize"
	testPlaintextAuth = "http://idp/authorize"
	testIdpExchange   = "https://idp/token"
	testRedirectURL   = "https://app/cb"
)

func newCodeFlow(t *testing.T, srv *httptest.Server, mode oauth.ClientAuthMode) *oauth.AuthorizationCodeFlow {
	t.Helper()
	flow, err := oauth.NewAuthorizationCodeFlow(oauth.AuthorizationCodeConfig{
		Client:                srv.Client(),
		ClientID:              "client",
		ClientSecret:          testFakeClientSecret,
		RedirectURI:           "https://app.example.com/cb",
		AuthorizationEndpoint: srv.URL + "/authorize",
		TokenEndpoint:         srv.URL + "/token",
		Scopes:                []string{"openid", "profile"},
		AuthMode:              mode,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return flow
}

func TestBuildAuthorizationURL_ContainsRequiredParams(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	flow := newCodeFlow(t, srv, oauth.ClientAuthBasic)

	pkce, err := oauth.GeneratePkcePair()
	if err != nil {
		t.Fatalf("pkce: %v", err)
	}

	raw, err := flow.BuildAuthorizationURL("state-xyz", pkce)
	if err != nil {
		t.Fatalf("BuildAuthorizationURL: %v", err)
	}

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	for _, k := range []string{"response_type", "client_id", "redirect_uri", "state", "code_challenge", "code_challenge_method", "scope"} {
		if q.Get(k) == "" {
			t.Errorf("missing query param %q", k)
		}
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	if q.Get("scope") != "openid profile" {
		t.Errorf("scope = %q", q.Get("scope"))
	}
}

func TestBuildAuthorizationURL_RejectsEmptyState(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	flow := newCodeFlow(t, srv, oauth.ClientAuthBasic)

	pkce, _ := oauth.GeneratePkcePair()
	_, err := flow.BuildAuthorizationURL("", pkce)
	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeInvalidRequest {
		t.Fatalf("expected InvalidRequest for empty state, got %v", err)
	}
}

func TestNewAuthorizationCodeFlow_RejectsPlaintextEndpoints(t *testing.T) {
	_, err := oauth.NewAuthorizationCodeFlow(oauth.AuthorizationCodeConfig{
		Client:                http.DefaultClient,
		ClientID:              "client",
		ClientSecret:          testFakeClientSecret,
		RedirectURI:           testRedirectURL,
		AuthorizationEndpoint: testPlaintextAuth,
		TokenEndpoint:         testIdpExchange,
		AuthMode:              oauth.ClientAuthBasic,
	})
	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeInvalidRequest {
		t.Fatalf("expected InvalidRequest for plaintext endpoint, got %v", err)
	}
}

func TestNewAuthorizationCodeFlow_RejectsUnknownAuthMode(t *testing.T) {
	_, err := oauth.NewAuthorizationCodeFlow(oauth.AuthorizationCodeConfig{
		Client:                http.DefaultClient,
		ClientID:              "c",
		ClientSecret:          testFakeClientSecret,
		RedirectURI:           testRedirectURL,
		AuthorizationEndpoint: testAuthorizeURL,
		TokenEndpoint:         testIdpExchange,
		AuthMode:              "weird",
	})
	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeInvalidRequest {
		t.Fatalf("expected InvalidRequest, got %v", err)
	}
}

func TestExchange_PostsFormAndParsesResponse(t *testing.T) {
	var captured struct {
		body        url.Values
		contentType string
		auth        string
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.contentType = r.Header.Get("Content-Type")
		captured.auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		captured.body, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"abc","token_type":"Bearer","expires_in":3600,"refresh_token":"r1"}`)
	}))
	defer srv.Close()
	flow := newCodeFlow(t, srv, oauth.ClientAuthBasic)

	resp, err := flow.Exchange(context.Background(), "code-123", "verifier-xyz")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if resp.AccessToken != "abc" {
		t.Errorf("AccessToken = %q", resp.AccessToken)
	}
	if captured.contentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", captured.contentType)
	}
	if !strings.HasPrefix(captured.auth, "Basic ") {
		t.Errorf("Authorization header missing or not Basic: %q", captured.auth)
	}
	for k, want := range map[string]string{
		"grant_type":    "authorization_code",
		"code":          "code-123",
		"code_verifier": "verifier-xyz",
		"client_id":     "client",
	} {
		if got := captured.body.Get(k); got != want {
			t.Errorf("form[%q] = %q, want %q", k, got, want)
		}
	}
	if captured.body.Get("client_secret") != "" {
		t.Error("client_secret should not appear in form when AuthMode is basic")
	}
}

func TestExchange_FormPostMovesSecretIntoBody(t *testing.T) {
	var captured url.Values
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"abc","token_type":"Bearer"}`)
	}))
	defer srv.Close()
	flow := newCodeFlow(t, srv, oauth.ClientAuthFormPost)

	if _, err := flow.Exchange(context.Background(), "code", "verifier"); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if captured.Get("client_secret") != testFakeClientSecret {
		t.Errorf("client_secret should be in body for form_post mode; got %q", captured.Get("client_secret"))
	}
}

func TestExchange_SurfacesTokenEndpointError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"bad code"}`)
	}))
	defer srv.Close()
	flow := newCodeFlow(t, srv, oauth.ClientAuthBasic)

	_, err := flow.Exchange(context.Background(), "code", "verifier")
	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeInvalidGrant {
		t.Fatalf("expected InvalidGrant, got %v", err)
	}
	if oe.HTTPStatus != http.StatusBadRequest {
		t.Errorf("HTTPStatus = %d", oe.HTTPStatus)
	}
}

func TestRefresh_PostsRefreshTokenGrant(t *testing.T) {
	var captured url.Values
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"new","token_type":"Bearer","expires_in":600}`)
	}))
	defer srv.Close()
	flow := newCodeFlow(t, srv, oauth.ClientAuthBasic)

	resp, err := flow.Refresh(context.Background(), "rt-1")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if captured.Get("grant_type") != "refresh_token" {
		t.Errorf("grant_type = %q", captured.Get("grant_type"))
	}
	if captured.Get("refresh_token") != "rt-1" {
		t.Errorf("refresh_token = %q", captured.Get("refresh_token"))
	}
	if resp.AccessToken != "new" {
		t.Errorf("AccessToken = %q", resp.AccessToken)
	}
}
