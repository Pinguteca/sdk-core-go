package oauth_test

import (
	"errors"
	"testing"

	"github.com/Pinguteca/sdk-core-go/oauth"
)

func TestFromTokenEndpointError_StandardCode(t *testing.T) {
	body := []byte(`{"error":"invalid_grant","error_description":"bad code","error_uri":"https://example.com/e"}`)
	e := oauth.FromTokenEndpointError(400, body)
	if e.Code != oauth.ErrorCodeInvalidGrant {
		t.Errorf("Code = %q, want %q", e.Code, oauth.ErrorCodeInvalidGrant)
	}
	if e.Description != "bad code" {
		t.Errorf("Description = %q, want %q", e.Description, "bad code")
	}
	if e.URI != "https://example.com/e" {
		t.Errorf("URI = %q", e.URI)
	}
	if e.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400", e.HTTPStatus)
	}
}

func TestFromTokenEndpointError_OpaqueBody(t *testing.T) {
	body := []byte("<html>internal error</html>")
	e := oauth.FromTokenEndpointError(500, body)
	if e.Code != oauth.ErrorCodeInvalidRequest {
		t.Errorf("Code = %q, want fallback %q", e.Code, oauth.ErrorCodeInvalidRequest)
	}
	if e.Description != string(body) {
		t.Errorf("Description = %q, want raw body preserved", e.Description)
	}
	if e.HTTPStatus != 500 {
		t.Errorf("HTTPStatus = %d, want 500", e.HTTPStatus)
	}
}

func TestFromTokenEndpointError_EmptyErrorField(t *testing.T) {
	body := []byte(`{"error_description":"missing error field"}`)
	e := oauth.FromTokenEndpointError(400, body)
	if e.Code != oauth.ErrorCodeInvalidRequest {
		t.Errorf("Code = %q, want fallback %q", e.Code, oauth.ErrorCodeInvalidRequest)
	}
}

func TestFromTransportError_WrapsAndUnwraps(t *testing.T) {
	inner := errors.New("dial tcp: connection refused")
	e := oauth.FromTransportError(inner)
	if e.Code != oauth.ErrorCodeTransport {
		t.Errorf("Code = %q, want %q", e.Code, oauth.ErrorCodeTransport)
	}
	if !errors.Is(e, inner) {
		t.Error("errors.Is should match the wrapped transport error")
	}
}

func TestOAuthError_IsMatchesOnCode(t *testing.T) {
	a := &oauth.OAuthError{Code: oauth.ErrorCodeInvalidGrant}
	b := &oauth.OAuthError{Code: oauth.ErrorCodeInvalidGrant, Description: "different"}
	c := &oauth.OAuthError{Code: oauth.ErrorCodeInvalidScope}
	if !errors.Is(a, b) {
		t.Error("same-code OAuthErrors should match via errors.Is")
	}
	if errors.Is(a, c) {
		t.Error("different-code OAuthErrors should not match")
	}
}
