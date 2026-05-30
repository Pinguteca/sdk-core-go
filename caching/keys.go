package caching

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
)

// BuildKey composes the cache key per RFC 0015 as
// `{scope}:{method}:{sha256(body)}`. SHA-256 keeps the key size
// stable regardless of payload, and avoids leaking request bodies
// into shared cache logs (MONITOR, slow-log, metrics). SHA-256 is
// FIPS 180-4 approved.
func BuildKey(scope, method string, body []byte) string {
	sum := sha256.Sum256(body)
	return scope + ":" + method + ":" + hex.EncodeToString(sum[:])
}

// readAndRestoreBody reads the request body fully and replaces it
// with a fresh reader so the downstream RoundTripper still sees the
// same bytes. Returns the body bytes for hashing.
func readAndRestoreBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("caching: read request body: %w", err)
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	return bodyBytes, nil
}
