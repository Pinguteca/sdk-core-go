package oauth

import (
	"encoding/json"
	"errors"
	"fmt"
)

// TokenResponse is the parsed body of a successful token endpoint
// response per RFC 6749 §5.1, plus the OIDC id_token field.
//
// The shape is exposed to callers so flows that need the id_token
// (login, OIDC userinfo) can read it directly. The
// [auth.RotatingTokenSource] surface yields only the access token.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
}

// ParseTokenResponse decodes a token endpoint JSON body. Returns an
// error when access_token or token_type is missing, as both are
// required by RFC 6749 §5.1.
func ParseTokenResponse(body []byte) (*TokenResponse, error) {
	var resp TokenResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("oauth: decode token response: %w", err)
	}
	if resp.AccessToken == "" {
		return nil, errors.New("oauth: token response missing access_token")
	}
	if resp.TokenType == "" {
		return nil, errors.New("oauth: token response missing token_type")
	}
	return &resp, nil
}
