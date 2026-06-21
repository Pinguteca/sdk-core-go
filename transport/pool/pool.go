// Package pool exposes a typed, validated connection pool builder
// on top of [net/http.Transport]. Consumers configure the pool
// once and pass the resulting [*http.Transport] to [*http.Client]
// or to higher-level helpers (mtls, caching). Defaults are pinned
// by sdk-scaffold RFC 0020.
package pool

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Config parameterises a pooled transport. Every field has a
// non-zero default; pass [DefaultConfig] and override the knobs
// the consumer cares about. Field semantics are pinned by RFC 0020.
//
// The field order satisfies govet's fieldalignment check (pointers
// then durations then ints then bools).
type Config struct {
	// TLSConfig overrides the TLS settings on the resulting
	// transport. Nil leaves Go's secure defaults. The mtls
	// package returns a [*tls.Config] to plug in here when client
	// certificates are required.
	TLSConfig *tls.Config

	// IdleConnTimeout is how long an idle keep-alive connection
	// stays in the pool before being closed.
	IdleConnTimeout time.Duration

	// ResponseHeaderTimeout caps how long the transport waits
	// for response headers after writing the request. Zero
	// means no timeout (use ctx deadlines instead).
	ResponseHeaderTimeout time.Duration

	// TLSHandshakeTimeout caps the TLS handshake.
	TLSHandshakeTimeout time.Duration

	// ExpectContinueTimeout caps the wait for a 100-Continue
	// response when Expect: 100-continue is set.
	ExpectContinueTimeout time.Duration

	// MaxIdleConns is the maximum number of idle keep-alive
	// connections kept across all hosts. Zero means no limit.
	MaxIdleConns int

	// MaxIdleConnsPerHost is the maximum number of idle keep-alive
	// connections kept per host.
	MaxIdleConnsPerHost int

	// MaxConnsPerHost is the maximum number of concurrent
	// connections (idle + active) per host. Zero means no limit.
	MaxConnsPerHost int

	// ForceAttemptHTTP2 enables HTTP/2 when the server supports
	// it via ALPN. Defaults to true.
	ForceAttemptHTTP2 bool

	// DisableKeepAlives turns off connection reuse entirely.
	// Off by default; only useful for short-lived load tests
	// or strict-binding deployments.
	DisableKeepAlives bool

	// DisableCompression turns off automatic gzip handling on
	// the transport layer. Off by default. Consumers that
	// install a compression interceptor should set this true to
	// avoid double-handling.
	DisableCompression bool
}

// Default pool sizing and durations, hoisted out of [DefaultConfig]
// so they read as a policy table rather than free numerics.
//
// Values are pinned by RFC 0020. Go stdlib's per-host idle cap is
// raised because the stdlib default starves typical RPC workloads.
const (
	defaultMaxIdleConns          = 100
	defaultMaxIdleConnsPerHost   = 10
	defaultIdleConnTimeout       = 2 * time.Minute
	defaultTLSHandshakeTimeout   = 10 * time.Second
	defaultExpectContinueTimeout = 1 * time.Second
)

// DefaultConfig returns the SDK's pool baseline.
func DefaultConfig() Config {
	return Config{
		MaxIdleConns:          defaultMaxIdleConns,
		MaxIdleConnsPerHost:   defaultMaxIdleConnsPerHost,
		IdleConnTimeout:       defaultIdleConnTimeout,
		TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
		ExpectContinueTimeout: defaultExpectContinueTimeout,
		ForceAttemptHTTP2:     true,
	}
}

// New builds a [*http.Transport] from the supplied [Config].
// Validates the config; returns an error when any duration is
// negative or any cap is negative.
func New(cfg Config) (*http.Transport, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	t := base.Clone()
	t.MaxIdleConns = cfg.MaxIdleConns
	t.MaxIdleConnsPerHost = cfg.MaxIdleConnsPerHost
	t.MaxConnsPerHost = cfg.MaxConnsPerHost
	t.IdleConnTimeout = cfg.IdleConnTimeout
	t.ResponseHeaderTimeout = cfg.ResponseHeaderTimeout
	t.TLSHandshakeTimeout = cfg.TLSHandshakeTimeout
	t.ExpectContinueTimeout = cfg.ExpectContinueTimeout
	t.ForceAttemptHTTP2 = cfg.ForceAttemptHTTP2
	t.DisableKeepAlives = cfg.DisableKeepAlives
	t.DisableCompression = cfg.DisableCompression
	if cfg.TLSConfig != nil {
		t.TLSClientConfig = cfg.TLSConfig
	}
	return t, nil
}

// NewClient is a one-shot helper that wraps [New] in a
// [*http.Client]. Equivalent to:
//
//	tr, err := pool.New(cfg)
//	if err != nil { return nil, err }
//	return &http.Client{Transport: tr, Timeout: timeout}, nil
//
// The timeout argument is the [http.Client.Timeout] (total
// request budget). Zero leaves it unset; rely on context
// deadlines instead.
func NewClient(cfg Config, timeout time.Duration) (*http.Client, error) {
	tr, err := New(cfg)
	if err != nil {
		return nil, fmt.Errorf("pool: build client transport: %w", err)
	}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}

// ErrInvalidConfig is returned by [New] when [Config] holds a
// negative cap or duration. Errors wrap this sentinel so callers
// can branch with errors.Is.
var ErrInvalidConfig = errors.New("pool: invalid config")

func validate(cfg Config) error {
	if cfg.MaxIdleConns < 0 {
		return fmt.Errorf("%w: MaxIdleConns must be >= 0", ErrInvalidConfig)
	}
	if cfg.MaxIdleConnsPerHost < 0 {
		return fmt.Errorf("%w: MaxIdleConnsPerHost must be >= 0", ErrInvalidConfig)
	}
	if cfg.MaxConnsPerHost < 0 {
		return fmt.Errorf("%w: MaxConnsPerHost must be >= 0", ErrInvalidConfig)
	}
	if cfg.IdleConnTimeout < 0 {
		return fmt.Errorf("%w: IdleConnTimeout must be >= 0", ErrInvalidConfig)
	}
	if cfg.ResponseHeaderTimeout < 0 {
		return fmt.Errorf("%w: ResponseHeaderTimeout must be >= 0", ErrInvalidConfig)
	}
	if cfg.TLSHandshakeTimeout < 0 {
		return fmt.Errorf("%w: TLSHandshakeTimeout must be >= 0", ErrInvalidConfig)
	}
	if cfg.ExpectContinueTimeout < 0 {
		return fmt.Errorf("%w: ExpectContinueTimeout must be >= 0", ErrInvalidConfig)
	}
	return nil
}
