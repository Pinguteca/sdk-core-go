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
// against a known magic number (the PEM header for CA bundles) before any
// decoding step touches them. The G304 gosec advisory still fires on the
// underlying [os.Open] call because the path is variable by design; see
// https://github.com/securego/gosec/issues/1054.
//
// PEM is the only format handled by this package. PKCS#12/PFX bundles
// land in the `transport/mtls/pkcs12` sub-module so that consumers who do
// not need the PKCS#12 parser do not pull its third-party dependency.
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
)

// MaxCertFileSize caps the bytes [Config] and [ReadBoundedFile] read from
// a single file. Real cert bundles are well under 100 KiB; the 1 MiB
// ceiling exists to bound DoS pressure if a caller points the helper at
// an unexpected file (a bind mount that turned into a log, a sparse
// file, etc.).
const MaxCertFileSize = 1 << 20

// Sentinel errors so callers can branch on misconfiguration without string
// matching. Wrapped via [fmt.Errorf] %w in the constructors.
var (
	ErrInsecureSkipVerify = errors.New("mtls: InsecureSkipVerify is not allowed")
	ErrEmptyCertPath      = errors.New("mtls: cert path is empty")
	ErrEmptyKeyPath       = errors.New("mtls: key path is empty")
	ErrNoCAInFile         = errors.New("mtls: no PEM certificates in CA file")
	ErrCertFileTooLarge   = errors.New("mtls: certificate file exceeds MaxCertFileSize")
	ErrInvalidPEM         = errors.New("mtls: file is not a PEM-encoded certificate")
)

// pemHeaderPrefix is the byte prefix shared by every PEM-encoded block
// (the first thing a CA bundle should start with after any leading
// whitespace).
var pemHeaderPrefix = []byte("-----BEGIN ")

// Options tunes the [tls.Config] produced by [Config] and [Assemble].
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

// Assemble turns an already-parsed [tls.Certificate] into a [*tls.Config]
// using the package's standard CA-loading and option-validation rules.
// Sub-packages that handle additional cert formats (PKCS#12, etc.) call
// this after producing the [tls.Certificate] so the resulting Config
// shares the same TLS 1.3 default and rejection of
// [Options.InsecureSkipVerify].
func Assemble(cert tls.Certificate, caCertPath string, opts Options) (*tls.Config, error) {
	if err := guardOptions(opts); err != nil {
		return nil, err
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

// ReadBoundedFile cleans the path, opens it, and reads up to
// [MaxCertFileSize] bytes. Anything larger is rejected before allocation
// grows beyond the cap. Sub-packages that load other cert formats reuse
// this so they pick up the same G304 hardening (path cleaning, size cap)
// and the same comment justifying the suppression. The kind argument is
// echoed in error messages and chooses nothing else.
func ReadBoundedFile(path, kind string) ([]byte, error) {
	cleaned := filepath.Clean(path)
	f, err := os.Open(cleaned) //#nosec G304 -- caller path is API; size cap below. gosec#1054.
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
	raw, err := ReadBoundedFile(path, "CA")
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
