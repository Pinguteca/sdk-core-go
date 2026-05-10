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

	pkcs12 "software.sslmate.com/src/go-pkcs12"

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

func TestConfigFromP12_HappyPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p12Path := writeTestP12(t, dir, "passw0rd")

	cfg, err := mtls.ConfigFromP12(p12Path, "passw0rd", "", mtls.Options{})
	if err != nil {
		t.Fatalf("ConfigFromP12: %v", err)
	}
	if got := len(cfg.Certificates); got != 1 {
		t.Errorf("Certificates len = %d, want 1", got)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %#x, want TLS13", cfg.MinVersion)
	}
}

func TestConfigFromP12_WrongPassword(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p12Path := writeTestP12(t, dir, "passw0rd")

	_, err := mtls.ConfigFromP12(p12Path, "wrong", "", mtls.Options{})
	if err == nil {
		t.Fatal("ConfigFromP12 with wrong password: expected error, got nil")
	}
}

func TestConfigFromP12_NotPKCS12(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	junkPath := filepath.Join(dir, "junk.p12")
	if err := os.WriteFile(junkPath, []byte("PEM-shaped junk -----BEGIN CERT-----"), 0o600); err != nil {
		t.Fatalf("write junk: %v", err)
	}

	_, err := mtls.ConfigFromP12(junkPath, "x", "", mtls.Options{})
	if !errors.Is(err, mtls.ErrInvalidPKCS12) {
		t.Errorf("err = %v, want ErrInvalidPKCS12", err)
	}
}

func TestConfigFromP12_TooLarge(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bigPath := filepath.Join(dir, "big.p12")
	big := make([]byte, mtls.MaxCertFileSize+1)
	big[0] = 0x30 // pretend SEQUENCE so we are testing size-cap, not magic.
	if err := os.WriteFile(bigPath, big, 0o600); err != nil {
		t.Fatalf("write big: %v", err)
	}

	_, err := mtls.ConfigFromP12(bigPath, "x", "", mtls.Options{})
	if !errors.Is(err, mtls.ErrCertFileTooLarge) {
		t.Errorf("err = %v, want ErrCertFileTooLarge", err)
	}
}

func TestConfigFromP12_EmptyPath(t *testing.T) {
	t.Parallel()
	_, err := mtls.ConfigFromP12("", "x", "", mtls.Options{})
	if !errors.Is(err, mtls.ErrEmptyP12Path) {
		t.Errorf("err = %v, want ErrEmptyP12Path", err)
	}
}

func TestConfigFromP12_RejectsInsecureSkipVerify(t *testing.T) {
	t.Parallel()
	_, err := mtls.ConfigFromP12("ignored", "x", "", mtls.Options{InsecureSkipVerify: true})
	if !errors.Is(err, mtls.ErrInsecureSkipVerify) {
		t.Errorf("err = %v, want ErrInsecureSkipVerify", err)
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
	certPEM, keyPEM, _, _ := genTestCert(t, "client")
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

func writeTestP12(t *testing.T, dir, password string) string {
	t.Helper()
	_, _, key, cert := genTestCert(t, "client")
	p12Bytes, err := pkcs12.Modern.Encode(key, cert, nil, password)
	if err != nil {
		t.Fatalf("pkcs12 encode: %v", err)
	}
	p12Path := filepath.Join(dir, "client.p12")
	if err := os.WriteFile(p12Path, p12Bytes, 0o600); err != nil {
		t.Fatalf("write p12: %v", err)
	}
	return p12Path
}

func genTestCert(t *testing.T, cn string) (certPEM, keyPEM []byte, key *ecdsa.PrivateKey, cert *x509.Certificate) {
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
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, key, cert
}
