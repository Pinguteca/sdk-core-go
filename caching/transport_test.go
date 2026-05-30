package caching

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newReq(t *testing.T, path, body string) *http.Request {
	t.Helper()
	u, err := url.Parse("https://example.com" + path)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Request{
		Method: http.MethodPost,
		URL:    u,
		Header: make(http.Header),
		Body:   io.NopCloser(strings.NewReader(body)),
	}
}

func respondJSON(status int, body string, headers http.Header) *http.Response {
	h := http.Header{}
	maps.Copy(h, headers)
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/json")
	}
	return &http.Response{
		StatusCode:    status,
		Header:        h,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func drain(t *testing.T, resp *http.Response) {
	t.Helper()
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
}

func TestTransport_NoKeyScope_PassesThrough(t *testing.T) {
	var calls atomic.Int32
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return respondJSON(http.StatusOK, `{"ok":true}`, nil), nil
	})
	tr := Transport(inner, Options{
		Store: NewMemoryCache(8),
		// KeyScope intentionally nil
		MethodConfig: map[string]Spec{"/svc/Get": {TTL: time.Minute}},
	})
	if _, ok := tr.(*transport); ok {
		t.Fatal("expected default-deny passthrough when KeyScope is nil")
	}
	resp, err := tr.RoundTrip(newReq(t, "/svc/Get", "{}"))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, resp)
	if calls.Load() != 1 {
		t.Fatalf("expected 1 inner call, got %d", calls.Load())
	}
}

func TestTransport_UnconfiguredMethod_PassesThrough(t *testing.T) {
	var calls atomic.Int32
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return respondJSON(http.StatusOK, "{}", nil), nil
	})
	tr := Transport(inner, Options{
		Store:        NewMemoryCache(8),
		KeyScope:     func(context.Context) string { return "" },
		MethodConfig: map[string]Spec{"/svc/Cached": {TTL: time.Minute}},
	})
	r1, _ := tr.RoundTrip(newReq(t, "/svc/NotCached", "{}"))
	drain(t, r1)
	r2, _ := tr.RoundTrip(newReq(t, "/svc/NotCached", "{}"))
	drain(t, r2)
	if calls.Load() != 2 {
		t.Fatalf("expected 2 inner calls (no caching), got %d", calls.Load())
	}
}

func TestTransport_MissThenHit(t *testing.T) {
	var calls atomic.Int32
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return respondJSON(http.StatusOK, `{"name":"alice"}`, nil), nil
	})
	tr := Transport(inner, Options{
		Store:        NewMemoryCache(8),
		KeyScope:     func(context.Context) string { return "tenant-a" },
		MethodConfig: map[string]Spec{"/svc/Get": {TTL: time.Minute}},
	})
	resp1, err := tr.RoundTrip(newReq(t, "/svc/Get", `{"id":1}`))
	if err != nil {
		t.Fatal(err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	_ = resp1.Body.Close()
	resp2, err := tr.RoundTrip(newReq(t, "/svc/Get", `{"id":1}`))
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if calls.Load() != 1 {
		t.Errorf("expected 1 inner call (miss+hit), got %d", calls.Load())
	}
	if !bytes.Equal(body1, body2) {
		t.Errorf("bodies differ: %s vs %s", body1, body2)
	}
}

func TestTransport_TenantIsolation(t *testing.T) {
	var calls atomic.Int32
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return respondJSON(http.StatusOK, "{}", nil), nil
	})
	scope := "tenant-a"
	tr := Transport(inner, Options{
		Store:        NewMemoryCache(8),
		KeyScope:     func(context.Context) string { return scope },
		MethodConfig: map[string]Spec{"/svc/Get": {TTL: time.Minute}},
	})
	r1, _ := tr.RoundTrip(newReq(t, "/svc/Get", `{"id":1}`))
	drain(t, r1)
	scope = "tenant-b"
	r2, _ := tr.RoundTrip(newReq(t, "/svc/Get", `{"id":1}`))
	drain(t, r2)
	if calls.Load() != 2 {
		t.Errorf("expected separate caches per tenant, got %d inner calls", calls.Load())
	}
}

func TestTransport_NegativeCaching(t *testing.T) {
	var calls atomic.Int32
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return respondJSON(http.StatusNotFound, `{"error":"not found"}`, nil), nil
	})
	tr := Transport(inner, Options{
		Store:    NewMemoryCache(8),
		KeyScope: func(context.Context) string { return "t" },
		MethodConfig: map[string]Spec{
			"/svc/Get": {TTL: time.Minute, NegativeTTL: time.Minute},
		},
	})
	r1, _ := tr.RoundTrip(newReq(t, "/svc/Get", "{}"))
	if r1.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", r1.StatusCode)
	}
	drain(t, r1)
	r2, _ := tr.RoundTrip(newReq(t, "/svc/Get", "{}"))
	drain(t, r2)
	if calls.Load() != 1 {
		t.Errorf("expected negative-cache hit on second call, got %d inner calls", calls.Load())
	}
}

