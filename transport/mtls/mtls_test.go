package mtls_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Pinguteca/sdk-core-go/transport/mtls"
)

func TestConfig_PEMHappyPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	certPath, keyPath, _ := writeTestPEM(t, dir)

	cfg, err := mtls.Config(certPath, keyPath, "", mtls.Options{})
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %#x, want TLS13 (%#x)", cfg.MinVersion, tls.VersionTLS13)
	}
	if got := len(cfg.Certificates); got != 1 {
		t.Errorf("Certificates len = %d, want 1", got)
	}
	if cfg.RootCAs == nil {
		t.Error("RootCAs is nil; expected at least the system pool fallback")
	}
}

func TestConfig_AppendsCAFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	certPath, keyPath, certPEM := writeTestPEM(t, dir)
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}

	cfg, err := mtls.Config(certPath, keyPath, caPath, mtls.Options{})
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if cfg.RootCAs == nil {
		t.Fatal("RootCAs is nil; expected pool with appended CA")
	}
}

func TestConfig_RejectsInsecureSkipVerify(t *testing.T) {
	t.Parallel()
	_, err := mtls.Config("ignored", "ignored", "", mtls.Options{InsecureSkipVerify: true})
	if !errors.Is(err, mtls.ErrInsecureSkipVerify) {
		t.Errorf("err = %v, want ErrInsecureSkipVerify", err)
	}
}

func TestConfig_EmptyCertPath(t *testing.T) {
	t.Parallel()
	_, err := mtls.Config("", "k", "", mtls.Options{})
	if !errors.Is(err, mtls.ErrEmptyCertPath) {
		t.Errorf("err = %v, want ErrEmptyCertPath", err)
	}
}

func TestConfig_EmptyKeyPath(t *testing.T) {
	t.Parallel()
	_, err := mtls.Config("c", "", "", mtls.Options{})
	if !errors.Is(err, mtls.ErrEmptyKeyPath) {
		t.Errorf("err = %v, want ErrEmptyKeyPath", err)
	}
}

func TestConfig_BadCAFileNoPEM(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	certPath, keyPath, _ := writeTestPEM(t, dir)
	junkPath := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junkPath, []byte("not a pem"), 0o600); err != nil {
		t.Fatalf("write junk: %v", err)
	}

	_, err := mtls.Config(certPath, keyPath, junkPath, mtls.Options{})
	if !errors.Is(err, mtls.ErrInvalidPEM) {
		t.Errorf("err = %v, want ErrInvalidPEM", err)
	}
}

func TestConfig_CAFileTooLarge(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	certPath, keyPath, _ := writeTestPEM(t, dir)
	bigPath := filepath.Join(dir, "big.pem")
	big := make([]byte, mtls.MaxCertFileSize+1)
	for i := range big {
		big[i] = 'a'
	}
	if err := os.WriteFile(bigPath, big, 0o600); err != nil {
		t.Fatalf("write big: %v", err)
	}

	_, err := mtls.Config(certPath, keyPath, bigPath, mtls.Options{})
	if !errors.Is(err, mtls.ErrCertFileTooLarge) {
		t.Errorf("err = %v, want ErrCertFileTooLarge", err)
	}
}

func TestConfig_CustomMinVersion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	certPath, keyPath, _ := writeTestPEM(t, dir)

	cfg, err := mtls.Config(certPath, keyPath, "", mtls.Options{MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %#x, want TLS12 (%#x)", cfg.MinVersion, tls.VersionTLS12)
	}
}

func TestTransport_AppliesTLSConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	certPath, keyPath, _ := writeTestPEM(t, dir)

	cfg, err := mtls.Config(certPath, keyPath, "", mtls.Options{})
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	tr := mtls.Transport(cfg)
	if tr == nil {
		t.Fatal("Transport returned nil")
	}
	if tr.TLSClientConfig != cfg {
		t.Error("TLSClientConfig was not applied to the returned Transport")
	}
	if tr.IdleConnTimeout == 0 {
		t.Error("expected stdlib defaults to be preserved (IdleConnTimeout was zero)")
	}
	// Round-trip a request to nowhere just to confirm the value is a usable
	// http.RoundTripper, without making a real network call.
	_ = (http.RoundTripper)(tr)
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func writeTestPEM(t *testing.T, dir string) (certPath, keyPath string, certPEM []byte) {
	t.Helper()
	certPEM, keyPEM := genTestPEM(t)
	certPath = filepath.Join(dir, "client.crt")
	keyPath = filepath.Join(dir, "client.key")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath, certPEM
}

func genTestPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}
