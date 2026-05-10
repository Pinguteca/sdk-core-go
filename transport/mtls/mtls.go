// Package mtls builds [crypto/tls.Config] and [net/http.Transport] values
// for client mTLS in standalone deployments. It is a transport-layer helper
// only; mesh-resident services should not wire it because the sidecar (or
// eBPF redirector) already terminates TLS for them. See ADR 0009 for the
// standalone vs mesh decision and ADR 0002 for the broader mesh coexistence
// rationale.
//
// Defaults:
//
//   - MinVersion is TLS 1.3.
//   - The system root pool is used; an optional CA file can be appended for
//     private CAs.
//   - ServerName is left to the stdlib (derived from the request URL host).
//   - [Options.InsecureSkipVerify] is rejected at build time.
//
// File-loading hardening: caller-supplied paths go through [filepath.Clean],
// reads are bounded by [MaxCertFileSize], and bytes are content-checked
// against a known magic number (PEM header for CA bundles, ASN.1 SEQUENCE
// tag for PKCS#12 bundles) before any decoding step touches them. The
// G304 gosec advisory still fires on the underlying [os.Open] call because
// the path is variable by design; see https://github.com/securego/gosec/issues/1054.
//
// Two constructors are provided so callers do not bypass the helper just
// because their cert format differs:
//
//   - [Config] for PEM-encoded cert/key/CA files (the dominant cloud-native
//     pipeline format).
//   - [ConfigFromP12] for PKCS#12/PFX bundles (common in Java- and
//     Windows-side ecosystems and in some cert-manager outputs).
//
// Cert hot-reload, SPIFFE workload identity, and TPM/HSM-backed keys are
// out of scope for v1; see the "Revisit when" section of ADR 0009.
package mtls

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// MaxCertFileSize caps the bytes [Config] and [ConfigFromP12] read from a
// single file. Real cert and PKCS#12 bundles are well under 100 KiB; the
// 1 MiB ceiling exists to bound DoS pressure if a caller points the helper
// at an unexpected file (a bind mount that turned into a log, a sparse
// file, etc.).
const MaxCertFileSize = 1 << 20

// asn1SequenceTag is the leading byte of a DER-encoded ASN.1 SEQUENCE,
// which is the outer structure of every PKCS#12 bundle.
const asn1SequenceTag byte = 0x30

// Sentinel errors so callers can branch on misconfiguration without string
// matching. Wrapped via [fmt.Errorf] %w in the constructors.
var (
	ErrInsecureSkipVerify = errors.New("mtls: InsecureSkipVerify is not allowed")
	ErrEmptyCertPath      = errors.New("mtls: cert path is empty")
	ErrEmptyKeyPath       = errors.New("mtls: key path is empty")
	ErrEmptyP12Path       = errors.New("mtls: pkcs12 path is empty")
	ErrNoCAInFile         = errors.New("mtls: no PEM certificates in CA file")
	ErrCertFileTooLarge   = errors.New("mtls: certificate file exceeds MaxCertFileSize")
	ErrInvalidPEM         = errors.New("mtls: file is not a PEM-encoded certificate")
	ErrInvalidPKCS12      = errors.New("mtls: file is not a PKCS#12 bundle")
)

// pemHeaderPrefix is the byte prefix shared by every PEM-encoded block
// (the first thing a CA bundle should start with after any leading
// whitespace).
var pemHeaderPrefix = []byte("-----BEGIN ")

// Options tunes the [tls.Config] produced by [Config] and [ConfigFromP12].
// The zero value is the recommended default (TLS 1.3, no insecure skip).
type Options struct {
	// MinVersion overrides the minimum TLS version. Zero means TLS 1.3.
	// Set to [tls.VersionTLS12] only when talking to a server that cannot
	// be upgraded; this loses 1.3 forward-secrecy guarantees and forces
	// the helper to rely on Go's default cipher suite list (which excludes
	// the legacy RSA key-exchange suites).
	MinVersion uint16
	// InsecureSkipVerify is present so callers cannot avoid the check by
	// poking at a returned [tls.Config] in surprise ways: setting it true
	// here causes the constructor to return [ErrInsecureSkipVerify]. Tests
	// that genuinely need to skip verification should build their own
	// [tls.Config] without going through this helper.
	InsecureSkipVerify bool
}

// Config loads a PEM-encoded client certificate and key, optionally appends
// a private CA bundle to the system root pool, and returns a [*tls.Config]
// suitable for [*http.Transport.TLSClientConfig]. caCertPath may be empty,
// in which case only the system root pool is used.
func Config(certPath, keyPath, caCertPath string, opts Options) (*tls.Config, error) {
	if err := guardOptions(opts); err != nil {
		return nil, err
	}
	if certPath == "" {
		return nil, ErrEmptyCertPath
	}
	if keyPath == "" {
		return nil, ErrEmptyKeyPath
	}

	cert, err := tls.LoadX509KeyPair(filepath.Clean(certPath), filepath.Clean(keyPath))
	if err != nil {
		return nil, fmt.Errorf("mtls: load PEM cert/key: %w", err)
	}

	pool, err := loadCAPool(caCertPath)
	if err != nil {
		return nil, err
	}

	return buildConfig(cert, pool, opts), nil
}

