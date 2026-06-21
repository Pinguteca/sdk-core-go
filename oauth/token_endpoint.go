package oauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// schemeHTTPS is the only scheme the SDK accepts for IdP-facing
// URLs (token, authorization, discovery, broker endpoints). RFC
// 0017 pins HTTPS-only at every function boundary.
const schemeHTTPS = "https"

// tokenEndpointBodyLimit caps how much of the token endpoint
// response the SDK ingests so a misbehaving server cannot pin
// large bodies in memory through this code path.
const tokenEndpointBodyLimit = 1 << 20 // 1 MiB

// postTokenRequest is the shared token endpoint POST used by every
// grant in this package (authorization_code, client_credentials,
// refresh). It applies the requested [ClientAuthMode] to the form
// or header, executes the HTTP request, and returns either a
// parsed [*TokenResponse] on success or a typed [*OAuthError] on
// any failure mode the server or transport produces.
func postTokenRequest(
	ctx context.Context,
	client *http.Client,
	tokenEndpoint string,
	form url.Values,
	authMode ClientAuthMode,
	clientID, clientSecret string,
) (*TokenResponse, error) {
	applyClientAuth(form, authMode, clientID, clientSecret)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		tokenEndpoint,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return nil, fmt.Errorf("oauth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if authMode == ClientAuthBasic {
		req.SetBasicAuth(clientID, clientSecret)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, FromTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, tokenEndpointBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("oauth: read token response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, FromTokenEndpointError(resp.StatusCode, body)
	}
	return ParseTokenResponse(body)
}

// applyClientAuth writes the client credentials onto the form
// according to [ClientAuthMode]. Basic-mode credentials go in the
// Authorization header inside [postTokenRequest]; mTLS uses the TLS
// layer (cert presented by the [http.Client]); None sends nothing
// beyond client_id.
func applyClientAuth(form url.Values, mode ClientAuthMode, clientID, clientSecret string) {
	form.Set("client_id", clientID)
	if mode == ClientAuthFormPost {
		form.Set("client_secret", clientSecret)
	}
}

// validateClientAuthMode rejects malformed or absent AuthMode
// values, and demands a client_secret when the mode requires one.
// Shared by every grant config validator in the package.
func validateClientAuthMode(mode ClientAuthMode, clientSecret string) error {
	switch mode {
	case ClientAuthBasic, ClientAuthFormPost:
		if clientSecret == "" {
			return &OAuthError{
				Code:        ErrorCodeInvalidRequest,
				Description: fmt.Sprintf("client_secret is required for AuthMode %q", mode),
			}
		}
	case ClientAuthNone, ClientAuthMtls:
		// No client_secret needed in either mode.
	case "":
		return &OAuthError{Code: ErrorCodeInvalidRequest, Description: "AuthMode is required"}
	default:
		return &OAuthError{
			Code:        ErrorCodeInvalidRequest,
			Description: fmt.Sprintf("unknown AuthMode %q", mode),
		}
	}
	return nil
}
