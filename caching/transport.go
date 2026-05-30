package caching

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
)

// ErrCorruptSingleflight is returned when the singleflight result is
// not a cache [Entry]. Indicates a programming error inside this
// package, not a runtime condition.
var ErrCorruptSingleflight = errors.New("caching: corrupt singleflight result")

// Transport wraps inner with HTTP-layer caching per RFC 0015. When
// opts.Store or opts.KeyScope is nil, Transport returns inner
// unchanged (default-deny tenant isolation: forgetting to wire
// KeyScope must not silently cache across tenants).
//
// Streaming RPCs and any procedure absent from opts.MethodConfig
// pass through untouched.
//
// Negative caching is HTTP-status-aware: a method declared with
// NegativeTTL only caches a 404 when the inner transport returns an
// actual HTTP 404. Connect-Go consumers using Connect's GET mode for
// NO_SIDE_EFFECTS methods see real 4xx statuses on error; consumers
// using Connect's POST mode receive 200-plus-error-envelope
// responses and negative caching does not apply.
func Transport(inner http.RoundTripper, opts Options) http.RoundTripper {
	if inner == nil {
		inner = http.DefaultTransport
	}
	if opts.Store == nil || opts.KeyScope == nil {
		return inner
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &transport{inner: inner, opts: opts}
}

type transport struct {
	inner http.RoundTripper
	opts  Options
	sf    singleflight.Group
}

// RoundTrip implements [http.RoundTripper].
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	method := req.URL.Path
	spec, ok := t.opts.MethodConfig[method]
	if !ok {
		resp, err := t.inner.RoundTrip(req)
		if err != nil {
			return resp, fmt.Errorf("caching: inner roundtrip: %w", err)
		}
		return resp, nil
	}

	ctx := req.Context()
	scope := t.opts.KeyScope(ctx)

	if !spec.Cacheable() {
		return t.forwardAndInvalidate(req, spec, scope, method)
	}

	body, err := readAndRestoreBody(req)
	if err != nil {
		return nil, err
	}
	key := BuildKey(scope, method, body)
	now := t.opts.Now()

	entry, found, _ := t.opts.Store.Get(ctx, key)
	if found {
		if !entry.Expired(now) {
			t.log(ctx, "hit", method, &entry, now)
			return responseFromEntry(req, entry), nil
		}
		if entry.Stale(now) {
			t.refreshAsync(req, key, spec, entry, body)
			t.log(ctx, "swr-hit", method, &entry, now)
			return responseFromEntry(req, entry), nil
		}
	}

	t.log(ctx, "miss", method, nil, now)
	return t.fetchAndCache(req, key, spec, body, entry, found)
}

func (t *transport) forwardAndInvalidate(
	req *http.Request, spec Spec, scope, writeMethod string,
) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		return resp, fmt.Errorf("caching: inner roundtrip: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return resp, nil
	}
	service := serviceFromMethod(writeMethod)
	for _, m := range spec.Invalidates {
		prefix := scope + ":" + service + m + ":"
		_ = t.opts.Store.DeleteMatching(req.Context(), prefix)
	}
	return resp, nil
}

func (t *transport) fetchAndCache(
	req *http.Request, key string, spec Spec, body []byte, prev Entry, hadPrev bool,
) (*http.Response, error) {
	ctx := req.Context()
	result, err, _ := t.sf.Do(key, func() (any, error) {
		return t.singleflightFetch(ctx, req, spec, body, prev, hadPrev, key)
	})
	if err != nil {
		return nil, fmt.Errorf("caching: singleflight: %w", err)
	}
	cached, ok := result.(Entry)
	if !ok {
		return nil, ErrCorruptSingleflight
	}
	return responseFromEntry(req, cached), nil
}

