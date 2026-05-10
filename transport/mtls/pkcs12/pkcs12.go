// Package pkcs12 builds a [*tls.Config] from a PKCS#12/PFX bundle. It is
// a Layer 3 companion to `transport/mtls`: consumers who only need PEM
// stay on the parent package and skip this module's
// `software.sslmate.com/src/go-pkcs12` dependency. PKCS#12 is included
// because it is common in Java- and Windows-side ecosystems and in some
// cert-manager outputs; rejecting the format outright would push those
// consumers to bypass the helpers altogether.
//
// Defaults and posture mirror the parent package: TLS 1.3 minimum,
// [mtls.Options.InsecureSkipVerify] rejected, file reads bounded by
// [mtls.MaxCertFileSize], magic-number check before decoding.
package pkcs12

import (
	"crypto/tls"
	"errors"
	"fmt"

	pkcs12 "software.sslmate.com/src/go-pkcs12"

	"github.com/Pinguteca/sdk-core-go/transport/mtls"
)

// asn1SequenceTag is the leading byte of a DER-encoded ASN.1 SEQUENCE,
// which is the outer structure of every PKCS#12 bundle.
const asn1SequenceTag byte = 0x30

// Sentinel errors specific to this package. Errors shared with the parent
// package (size cap, insecure skip, etc.) come from `mtls` directly and
// should be matched there.
var (
	ErrEmptyP12Path  = errors.New("mtls/pkcs12: pkcs12 path is empty")
	ErrInvalidPKCS12 = errors.New("mtls/pkcs12: file is not a PKCS#12 bundle")
)

// Config loads a PKCS#12/PFX bundle (cert, key, optional intermediate
// chain) and returns a [*tls.Config]. caCertPath, when non-empty, appends
// a PEM CA bundle to the system root pool. The PKCS#12 bundle's own chain
// is sent to the server; the CA file pins which roots are trusted on
// inbound verification.
func Config(p12Path, password, caCertPath string, opts mtls.Options) (*tls.Config, error) {
	if p12Path == "" {
		return nil, ErrEmptyP12Path
	}

	raw, err := mtls.ReadBoundedFile(p12Path, "pkcs12")
	if err != nil {
		return nil, err
	}
	if err := ensurePKCS12(raw); err != nil {
		return nil, err
	}

	privKey, leaf, chain, err := pkcs12.DecodeChain(raw, password)
	if err != nil {
		return nil, fmt.Errorf("mtls/pkcs12: decode pkcs12: %w", err)
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

	cfg, err := mtls.Assemble(cert, caCertPath, opts)
	if err != nil {
		return nil, err
	}
	return cfg, nil
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
