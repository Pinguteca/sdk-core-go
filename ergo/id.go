package ergo

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// idBytes is the size in bytes of a generated op or correlation id.
// 128 bits matches UUID v4 strength; collisions are effectively
// impossible at SDK call volumes.
const idBytes = 16

// newID returns a 32-character hex string drawn from crypto/rand.
// crypto/rand is FIPS-approved (SP 800-90A DRBG on every supported
// platform); returning an error rather than falling back keeps the
// caller in control when entropy is genuinely unavailable.
func newID() (string, error) {
	var b [idBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("ergo: read crypto/rand: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
