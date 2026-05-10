package pkcs12_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	gopkcs12 "software.sslmate.com/src/go-pkcs12"

	"github.com/Pinguteca/sdk-core-go/transport/mtls"
	"github.com/Pinguteca/sdk-core-go/transport/mtls/pkcs12"
)

func TestConfig_HappyPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p12Path := writeTestP12(t, dir, "passw0rd")

	cfg, err := pkcs12.Config(p12Path, "passw0rd", "", mtls.Options{})
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if got := len(cfg.Certificates); got != 1 {
		t.Errorf("Certificates len = %d, want 1", got)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %#x, want TLS13", cfg.MinVersion)
	}
}

func TestConfig_WrongPassword(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p12Path := writeTestP12(t, dir, "passw0rd")

	_, err := pkcs12.Config(p12Path, "wrong", "", mtls.Options{})
	if err == nil {
		t.Fatal("Config with wrong password: expected error, got nil")
	}
}

func TestConfig_NotPKCS12(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	junkPath := filepath.Join(dir, "junk.p12")
	if err := os.WriteFile(junkPath, []byte("PEM-shaped junk -----BEGIN CERT-----"), 0o600); err != nil {
		t.Fatalf("write junk: %v", err)
	}

	_, err := pkcs12.Config(junkPath, "x", "", mtls.Options{})
	if !errors.Is(err, pkcs12.ErrInvalidPKCS12) {
		t.Errorf("err = %v, want ErrInvalidPKCS12", err)
	}
}

func TestConfig_TooLarge(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bigPath := filepath.Join(dir, "big.p12")
	big := make([]byte, mtls.MaxCertFileSize+1)
	big[0] = 0x30 // pretend SEQUENCE so we are testing size-cap, not magic.
	if err := os.WriteFile(bigPath, big, 0o600); err != nil {
		t.Fatalf("write big: %v", err)
	}

	_, err := pkcs12.Config(bigPath, "x", "", mtls.Options{})
	if !errors.Is(err, mtls.ErrCertFileTooLarge) {
		t.Errorf("err = %v, want ErrCertFileTooLarge", err)
	}
}

func TestConfig_EmptyPath(t *testing.T) {
	t.Parallel()
	_, err := pkcs12.Config("", "x", "", mtls.Options{})
	if !errors.Is(err, pkcs12.ErrEmptyP12Path) {
		t.Errorf("err = %v, want ErrEmptyP12Path", err)
	}
}

func TestConfig_RejectsInsecureSkipVerify(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p12Path := writeTestP12(t, dir, "passw0rd")

	_, err := pkcs12.Config(p12Path, "passw0rd", "", mtls.Options{InsecureSkipVerify: true})
	if !errors.Is(err, mtls.ErrInsecureSkipVerify) {
		t.Errorf("err = %v, want ErrInsecureSkipVerify", err)
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func writeTestP12(t *testing.T, dir, password string) string {
	t.Helper()
	key, cert := genTestCert(t)
	p12Bytes, err := gopkcs12.Modern.Encode(key, cert, nil, password)
	if err != nil {
		t.Fatalf("pkcs12 encode: %v", err)
	}
	p12Path := filepath.Join(dir, "client.p12")
	if err := os.WriteFile(p12Path, p12Bytes, 0o600); err != nil {
		t.Fatalf("write p12: %v", err)
	}
	return p12Path
}

func genTestCert(t *testing.T) (*ecdsa.PrivateKey, *x509.Certificate) {
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
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return key, cert
}