func TestTransport_NegativeCachingDisabledByDefault(t *testing.T) {
	var calls atomic.Int32
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return respondJSON(http.StatusNotFound, "{}", nil), nil
	})
	tr := Transport(inner, Options{
		Store:        NewMemoryCache(8),
		KeyScope:     func(context.Context) string { return "t" },
		MethodConfig: map[string]Spec{"/svc/Get": {TTL: time.Minute}},
	})
	r1, _ := tr.RoundTrip(newReq(t, "/svc/Get", "{}"))
	drain(t, r1)
	r2, _ := tr.RoundTrip(newReq(t, "/svc/Get", "{}"))
	drain(t, r2)
	if calls.Load() != 2 {
		t.Errorf("expected no negative caching, got %d inner calls", calls.Load())
	}
}

func TestTransport_ServerErrorsNotCached(t *testing.T) {
	var calls atomic.Int32
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return respondJSON(http.StatusServiceUnavailable, "{}", nil), nil
	})
	tr := Transport(inner, Options{
		Store:        NewMemoryCache(8),
		KeyScope:     func(context.Context) string { return "t" },
		MethodConfig: map[string]Spec{"/svc/Get": {TTL: time.Minute}},
	})
	r1, _ := tr.RoundTrip(newReq(t, "/svc/Get", "{}"))
	drain(t, r1)
	r2, _ := tr.RoundTrip(newReq(t, "/svc/Get", "{}"))
	drain(t, r2)
	if calls.Load() != 2 {
		t.Errorf("expected 503 to bypass cache, got %d inner calls", calls.Load())
	}
}

func TestTransport_WriteInvalidatesMatchingReads(t *testing.T) {
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return respondJSON(http.StatusOK, `{"ok":true}`, nil), nil
	})
	store := NewMemoryCache(8)
	tr := Transport(inner, Options{
		Store:    store,
		KeyScope: func(context.Context) string { return "t" },
		MethodConfig: map[string]Spec{
			"/user.v1.Svc/GetUser":    {TTL: time.Minute},
			"/user.v1.Svc/UpdateUser": {Invalidates: []string{"GetUser"}},
		},
	})
	r1, _ := tr.RoundTrip(newReq(t, "/user.v1.Svc/GetUser", `{"id":1}`))
	drain(t, r1)
	r2, _ := tr.RoundTrip(newReq(t, "/user.v1.Svc/UpdateUser", `{"id":1}`))
	drain(t, r2)
	// Cache should be empty for GetUser now.
	body, _ := readAndRestoreBody(newReq(t, "/user.v1.Svc/GetUser", `{"id":1}`))
	key := BuildKey("t", "/user.v1.Svc/GetUser", body)
	if _, found, _ := store.Get(context.Background(), key); found {
		t.Error("expected write to invalidate cached GetUser entry")
	}
}

func TestTransport_SingleFlightCollapsesConcurrentMisses(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-release
		return respondJSON(http.StatusOK, `{"ok":true}`, nil), nil
	})
	tr := Transport(inner, Options{
		Store:        NewMemoryCache(8),
		KeyScope:     func(context.Context) string { return "t" },
		MethodConfig: map[string]Spec{"/svc/Get": {TTL: time.Minute}},
	})
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			resp, err := tr.RoundTrip(newReq(t, "/svc/Get", "{}"))
			if err == nil {
				_, _ = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
			}
		})
	}
	// Let the goroutines pile up on the singleflight lock.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Errorf("expected single-flight to collapse to 1 inner call, got %d", calls.Load())
	}
}

func TestTransport_ETagRevalidation(t *testing.T) {
	var calls atomic.Int32
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			return &http.Response{
				StatusCode: http.StatusNotModified,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}
		return respondJSON(http.StatusOK, `{"v":1}`, http.Header{"Etag": []string{`"v1"`}}), nil
	})

	now := time.Now()
	clock := now
	tr := Transport(inner, Options{
		Store:    NewMemoryCache(8),
		KeyScope: func(context.Context) string { return "t" },
		MethodConfig: map[string]Spec{
			"/svc/Get": {TTL: 100 * time.Millisecond, SWR: time.Minute},
		},
		Now: func() time.Time { return clock },
	})

	// First call populates the cache.
	r1, _ := tr.RoundTrip(newReq(t, "/svc/Get", "{}"))
	drain(t, r1)

	// Advance past TTL but within SWR.
	clock = now.Add(time.Second)
	// SWR hit returns cached value immediately and schedules an
	// async refresh. The refresh sends If-None-Match and expects 304.
	r2, _ := tr.RoundTrip(newReq(t, "/svc/Get", "{}"))
	body2, _ := io.ReadAll(r2.Body)
	_ = r2.Body.Close()
	if string(body2) != `{"v":1}` {
		t.Errorf("expected stale body served, got %q", body2)
	}
	// Give the background refresh a moment.
	time.Sleep(100 * time.Millisecond)
	if calls.Load() < 2 {
		t.Errorf("expected at least 2 inner calls (initial + refresh), got %d", calls.Load())
	}
}
