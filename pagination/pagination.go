// Package pagination provides consumer-side helpers for paginated RPCs. The
// typical Connect-Go pattern is a List/Search method that returns a page of
// items plus a next-page token; iterating until the token is empty is
// boilerplate that every consumer would otherwise re-roll. This package turns
// a "fetch one page" function into an iter.Seq2[T, error] (Go 1.23+) that
// yields every item across all pages, plus an opt-in parallel-prefetch
// variant for I/O-bound consumers.
//
// This is intentionally NOT a Connect interceptor. It runs in consumer code
// wrapping the generated RPC stubs; the interceptor stack still applies to
// each underlying RPC the helper performs.
package pagination

import (
	"context"
	"iter"
)

// DefaultLookahead is the recommended buffer for [IterParallel] when the
// caller does not pick its own. Two pages of headroom hides typical
// per-request latency without ballooning memory for large pages.
const DefaultLookahead = 2

// FetchPage fetches one page of items. It receives a page token (empty on the
// first call) and returns the items, the token to pass on the next call
// (empty when there are no more pages), and any error.
type FetchPage[T any] func(ctx context.Context, pageToken string) (items []T, nextToken string, err error)

// Iter returns an iterator that walks every item across every page. Iteration
// stops on the first error from fetch, on context cancellation, or when fetch
// returns an empty next-page token. Errors are surfaced via the second yield
// value; the caller should always inspect it.
//
//	for item, err := range pagination.Iter(ctx, fetch) {
//	    if err != nil { return err }
//	    handle(item)
//	}
func Iter[T any](ctx context.Context, fetch FetchPage[T]) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		var token string
		for {
			if err := ctx.Err(); err != nil {
				yield(zero, err)
				return
			}
			items, next, err := fetch(ctx, token)
			if err != nil {
				yield(zero, err)
				return
			}
			for _, item := range items {
				if !yield(item, nil) {
					return
				}
			}
			if next == "" {
				return
			}
			token = next
		}
	}
}

// IterParallel is like [Iter] but prefetches up to lookahead pages ahead of
// the consumer. Pages are yielded in order; an error from page N appears in
// the iterator AFTER all items from pages 0..N-1 have been yielded. lookahead
// less than 1 falls back to sequential [Iter].
//
// Use IterParallel when each fetch has non-trivial latency (network or
// database) and the consumer's per-item work is fast enough to keep up.
// Sequential Iter is the right choice when the consumer is the bottleneck.
func IterParallel[T any](ctx context.Context, fetch FetchPage[T], lookahead int) iter.Seq2[T, error] {
	if lookahead < 1 {
		return Iter(ctx, fetch)
	}
	return func(yield func(T, error) bool) {
		fctx, cancel := context.WithCancel(ctx)
		defer cancel()

		ch := make(chan page[T], lookahead)
		go runProducer(fctx, fetch, ch)

		consumePages(ch, yield)
	}
}

type page[T any] struct {
	err   error
	next  string
	items []T
}

// runProducer walks pages sequentially (page N depends on page N-1's token)
// and pushes them onto ch. Concurrency vs the consumer comes from the channel
// buffer: while the consumer processes page N, the producer can already be
// fetching page N+1, capped at len(ch) pages ahead.
func runProducer[T any](ctx context.Context, fetch FetchPage[T], ch chan<- page[T]) {
	defer close(ch)
	var token string
	for {
		items, next, err := fetch(ctx, token)
		select {
		case ch <- page[T]{items: items, err: err, next: next}:
		case <-ctx.Done():
			return
		}
		if err != nil || next == "" {
			return
		}
		token = next
	}
}

// consumePages yields items from each buffered page in order. An error from
// page N appears after all items from pages 0..N-1 have been yielded.
func consumePages[T any](ch <-chan page[T], yield func(T, error) bool) {
	var zero T
	for p := range ch {
		if p.err != nil {
			yield(zero, p.err)
			return
		}
		for _, item := range p.items {
			if !yield(item, nil) {
				return
			}
		}
	}
}

// Collect materialises every item from fetch into a slice. On error, returns
// the items collected so far plus the error. Callers who need all-or-nothing
// semantics should treat any non-nil error as fatal and discard the partial
// slice.
func Collect[T any](ctx context.Context, fetch FetchPage[T]) ([]T, error) {
	var all []T
	for item, err := range Iter(ctx, fetch) {
		if err != nil {
			return all, err
		}
		all = append(all, item)
	}
	return all, nil
}
