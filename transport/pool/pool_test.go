package pool_test

import (
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/Pinguteca/sdk-core-go/transport/pool"
)

func TestDefaultConfig_MatchesCrossSdkBaseline(t *testing.T) {
	cfg := pool.DefaultConfig()

	if cfg.MaxIdleConns != 100 {
		t.Errorf("MaxIdleConns = %d, want 100", cfg.MaxIdleConns)
	}
	if cfg.MaxIdleConnsPerHost != 10 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 10", cfg.MaxIdleConnsPerHost)
	}
	if cfg.IdleConnTimeout != 2*time.Minute {
		t.Errorf("IdleConnTimeout = %v, want 2m", cfg.IdleConnTimeout)
	}
	if cfg.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v, want 10s", cfg.TLSHandshakeTimeout)
	}
	if !cfg.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 = false, want true")
	}
}

func TestNew_AppliesEveryField(t *testing.T) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	cfg := pool.Config{
		TLSConfig:             tlsCfg,
		MaxIdleConns:          50,
		MaxIdleConnsPerHost:   5,
		MaxConnsPerHost:       20,
		IdleConnTimeout:       45 * time.Second,
		ResponseHeaderTimeout: 7 * time.Second,
		TLSHandshakeTimeout:   3 * time.Second,
		ExpectContinueTimeout: 500 * time.Millisecond,
		ForceAttemptHTTP2:     true,
		DisableKeepAlives:     true,
		DisableCompression:    true,
	}

	tr, err := pool.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if tr.MaxIdleConns != 50 {
		t.Errorf("MaxIdleConns = %d", tr.MaxIdleConns)
	}
	if tr.MaxIdleConnsPerHost != 5 {
		t.Errorf("MaxIdleConnsPerHost = %d", tr.MaxIdleConnsPerHost)
	}
	if tr.MaxConnsPerHost != 20 {
		t.Errorf("MaxConnsPerHost = %d", tr.MaxConnsPerHost)
	}
	if tr.IdleConnTimeout != 45*time.Second {
		t.Errorf("IdleConnTimeout = %v", tr.IdleConnTimeout)
	}
	if tr.ResponseHeaderTimeout != 7*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v", tr.ResponseHeaderTimeout)
	}
	if tr.TLSHandshakeTimeout != 3*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v", tr.TLSHandshakeTimeout)
	}
	if tr.ExpectContinueTimeout != 500*time.Millisecond {
		t.Errorf("ExpectContinueTimeout = %v", tr.ExpectContinueTimeout)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 not propagated")
	}
	if !tr.DisableKeepAlives {
		t.Error("DisableKeepAlives not propagated")
	}
	if !tr.DisableCompression {
		t.Error("DisableCompression not propagated")
	}
	if tr.TLSClientConfig != tlsCfg {
		t.Error("TLSClientConfig not propagated")
	}
}

func TestNew_ClonesDefaultTransport(t *testing.T) {
	cfg := pool.DefaultConfig()
	tr1, _ := pool.New(cfg)
	tr2, _ := pool.New(cfg)
	if tr1 == tr2 {
		t.Error("New returned same *http.Transport across calls; must be a clone per build")
	}
}

func TestNew_RejectsNegativeValues(t *testing.T) {
	cases := []struct {
		mut  func(*pool.Config)
		name string
	}{
		{name: "MaxIdleConns", mut: func(c *pool.Config) { c.MaxIdleConns = -1 }},
		{name: "MaxIdleConnsPerHost", mut: func(c *pool.Config) { c.MaxIdleConnsPerHost = -1 }},
		{name: "MaxConnsPerHost", mut: func(c *pool.Config) { c.MaxConnsPerHost = -1 }},
		{name: "IdleConnTimeout", mut: func(c *pool.Config) { c.IdleConnTimeout = -1 }},
		{name: "ResponseHeaderTimeout", mut: func(c *pool.Config) { c.ResponseHeaderTimeout = -1 }},
		{name: "TLSHandshakeTimeout", mut: func(c *pool.Config) { c.TLSHandshakeTimeout = -1 }},
		{name: "ExpectContinueTimeout", mut: func(c *pool.Config) { c.ExpectContinueTimeout = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := pool.DefaultConfig()
			tc.mut(&cfg)
			_, err := pool.New(cfg)
			if !errors.Is(err, pool.ErrInvalidConfig) {
				t.Fatalf("expected ErrInvalidConfig for %s, got %v", tc.name, err)
			}
		})
	}
}

func TestNewClient_WiresTimeout(t *testing.T) {
	client, err := pool.NewClient(pool.DefaultConfig(), 9*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.Timeout != 9*time.Second {
		t.Errorf("Timeout = %v, want 9s", client.Timeout)
	}
	if client.Transport == nil {
		t.Error("Transport is nil")
	}
}

func TestNewClient_SurfacesConfigError(t *testing.T) {
	cfg := pool.DefaultConfig()
	cfg.MaxIdleConns = -1
	_, err := pool.NewClient(cfg, 0)
	if !errors.Is(err, pool.ErrInvalidConfig) {
		t.Fatalf("expected wrapped ErrInvalidConfig, got %v", err)
	}
}
