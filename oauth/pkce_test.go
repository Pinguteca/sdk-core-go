package oauth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Pinguteca/sdk-core-go/oauth"
)

// RFC 7636 Appendix B vector. Pinning this guarantees the
// challenge derivation interops with any conformant server.
func TestPkcePairFromVerifier_AppendixBVector(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

	p, err := oauth.PkcePairFromVerifier(verifier)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Challenge != want {
		t.Errorf("Challenge = %q, want %q", p.Challenge, want)
	}
	if p.Method() != "S256" {
		t.Errorf("Method = %q, want S256", p.Method())
	}
}

func TestGeneratePkcePair_LengthAndRoundTrip(t *testing.T) {
	p, err := oauth.GeneratePkcePair()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(p.Verifier); got != 43 {
		t.Errorf("verifier length = %d, want 43", got)
	}
	round, err := oauth.PkcePairFromVerifier(p.Verifier)
	if err != nil {
		t.Fatalf("unexpected error round-tripping verifier: %v", err)
	}
	if round.Challenge != p.Challenge {
		t.Errorf("recomputed challenge differs from generated")
	}
}

func TestPkcePairFromVerifier_RejectsOutOfRangeLengths(t *testing.T) {
	cases := []string{"", "tooshort", strings.Repeat("a", 129)}
	for _, v := range cases {
		_, err := oauth.PkcePairFromVerifier(v)
		if err == nil {
			t.Errorf("expected error for verifier of length %d", len(v))
			continue
		}
		var oe *oauth.OAuthError
		if !errors.As(err, &oe) || oe.Code != oauth.ErrorCodeInvalidVerifier {
			t.Errorf("expected ErrorCodeInvalidVerifier, got %v", err)
		}
	}
}
