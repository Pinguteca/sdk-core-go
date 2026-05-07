// Package auth provides Connect interceptors that attach credentials to outgoing
// RPCs. Two TokenSource flavours ship out of the box: [StaticBearer] for fixed
// tokens (CI tokens, dev fixtures) and [ClientCredentials] for OAuth2 service-
// to-service flows with automatic refresh and caching.
//
// Bring-your-own [TokenSource] for any other IdP (Keycloak, Auth0, Entra ID,
// custom) by satisfying the small interface; nothing here is vendor-locked.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// TokenSource yields a bearer token. Implementations are responsible for any
// caching or refresh behaviour; the interceptor calls [TokenSource.Token] once
// per RPC.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// TokenSourceFunc is an adapter so a plain function can satisfy [TokenSource].
type TokenSourceFunc func(ctx context.Context) (string, error)

// Token implements [TokenSource].
func (f TokenSourceFunc) Token(ctx context.Context) (string, error) { return f(ctx) }

// StaticBearer returns a [TokenSource] that always yields the same token. Useful
// for short-lived CI credentials or hand-issued service tokens.
func StaticBearer(token string) TokenSource {
	return TokenSourceFunc(func(context.Context) (string, error) {
		if token == "" {
			return "", errors.New("auth: static bearer token is empty")
		}
		return token, nil
	})
}

// ClientCredentialsConfig parameterises an OAuth2 client_credentials flow.
type ClientCredentialsConfig struct {
	// TokenURL is the IdP's token endpoint. Required.
	TokenURL string
	// ClientID and ClientSecret authenticate the caller against the IdP. Required.
	ClientID     string
	ClientSecret string
	// Scopes requested on token issuance. Optional.
	Scopes []string
	// EndpointParams adds custom form fields to the token request (e.g. audience). Optional.
	EndpointParams url.Values
	// AuthStyle controls whether credentials go in the Authorization header
	// (in-header) or the request body. Defaults to AuthStyleAutoDetect.
	AuthStyle oauth2.AuthStyle
}

// ClientCredentials returns a [TokenSource] backed by golang.org/x/oauth2 with
// thread-safe caching and automatic refresh on expiry.
func ClientCredentials(cfg ClientCredentialsConfig) (TokenSource, error) {
	if cfg.TokenURL == "" {
		return nil, errors.New("auth: TokenURL is required")
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("auth: ClientID and ClientSecret are required")
	}
	cc := &clientcredentials.Config{
		ClientID:       cfg.ClientID,
		ClientSecret:   cfg.ClientSecret,
		TokenURL:       cfg.TokenURL,
		Scopes:         cfg.Scopes,
		EndpointParams: cfg.EndpointParams,
		AuthStyle:      cfg.AuthStyle,
	}
	return &cachingOAuth2Source{cfg: cc}, nil
}

type cachingOAuth2Source struct {
	cfg *clientcredentials.Config

	mu  sync.Mutex
	src oauth2.TokenSource
}

func (s *cachingOAuth2Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	if s.src == nil {
		// oauth2.ReuseTokenSource caches and refreshes on expiry.
		s.src = oauth2.ReuseTokenSource(nil, s.cfg.TokenSource(ctx))
	}
	src := s.src
	s.mu.Unlock()

	tok, err := src.Token()
	if err != nil {
		return "", fmt.Errorf("auth: oauth2 token: %w", err)
	}
	return tok.AccessToken, nil
}

// Options configure [Interceptor].
type Options struct {
	// Source provides the token. Required.
	Source TokenSource
	// HeaderName overrides "Authorization". Useful for APIs that read a custom
	// header (e.g. "X-Api-Key").
	HeaderName string
	// FormatHeader formats the token into the header value. Defaults to
	// `"Bearer " + token`. Override to send a raw token (return token unchanged)
	// or a different scheme (e.g. `"Token " + token`).
	FormatHeader func(token string) string
	// Skip returns true for procedures that should NOT have the credential attached
	// (e.g. an unauthenticated /Login RPC). Optional.
	Skip func(procedure string) bool
}

// Interceptor returns a Connect interceptor that injects a bearer credential on
// every unary and streaming call. This is a CLIENT-SIDE interceptor and must be
// wired into client construction only (e.g.
// connect.WithInterceptors(authIc) inside NewMyServiceClient). Registering it
// on a handler is incorrect: it would attach the bearer header to inbound
// requests, which is meaningless on the server side.
func Interceptor(opts Options) (connect.Interceptor, error) {
	if opts.Source == nil {
		return nil, errors.New("auth: Source is required")
	}
	if opts.HeaderName == "" {
		opts.HeaderName = "Authorization"
	}
	if opts.FormatHeader == nil {
		opts.FormatHeader = func(token string) string { return "Bearer " + token }
	}
	return &authInterceptor{opts: opts}, nil
}

type authInterceptor struct{ opts Options }

func (a *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := a.attach(ctx, req.Spec().Procedure, req.Header()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (a *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		if err := a.attach(ctx, spec.Procedure, conn.RequestHeader()); err != nil {
			// Defer the failure to the first Send/Receive so the caller sees a normal error path.
			return &failedStreamingConn{StreamingClientConn: conn, err: err}
		}
		return conn
	}
}

func (a *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func (a *authInterceptor) attach(ctx context.Context, procedure string, hdr connectHeader) error {
	if a.opts.Skip != nil && a.opts.Skip(procedure) {
		return nil
	}
	tok, err := a.opts.Source.Token(ctx)
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("auth: token: %w", err))
	}
	hdr.Set(a.opts.HeaderName, a.opts.FormatHeader(tok))
	return nil
}

// connectHeader is the small surface of http.Header we need; satisfied by both
// AnyRequest.Header() and StreamingClientConn.RequestHeader().
type connectHeader interface{ Set(key, value string) }

// WaitToken returns a token now or until ctx is done. Useful for pre-flight
// checks (warming caches, surfacing config errors before the first RPC).
func WaitToken(ctx context.Context, src TokenSource, timeout time.Duration) (string, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return src.Token(ctx)
}

// failedStreamingConn defers an attach error to the first stream operation.
type failedStreamingConn struct {
	connect.StreamingClientConn
	err error
}

func (f *failedStreamingConn) Send(any) error    { return f.err }
func (f *failedStreamingConn) Receive(any) error { return f.err }
func (f *failedStreamingConn) CloseRequest() error {
	if f.StreamingClientConn != nil {
		_ = f.StreamingClientConn.CloseRequest()
	}
	return f.err
}
func (f *failedStreamingConn) CloseResponse() error {
	if f.StreamingClientConn != nil {
		_ = f.StreamingClientConn.CloseResponse()
	}
	return f.err
}
