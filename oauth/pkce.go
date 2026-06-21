package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// PkcePair is an RFC 7636 PKCE verifier and challenge pair. S256
// only. The plain method is forbidden per RFC 0017.
type PkcePair struct {
	Verifier  string
	Challenge string
}

// Method returns the code_challenge_method to send to the
// authorization endpoint. Always "S256".
func (PkcePair) Method() string { return "S256" }

// GeneratePkcePair returns a fresh PKCE pair drawn from crypto/rand.
// The verifier is a 43-character base64url-no-pad string sampled
// from 32 random bytes, satisfying RFC 7636 §4.1.
func GeneratePkcePair() (PkcePair, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return PkcePair{}, fmt.Errorf("oauth: read random bytes for PKCE verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf[:])
	pair, err := PkcePairFromVerifier(verifier)
	if err != nil {
		return PkcePair{}, fmt.Errorf("oauth: compute PKCE pair from generated verifier: %w", err)
	}
	return pair, nil
}

// PkcePairFromVerifier recomputes the challenge from a caller
// supplied verifier. Useful when the verifier was persisted across
// the redirect step and the original pair has been discarded.
//
// Returns an [OAuthError] with code [ErrorCodeInvalidVerifier] when
// the verifier length falls outside the RFC 7636 §4.1 range.
func PkcePairFromVerifier(verifier string) (PkcePair, error) {
	if n := len(verifier); n < 43 || n > 128 {
		return PkcePair{}, &OAuthError{
			Code:        ErrorCodeInvalidVerifier,
			Description: "PKCE verifier must be 43-128 characters (RFC 7636 §4.1)",
		}
	}
	sum := sha256.Sum256([]byte(verifier))
	return PkcePair{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}, nil
}