func (t *transport) singleflightFetch(
	ctx context.Context, req *http.Request, spec Spec, body []byte,
	prev Entry, hadPrev bool, key string,
) (Entry, error) {
	cloned := req.Clone(ctx)
	cloned.Body = io.NopCloser(bytes.NewReader(body))
	if hadPrev && prev.ETag != "" {
		cloned.Header.Set("If-None-Match", prev.ETag)
	}
	resp, err := t.inner.RoundTrip(cloned)
	if err != nil {
		return Entry{}, fmt.Errorf("caching: inner roundtrip: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return t.processResponse(ctx, key, spec, prev, hadPrev, resp)
}

func (t *transport) refreshAsync(req *http.Request, key string, spec Spec, prev Entry, body []byte) {
	// Background refresh outlives the foreground request: strip the
	// cancellation signal but keep context values so any logging /
	// tracing scope still works.
	bgCtx := context.WithoutCancel(req.Context())
	go func() {
		_, _, _ = t.sf.Do(key, func() (any, error) {
			return t.singleflightFetch(bgCtx, req, spec, body, prev, true, key)
		})
	}()
}

func (t *transport) processResponse(
	ctx context.Context, key string, spec Spec, prev Entry, hadPrev bool, resp *http.Response,
) (Entry, error) {
	// The caller closes resp.Body via defer; processResponse owns
	// only body draining/reading.
	switch {
	case resp.StatusCode == http.StatusNotModified && hadPrev:
		refreshed := Entry{
			Body:    prev.Body,
			Headers: prev.Headers,
			ETag:    pickETag(resp.Header, prev.ETag),
			Created: t.opts.Now(),
			TTL:     spec.TTL,
			SWR:     spec.SWR,
			Status:  prev.Status,
		}
		_ = t.opts.Store.Set(ctx, key, refreshed)
		_, _ = io.Copy(io.Discard, resp.Body)
		return refreshed, nil
	case resp.StatusCode == http.StatusNotFound && spec.NegativeTTL > 0:
		entry, err := bufferResponse(resp, spec.NegativeTTL, 0, t.opts.Now())
		if err != nil {
			return Entry{}, err
		}
		_ = t.opts.Store.Set(ctx, key, entry)
		return entry, nil
	case resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices:
		entry, err := bufferResponse(resp, spec.TTL, spec.SWR, t.opts.Now())
		if err != nil {
			return Entry{}, err
		}
		_ = t.opts.Store.Set(ctx, key, entry)
		return entry, nil
	default:
		// Other errors are not cached. Buffer the response so the
		// caller still receives it; do not store.
		return bufferResponse(resp, 0, 0, t.opts.Now())
	}
}

// bufferResponse drains the response body and packs it into an
// [Entry]. The caller closes the response body via defer; the body
// reader has been fully consumed here.
func bufferResponse(resp *http.Response, ttl, swr time.Duration, now time.Time) (Entry, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Entry{}, fmt.Errorf("caching: read response body: %w", err)
	}
	return Entry{
		Body:    body,
		Headers: cloneHeaders(resp.Header),
		ETag:    resp.Header.Get("ETag"),
		Created: now,
		TTL:     ttl,
		SWR:     swr,
		Status:  resp.StatusCode,
	}, nil
}

// responseFromEntry constructs an *[http.Response] from a cached
// [Entry]. Each call returns a fresh body reader; cached bytes stay
// immutable.
func responseFromEntry(req *http.Request, entry Entry) *http.Response {
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", entry.Status, http.StatusText(entry.Status)),
		StatusCode:    entry.Status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        cloneHeaders(entry.Headers),
		Body:          io.NopCloser(bytes.NewReader(entry.Body)),
		ContentLength: int64(len(entry.Body)),
		Request:       req,
	}
}

func cloneHeaders(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, v := range h {
		vv := make([]string, len(v))
		copy(vv, v)
		out[k] = vv
	}
	return out
}

func pickETag(respHeaders http.Header, fallback string) string {
	if e := respHeaders.Get("ETag"); e != "" {
		return e
	}
	return fallback
}

// serviceFromMethod returns the `/service.v1.Svc/` prefix of a
// procedure path. Used for write-triggered invalidation: the
// composed prefix is `{scope}:{service}{invalidatedMethod}:`.
func serviceFromMethod(method string) string {
	i := strings.LastIndex(method, "/")
	if i <= 0 {
		return method
	}
	return method[:i+1]
}

func (t *transport) log(ctx context.Context, outcome, method string, entry *Entry, now time.Time) {
	if t.opts.Logger == nil {
		return
	}
	attrs := []slog.Attr{
		slog.String("cache.outcome", outcome),
		slog.String("rpc.method", method),
	}
	if entry != nil {
		attrs = append(attrs,
			slog.Int64("cache.age_ms", now.Sub(entry.Created).Milliseconds()),
		)
	}
	t.opts.Logger.LogAttrs(ctx, slog.LevelInfo, "cache", attrs...)
}
