package caching

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestMemoryCache_GetMiss(t *testing.T) {
	c := NewMemoryCache(8)
	_, found, err := c.Get(context.Background(), "absent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("found=true for absent key")
	}
}

func TestMemoryCache_SetThenGet(t *testing.T) {
	c := NewMemoryCache(8)
	entry := Entry{
		Body:    []byte("payload"),
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Status:  200,
		Created: time.Now(),
		TTL:     time.Minute,
	}
	if err := c.Set(context.Background(), "key", entry); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, found, err := c.Get(context.Background(), "key")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if string(got.Body) != "payload" {
		t.Errorf("unexpected body: %s", got.Body)
	}
}

func TestMemoryCache_LRUEviction(t *testing.T) {
	c := NewMemoryCache(2)
	for _, k := range []string{"a", "b", "c"} {
		_ = c.Set(context.Background(), k, Entry{Body: []byte(k), TTL: time.Minute, Created: time.Now()})
	}
	// "a" should have been evicted (LRU); "b" and "c" remain.
	if _, found, _ := c.Get(context.Background(), "a"); found {
		t.Error("expected 'a' to be evicted")
	}
	if _, found, _ := c.Get(context.Background(), "c"); !found {
		t.Error("expected 'c' to be present")
	}
}

func TestMemoryCache_HardExpiryEvicts(t *testing.T) {
	c := NewMemoryCache(8)
	now := time.Now()
	c.now = func() time.Time { return now }
	_ = c.Set(context.Background(), "k", Entry{
		Body:    []byte("x"),
		Created: now,
		TTL:     time.Second,
		SWR:     time.Second,
	})
	// Within TTL: found.
	if _, found, _ := c.Get(context.Background(), "k"); !found {
		t.Fatal("expected fresh hit")
	}
	// Within SWR: still found.
	c.now = func() time.Time { return now.Add(1500 * time.Millisecond) }
	if _, found, _ := c.Get(context.Background(), "k"); !found {
		t.Fatal("expected SWR hit")
	}
	// Past SWR: gone.
	c.now = func() time.Time { return now.Add(3 * time.Second) }
	if _, found, _ := c.Get(context.Background(), "k"); found {
		t.Fatal("expected hard expiry to evict")
	}
}

func TestMemoryCache_Delete(t *testing.T) {
	c := NewMemoryCache(8)
	_ = c.Set(context.Background(), "k", Entry{Body: []byte("x"), TTL: time.Minute, Created: time.Now()})
	_ = c.Delete(context.Background(), "k")
	if _, found, _ := c.Get(context.Background(), "k"); found {
		t.Error("expected deleted entry to be gone")
	}
}

func TestMemoryCache_DeleteMatching(t *testing.T) {
	c := NewMemoryCache(8)
	now := time.Now()
	for _, k := range []string{
		"tenantA:/svc/GetUser:hash1",
		"tenantA:/svc/GetUser:hash2",
		"tenantA:/svc/ListUsers:hash3",
		"tenantB:/svc/GetUser:hash4",
	} {
		_ = c.Set(context.Background(), k, Entry{Body: []byte(k), TTL: time.Minute, Created: now})
	}
	if err := c.DeleteMatching(context.Background(), "tenantA:/svc/GetUser:"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := c.Get(context.Background(), "tenantA:/svc/GetUser:hash1"); found {
		t.Error("hash1 should be gone")
	}
	if _, found, _ := c.Get(context.Background(), "tenantA:/svc/ListUsers:hash3"); !found {
		t.Error("ListUsers should stay (different method)")
	}
	if _, found, _ := c.Get(context.Background(), "tenantB:/svc/GetUser:hash4"); !found {
		t.Error("tenantB entry should stay (different scope)")
	}
}

func TestMemoryCache_DeleteMatching_EmptyPrefixFails(t *testing.T) {
	c := NewMemoryCache(8)
	if err := c.DeleteMatching(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty prefix")
	}
}
