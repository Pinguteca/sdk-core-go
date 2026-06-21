package oauth_test

import (
	"testing"

	"github.com/Pinguteca/sdk-core-go/oauth"
)

func TestParseTokenResponse_Minimal(t *testing.T) {
	body := []byte(`{"access_token":"abc","token_type":"Bearer"}`)
	r, err := oauth.ParseTokenResponse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.AccessToken != "abc" {
		t.Errorf("AccessToken = %q, want abc", r.AccessToken)
	}
	if r.TokenType != "Bearer" {
		t.Errorf("TokenType = %q, want Bearer", r.TokenType)
	}
}

func TestParseTokenResponse_AllFields(t *testing.T) {
	body := []byte(`{"access_token":"a","token_type":"Bearer","expires_in":3600,"refresh_token":"r","scope":"s1 s2","id_token":"id"}`)
	r, err := oauth.ParseTokenResponse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ExpiresIn != 3600 || r.RefreshToken != "r" || r.Scope != "s1 s2" || r.IDToken != "id" {
		t.Errorf("response fields not parsed correctly: %+v", r)
	}
}

func TestParseTokenResponse_MissingAccessToken(t *testing.T) {
	body := []byte(`{"token_type":"Bearer"}`)
	if _, err := oauth.ParseTokenResponse(body); err == nil {
		t.Error("expected error for missing access_token")
	}
}

func TestParseTokenResponse_MissingTokenType(t *testing.T) {
	body := []byte(`{"access_token":"abc"}`)
	if _, err := oauth.ParseTokenResponse(body); err == nil {
		t.Error("expected error for missing token_type")
	}
}

func TestParseTokenResponse_InvalidJSON(t *testing.T) {
	if _, err := oauth.ParseTokenResponse([]byte("not json")); err == nil {
		t.Error("expected error for invalid JSON")
	}
}
