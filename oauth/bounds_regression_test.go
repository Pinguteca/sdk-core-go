package oauth_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Pinguteca/sdk-core-go/oauth"
)

// Regression: parseDiscoveryResponse decoded the success body straight
// off the wire with no bound, so a hostile issuer could allocate
// unbounded memory in the client. The issuer-equality check that would
// reject the document runs only after the decode, so the allocation
// happens even for a document the SDK is about to throw away.
func TestDiscover_RejectsOversizedDocument(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A syntactically valid document padded past the 1 MiB ceiling.
		padding := strings.Repeat("a", 1<<20)
		_, _ = fmt.Fprintf(w, `{"issuer":"https://evil.example","padding":%q}`, padding)
	}))
	defer srv.Close()

	_, err := oauth.Discover(context.Background(), oauth.DiscoverConfig{
		Client: srv.Client(),
		Issuer: srv.URL,
	})

	var oe *oauth.OAuthError
	if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeInvalidIssuer {
		t.Fatalf("expected InvalidIssuer for oversized document, got %v", err)
	}
	if !strings.Contains(oe.Description, "exceeds") {
		t.Errorf("error should name the size ceiling, got %q", oe.Description)
	}
}

// A normally-sized document must still round-trip.
func TestDiscover_AcceptsNormalDocument(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"issuer":%q,"token_endpoint":%q}`, srv.URL, srv.URL+"/token")
	}))
	defer srv.Close()

	md, err := oauth.Discover(context.Background(), oauth.DiscoverConfig{
		Client: srv.Client(),
		Issuer: srv.URL,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if md.TokenEndpoint != srv.URL+"/token" {
		t.Errorf("TokenEndpoint = %q", md.TokenEndpoint)
	}
}

// Regression: expired() returned false whenever the broker omitted
// expires_in, so the very first token was served for the lifetime of
// the process. RFC 0019 pins the cap as the freshness guarantee
// precisely because a broker can rotate a token without warning, and
// the early return also short-circuited an explicit MaxCacheDuration.
func TestLocalEndpointBroker_CapAppliesWhenExpiresInAbsent(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// No expires_in field, which is legal per RFC 6749 section 5.1.
		_, _ = fmt.Fprintf(w, `{"access_token":"token-%d","token_type":"Bearer"}`, n)
	}))
	defer srv.Close()

	src, err := oauth.NewLocalEndpointBrokerSource(oauth.LocalEndpointBrokerConfig{
		Client:           srv.Client(),
		Endpoint:         srv.URL,
		MaxCacheDuration: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewLocalEndpointBrokerSource: %v", err)
	}

	first, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("first Token: %v", err)
	}
	// Well past the 1ms ceiling; the cap must force a re-exchange even
	// though the broker never told us when the token expires.
	time.Sleep(20 * time.Millisecond)

	second, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if first == second {
		t.Errorf("token %q served past the cache ceiling; cap was bypassed", first)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("broker exchanged %d times, want 2", got)
	}
}

// Within the ceiling the token is still cached, so the fix does not
// turn every call into a broker round-trip.
func TestLocalEndpointBroker_CachesWithinCeiling(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"stable","token_type":"Bearer"}`)
	}))
	defer srv.Close()

	src, err := oauth.NewLocalEndpointBrokerSource(oauth.LocalEndpointBrokerConfig{
		Client:           srv.Client(),
		Endpoint:         srv.URL,
		MaxCacheDuration: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLocalEndpointBrokerSource: %v", err)
	}
	for i := range 3 {
		if _, err := src.Token(context.Background()); err != nil {
			t.Fatalf("Token %d: %v", i, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("broker exchanged %d times, want 1", got)
	}
}
