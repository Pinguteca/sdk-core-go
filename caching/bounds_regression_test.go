package caching

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// Regression: DeleteMatching used strings.Contains, so an invalidation
// by tenant "42" also swept tenant "142" whose scope merely ends with
// the acting tenant's scope. RFC 0015 makes tenant isolation a stated
// property of this module, so the match must be anchored.
func TestDeleteMatching_SuffixRelatedTenantSurvives(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryCache(16)

	const (
		actorKey  = "42:/svc.v1.Svc/Get:deadbeef"
		victimKey = "142:/svc.v1.Svc/Get:cafebabe"
	)
	entry := Entry{Body: []byte("payload"), Created: time.Now(), TTL: time.Hour}
	for _, k := range []string{actorKey, victimKey} {
		if err := c.Set(ctx, k, entry); err != nil {
			t.Fatalf("Set(%s): %v", k, err)
		}
	}

	if err := c.DeleteMatching(ctx, "42:/svc.v1.Svc/Get:"); err != nil {
		t.Fatalf("DeleteMatching: %v", err)
	}

	if _, found, _ := c.Get(ctx, actorKey); found {
		t.Error("acting tenant's own entry survived its invalidation")
	}
	if _, found, _ := c.Get(ctx, victimKey); !found {
		t.Error("tenant 142 was evicted by tenant 42's invalidation")
	}
}

// Regression: MemoryCache evicted on entry count only, so the real
// ceiling was capacity multiplied by whatever the upstream server chose
// to send. The byte budget has to bound retention independently.
func TestMemoryCache_EvictsOnByteBudget(t *testing.T) {
	ctx := context.Background()
	const (
		bodySize = 1024
		maxBytes = 8 * bodySize
		writes   = 40
	)
	// Capacity is deliberately far above `writes` so only the byte
	// budget can be doing the bounding here.
	c := NewMemoryCacheWithLimits(10_000, maxBytes)

	entry := Entry{Body: make([]byte, bodySize), Created: time.Now(), TTL: time.Hour}
	for i := range writes {
		if err := c.Set(ctx, fmt.Sprintf("tenant:/svc/Get:%04d", i), entry); err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
	}

	if c.bytes > maxBytes {
		t.Errorf("retained %d bytes, budget is %d", c.bytes, maxBytes)
	}
	if got := c.order.Len(); got > writes/2 {
		t.Errorf("kept %d entries; byte budget should have evicted most of %d", got, writes)
	}
	if c.order.Len() != len(c.items) {
		t.Errorf("list/map drift: list=%d map=%d", c.order.Len(), len(c.items))
	}
}

// The accounted byte total must return to zero once everything is
// removed, otherwise the budget leaks and the cache slowly refuses to
// hold anything.
func TestMemoryCache_ByteAccountingReturnsToZero(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryCache(16)
	entry := Entry{
		Body:    []byte("payload"),
		Headers: http.Header{"Etag": []string{"abc"}},
		ETag:    "abc",
		Created: time.Now(),
		TTL:     time.Hour,
	}
	for _, k := range []string{"t:/svc/A:1", "t:/svc/B:2"} {
		if err := c.Set(ctx, k, entry); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if c.bytes == 0 {
		t.Fatal("expected non-zero accounted bytes after Set")
	}
	// Overwriting the same key must not double-count.
	if err := c.Set(ctx, "t:/svc/A:1", entry); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	afterOverwrite := c.bytes

	if err := c.Delete(ctx, "t:/svc/A:1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := c.DeleteMatching(ctx, "t:/svc/B:"); err != nil {
		t.Fatalf("DeleteMatching: %v", err)
	}
	if c.bytes != 0 {
		t.Errorf("bytes = %d after removing everything (was %d), want 0", c.bytes, afterOverwrite)
	}
}

// Regression: bufferResponse called io.ReadAll with no bound, so the
// upstream server decided how much memory the client allocated.
func TestTransport_OversizedBodyRejected(t *testing.T) {
	const limit = 1024
	oversized := make([]byte, limit+1)
	for i := range oversized {
		oversized[i] = 'a'
	}

	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return respondJSON(http.StatusOK, string(oversized), nil), nil
	})
	tr := Transport(inner, Options{
		Store:        NewMemoryCache(8),
		KeyScope:     func(context.Context) string { return "tenant" },
		MethodConfig: map[string]Spec{"/svc/Get": {TTL: time.Minute}},
		MaxBodyBytes: limit,
	})

	_, err := tr.RoundTrip(newReq(t, "/svc/Get", "{}"))
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("expected ErrBodyTooLarge, got %v", err)
	}
}

// A body that exactly fills the cap is legitimate and must still cache.
func TestTransport_BodyAtExactLimitIsCached(t *testing.T) {
	const limit = 512
	body := make([]byte, limit)
	for i := range body {
		body[i] = 'b'
	}

	var calls int
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls++
		return respondJSON(http.StatusOK, string(body), nil), nil
	})
	tr := Transport(inner, Options{
		Store:        NewMemoryCache(8),
		KeyScope:     func(context.Context) string { return "tenant" },
		MethodConfig: map[string]Spec{"/svc/Get": {TTL: time.Minute}},
		MaxBodyBytes: limit,
	})

	for i := range 2 {
		resp, err := tr.RoundTrip(newReq(t, "/svc/Get", "{}"))
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		drain(t, resp)
	}
	if calls != 1 {
		t.Errorf("inner called %d times, want 1 (second should be a cache hit)", calls)
	}
}