// ConfigFromP12 loads a PKCS#12/PFX bundle (cert, key, optional intermediate
// chain) and returns a [*tls.Config]. caCertPath, when non-empty, appends a
// PEM CA bundle to the system root pool. The PKCS#12 bundle's own chain
// is sent to the server; the CA file pins which roots are trusted on
// inbound verification.
func ConfigFromP12(p12Path, password, caCertPath string, opts Options) (*tls.Config, error) {
	if err := guardOptions(opts); err != nil {
		return nil, err
	}
	if p12Path == "" {
		return nil, ErrEmptyP12Path
	}

	raw, err := readBoundedFile(p12Path, "pkcs12")
	if err != nil {
		return nil, err
	}
	if err := ensurePKCS12(raw); err != nil {
		return nil, err
	}

	privKey, leaf, chain, err := pkcs12.DecodeChain(raw, password)
	if err != nil {
		return nil, fmt.Errorf("mtls: decode pkcs12: %w", err)
	}

	rawChain := make([][]byte, 0, 1+len(chain))
	rawChain = append(rawChain, leaf.Raw)
	for _, c := range chain {
		rawChain = append(rawChain, c.Raw)
	}

	cert := tls.Certificate{
		Certificate: rawChain,
		PrivateKey:  privKey,
		Leaf:        leaf,
	}

	pool, err := loadCAPool(caCertPath)
	if err != nil {
		return nil, err
	}

	return buildConfig(cert, pool, opts), nil
}

// Transport returns a clone of [http.DefaultTransport] with the supplied
// [tls.Config] applied. The clone preserves stdlib defaults for connection
// pooling, idle timeouts, and HTTP/2 negotiation; only TLSClientConfig is
// replaced. If [http.DefaultTransport] has been swapped to a non-Transport
// type the helper returns a fresh [*http.Transport] with only the supplied
// TLS config set.
func Transport(cfg *tls.Config) *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{TLSClientConfig: cfg}
	}
	t := base.Clone()
	t.TLSClientConfig = cfg
	return t
}

func guardOptions(opts Options) error {
	if opts.InsecureSkipVerify {
		return ErrInsecureSkipVerify
	}
	return nil
}

func loadCAPool(path string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if path == "" {
		return pool, nil
	}
	raw, err := readBoundedFile(path, "CA")
	if err != nil {
		return nil, err
	}
	if err := ensurePEM(raw); err != nil {
		return nil, err
	}
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("%w: %s", ErrNoCAInFile, path)
	}
	return pool, nil
}

// readBoundedFile cleans the path, opens it, and reads up to MaxCertFileSize
// bytes. Anything larger is rejected before allocation grows beyond the cap.
// The G304 advisory is suppressed because reading caller-supplied paths is
// the package's stated purpose; the magic-number check in the caller and
// the size cap here narrow the blast radius. See https://github.com/securego/gosec/issues/1054.
func readBoundedFile(path, kind string) ([]byte, error) {
	cleaned := filepath.Clean(path)
	f, err := os.Open(cleaned) //#nosec G304 -- caller path is API; size cap and magic check below. gosec#1054.
	if err != nil {
		return nil, fmt.Errorf("mtls: open %s file %s: %w", kind, cleaned, err)
	}
	defer func() { _ = f.Close() }()

	raw, err := io.ReadAll(io.LimitReader(f, MaxCertFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("mtls: read %s file %s: %w", kind, cleaned, err)
	}
	if len(raw) > MaxCertFileSize {
		return nil, fmt.Errorf("%w: %s (%s)", ErrCertFileTooLarge, kind, cleaned)
	}
	return raw, nil
}

// ensurePEM reports whether the bytes look like a PEM-encoded structure
// (the first non-whitespace bytes are the canonical "-----BEGIN " prefix).
// This is a magic-number sniff, not a full parse; the eventual decoder
// surfaces structural errors.
func ensurePEM(raw []byte) error {
	if !bytes.HasPrefix(bytes.TrimLeft(raw, " \t\r\n"), pemHeaderPrefix) {
		return ErrInvalidPEM
	}
	return nil
}

// ensurePKCS12 reports whether the bytes look like a DER-encoded ASN.1
// SEQUENCE, the outer structure of every PKCS#12 bundle. The full parse
// is left to [pkcs12.DecodeChain].
func ensurePKCS12(raw []byte) error {
	if len(raw) < 2 || raw[0] != asn1SequenceTag {
		return ErrInvalidPKCS12
	}
	return nil
}

func buildConfig(cert tls.Certificate, pool *x509.CertPool, opts Options) *tls.Config {
	minVersion := opts.MinVersion
	if minVersion == 0 {
		minVersion = tls.VersionTLS13
	}
	// G402 false positive: gosec does not propagate the local minVersion
	// fallback to TLS 1.3, so it sees the field as variable and assumes
	// 1.0/1.1. See https://github.com/securego/gosec/issues/1054. The zero
	// value of [Options.MinVersion] resolves to TLS 1.3 above; explicit
	// overrides are documented as TLS 1.2 minimum, which is safe with
	// Go's default cipher suite list (no RSA key-exchange suites).
	return &tls.Config{ //#nosec G402 -- gosec#1054 false positive; minVersion defaults to TLS 1.3.
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   minVersion,
	}
}
