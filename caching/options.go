package caching

import (
	"context"
	"log/slog"
	"time"
)

// Spec describes the cache policy for a single procedure. Read
// methods set TTL > 0 to enable caching. Write methods leave TTL at
// zero and populate Invalidates with the read-method names whose
// cache entries should be flushed on a successful write.
//
// Field order groups the slice (pointer) at the front so the
// fieldalignment check stays satisfied.
type Spec struct {
	Invalidates []string

	TTL         time.Duration
	SWR         time.Duration
	NegativeTTL time.Duration
}

// Cacheable reports whether the spec describes a cacheable read.
func (s Spec) Cacheable() bool { return s.TTL > 0 }

// Options configures the caching transport.
type Options struct {
	// Store is the pluggable cache backend. When nil the transport
	// passes every call through.
	Store Cache

	// KeyScope returns the tenant identifier from the request
	// context. Returning the empty string opts into single-tenant
	// mode with explicit acknowledgement. Required by default-deny:
	// if KeyScope is nil the transport passes every call through
	// without caching, per RFC 0015.
	KeyScope func(ctx context.Context) string

	// MethodConfig maps fully-qualified procedure paths
	// (`/service.v1.Svc/Method`) to their cache specs. Methods
	// absent from the map pass through uncached.
	MethodConfig map[string]Spec

	// Logger receives one structured record per cache outcome (hit,
	// miss, swr-hit, negative-hit, bypass). Optional; the transport
	// works without it.
	Logger *slog.Logger

	// Now overrides the clock for tests. Production code leaves this
	// nil so the transport uses [time.Now].
	Now func() time.Time

	// MaxBodyBytes caps the size of a response body the transport will
	// buffer into a cache entry. Zero selects [DefaultMaxBodyBytes].
	//
	// A response over the cap fails with [ErrBodyTooLarge] rather than
	// being buffered, because the entry is shared across singleflight
	// callers and cannot be streamed past the cache. Consumers
	// expecting large payloads on a cached method should raise this or
	// drop the method from MethodConfig.
	//
	// Declared last so the pointer-width fields stay grouped for the
	// fieldalignment check.
	MaxBodyBytes int64
}

// DefaultMaxBodyBytes bounds a single cached response body. It matches
// the 1 MiB ceiling the oauth package applies to token endpoint
// responses, for the same reason: a misbehaving server must not be able
// to pin arbitrary memory in the client.
const DefaultMaxBodyBytes int64 = 1 << 20
