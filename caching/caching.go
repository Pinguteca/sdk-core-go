// Package caching provides an HTTP-layer caching transport for
// Connect-Go clients. It implements the cross-SDK contract pinned in
// sdk-scaffold/docs/rfc/0015-caching-strategy.md: schema-driven
// per-method opt-in via the [Options.MethodConfig] map, content-hashed
// cache keys, default-deny tenant-scope isolation, TTL plus ETag plus
// write-triggered invalidation, opt-in stale-while-revalidate and
// negative caching, default-on single-flight, and streaming
// pass-through.
//
// The transport wraps an inner [net/http.RoundTripper], so it composes
// with any Connect-Go client without changes to the interceptor
// chain. Streaming RPCs and methods absent from [Options.MethodConfig]
// pass through untouched.
//
// This module ships as a Layer 3 companion because the realistic
// cache stores (Redis, Memcached) are third-party. The in-memory
// default lives in the same module for consistency with the Layer 3
// shape used by compression, logging, and hedge.
package caching

import (
	"context"
	"net/http"
	"time"
)

// Entry is a cached HTTP response: the body bytes, the response
// headers, the status code, the ETag (when the server supplied one),
// and bookkeeping for TTL and stale-while-revalidate.
//
// Field order groups pointer-bearing fields at the front so the GC
// scan range stays tight (Created.Location is a hidden pointer at
// offset +16 inside time.Time, so we keep Created next to the other
// pointer-bearing fields). Do not reorder without re-running the
// fieldalignment check.
type Entry struct {
	Headers http.Header
	ETag    string
	Created time.Time
	Body    []byte
	TTL     time.Duration
	SWR     time.Duration
	Status  int
}

// Expired returns true when now is past Created+TTL.
func (e Entry) Expired(now time.Time) bool {
	return now.After(e.Created.Add(e.TTL))
}

// Stale returns true when now is in the SWR window past TTL. A
// stale entry is served immediately while a background fetch
// refreshes it.
func (e Entry) Stale(now time.Time) bool {
	if e.SWR <= 0 {
		return false
	}
	hardDeadline := e.Created.Add(e.TTL + e.SWR)
	return e.Expired(now) && !now.After(hardDeadline)
}

// Cache is the pluggable store the transport reads and writes
// through. In-memory and (future) distributed implementations live
// alongside this module; consumers may implement their own.
type Cache interface {
	// Get returns the cached entry for key if present and within the
	// hard SWR deadline. A nil error with found=false means a clean
	// miss.
	Get(ctx context.Context, key string) (entry Entry, found bool, err error)

	// Set inserts or refreshes the entry for key.
	Set(ctx context.Context, key string, entry Entry) error

	// Delete removes the cache entry for key. No-op when absent.
	Delete(ctx context.Context, key string) error

	// DeleteMatching removes every entry whose key contains prefix as
	// a substring. The transport composes the prefix as
	// `{scope}:{service/method}:` during write-triggered invalidation.
	DeleteMatching(ctx context.Context, prefix string) error
}
