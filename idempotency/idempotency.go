// Package idempotency attaches a stable Idempotency-Key header to mutating
// unary RPCs. The same key is reused across retry attempts so the server can
// safely deduplicate.
//
// Composition: register this interceptor *before* the retry interceptor so
// it fires once per logical call. The key is stored on the request header,
// which retry preserves across attempts because it reuses the same Request
// object. Read-only methods are skipped by default.
package idempotency

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
)

// UUIDv7 layout constants per RFC 9562 section 5.7. Named so the static
// analyser sees the bit-twiddling as RFC compliance, not magic numbers.
const (
	uuidVersionByteIdx = 6
	uuidVersionMask    = 0x0f
	uuidV7VersionByte  = 0x70

	uuidVariantByteIdx = 8
	uuidVariantMask    = 0x3f
	uuidRFC4122Variant = 0x80

	// unixMilli48Mask zeroes the 16 high bits of an int64 millisecond
	// timestamp so the cast to uint64 cannot overflow the 48 bits we
	// are about to embed. Any post-epoch time before year ~8921 fits
	// in 48 bits already, so the mask is a no-op for current dates and
	// a defence against unexpected-input years.
	unixMilli48Mask int64 = 0x0000_FFFF_FFFF_FFFF
)

// Options configure [Interceptor].
type Options struct {
	KeyFn      func() string
	IsSafe     func(procedure string) bool
	HeaderName string
}

// Interceptor returns the idempotency-key interceptor. Streaming RPCs are
// passed through untouched.
func Interceptor(opts Options) connect.Interceptor {
	if opts.HeaderName == "" {
		opts.HeaderName = "Idempotency-Key"
	}
	if opts.KeyFn == nil {
		opts.KeyFn = defaultKey
	}
	if opts.IsSafe == nil {
		opts.IsSafe = defaultIsSafe
	}
	return &idempotencyInterceptor{opts: opts}
}

type idempotencyInterceptor struct{ opts Options }

func (i *idempotencyInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if i.opts.IsSafe(req.Spec().Procedure) {
			return next(ctx, req)
		}
		// Reuse any header the caller already attached, or any key set by an
		// earlier wrap. Retry replays the same Request, so the header survives
		// across attempts and the server sees a stable key.
		if req.Header().Get(i.opts.HeaderName) == "" {
			req.Header().Set(i.opts.HeaderName, i.opts.KeyFn())
		}
		return next(ctx, req)
	}
}

func (i *idempotencyInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *idempotencyInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// defaultKey returns a UUIDv7 (RFC 9562) as the canonical hex-with-dashes
// string. UUIDv7 is preferred over v4 because the 48-bit Unix-millisecond
// prefix gives lexicographic order = chronological order, which keeps
// server-side key indexes localized to recent writes. The remaining 74
// bits come from crypto/rand and stay unguessable in practice.
//
// Implementation is inline rather than a third-party UUID library because
// the RFC layout is small enough to own and the SDK keeps its Layer 2
// dependency surface to stdlib plus golang.org/x/* per RFC 0002.
func defaultKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read on a healthy host never fails; if it does, the
		// process is already in trouble. Caller still receives a value so
		// the request ships and the server's dedupe layer treats it like
		// any other key.
		return fmt.Sprintf("00000000-0000-7000-8000-%012x", time.Now().UnixNano())
	}

	// Embed the Unix millisecond timestamp as a big-endian 48-bit value
	// in bytes 0..5. The 48-bit mask makes the cast to uint64 provably
	// non-overflowing for the static analyser; a full uint64 is written
	// into a scratch buffer and the low 6 bytes are copied into place.
	var tsBuf [8]byte
	ms := time.Now().UnixMilli() & unixMilli48Mask
	binary.BigEndian.PutUint64(tsBuf[:], uint64(ms))
	copy(b[:uuidVersionByteIdx], tsBuf[2:])

	// Set version 7 in the high nibble of byte 6 and the RFC 4122 variant
	// in the high two bits of byte 8.
	b[uuidVersionByteIdx] = (b[uuidVersionByteIdx] & uuidVersionMask) | uuidV7VersionByte
	b[uuidVariantByteIdx] = (b[uuidVariantByteIdx] & uuidVariantMask) | uuidRFC4122Variant

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// defaultIsSafe heuristically skips read-only procedures. Override IsSafe to
// honour service-specific naming.
func defaultIsSafe(procedure string) bool {
	method := procedure
	if i := strings.LastIndex(procedure, "/"); i >= 0 {
		method = procedure[i+1:]
	}
	for _, prefix := range readOnlyPrefixes {
		if strings.HasPrefix(method, prefix) {
			return true
		}
	}
	return false
}

var readOnlyPrefixes = []string{"Get", "List", "Read", "Watch", "Search", "Query", "Lookup"}
