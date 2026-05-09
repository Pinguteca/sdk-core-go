package pagination_test

import (
	"context"
	"errors"
	"iter"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Pinguteca/sdk-core-go/pagination"
)

// pages3 returns 3 fixed pages of [1,2], [3,4], [5,6].
func pages3() pagination.FetchPage[int] {
	return func(_ context.Context, token string) ([]int, string, error) {
		switch token {
		case "":
			return []int{1, 2}, "p2", nil
		case "p2":
			return []int{3, 4}, "p3", nil
		case "p3":
			return []int{5, 6}, "", nil
		}
		return nil, "", errors.New("unexpected token")
	}
}

func TestIterYieldsAllItemsInOrder(t *testing.T) {
	t.Parallel()
	var got []int
	for v, err := range pagination.Iter(context.Background(), pages3()) {
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		got = append(got, v)
	}
	want := []int{1, 2, 3, 4, 5, 6}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestIterStopsOnFetchError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fetch := func(_ context.Context, token string) ([]int, string, error) {
		if token == "" {
			return []int{1, 2}, "p2", nil
		}
		return nil, "", wantErr
	}

	var items []int
	var observedErr error
	for v, err := range pagination.Iter(context.Background(), fetch) {
		if err != nil {
			observedErr = err
			break
		}
		items = append(items, v)
	}
	if !errors.Is(observedErr, wantErr) {
		t.Fatalf("err = %v, want %v", observedErr, wantErr)
	}
	if len(items) != 2 {
		t.Fatalf("items = %v, want first page only", items)
	}
}

func TestIterStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var observedErr error
	for _, err := range pagination.Iter(ctx, pages3()) {
		if err != nil {
			observedErr = err
			break
		}
	}
	if !errors.Is(observedErr, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", observedErr)
	}
}

func TestIterEmptyFirstPage(t *testing.T) {
	t.Parallel()
	fetch := func(context.Context, string) ([]int, string, error) {
		return nil, "", nil
	}
	count := 0
	for range pagination.Iter(context.Background(), fetch) {
		count++
	}
	if count != 0 {
		t.Fatalf("expected zero items, got %d", count)
	}
}

func TestIterParallelPreservesOrder(t *testing.T) {
	t.Parallel()
	var got []int
	for v, err := range pagination.IterParallel(context.Background(), pages3(), 2) {
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		got = append(got, v)
	}
	want := []int{1, 2, 3, 4, 5, 6}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestIterParallelLookaheadZeroFallsBack(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	fetch := func(_ context.Context, token string) ([]int, string, error) {
		calls.Add(1)
		if token == "" {
			return []int{1}, "", nil
		}
		return nil, "", nil
	}
	for range pagination.IterParallel(context.Background(), fetch, 0) {
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
}

func TestIterParallelPrefetchesAhead(t *testing.T) {
	t.Parallel()
	// Pagination via next-page-token is inherently sequential (page N
	// depends on page N-1's token), so concurrent fetches are impossible.
	// "Prefetch" means: while the consumer is processing page N, the
	// producer is fetching page N+1. Verified here by counting how many
	// pages have been fetched at the point the consumer reads its first
	// item — with lookahead=2 the producer should be working ahead.
	var fetchCount atomic.Int32
	fetch := func(_ context.Context, token string) ([]int, string, error) {
		fetchCount.Add(1)
		switch token {
		case "":
			return []int{1}, "p2", nil
		case "p2":
			return []int{2}, "p3", nil
		case "p3":
			return []int{3}, "", nil
		}
		return nil, "", nil
	}

	seq := pagination.IterParallel(context.Background(), fetch, 2)
	next, stop := iter.Pull2(seq)
	defer stop()

	// Pull the first item, then give the producer time to keep fetching.
	if _, _, ok := next(); !ok {
		t.Fatalf("no first item")
	}
	time.Sleep(20 * time.Millisecond)
	if got := fetchCount.Load(); got < 2 {
		t.Fatalf("fetchCount after first item = %d, want at least 2 (producer should prefetch)", got)
	}
	for {
		if _, _, ok := next(); !ok {
			break
		}
	}
}

func TestIterParallelSurfacesErrorAfterEarlierItems(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("page 2 fail")
	fetch := func(_ context.Context, token string) ([]int, string, error) {
		switch token {
		case "":
			return []int{1, 2}, "p2", nil
		case "p2":
			return nil, "", wantErr
		}
		return nil, "", nil
	}

	var items []int
	var observedErr error
	for v, err := range pagination.IterParallel(context.Background(), fetch, 2) {
		if err != nil {
			observedErr = err
			break
		}
		items = append(items, v)
	}
	if !errors.Is(observedErr, wantErr) {
		t.Fatalf("err = %v, want %v", observedErr, wantErr)
	}
	if len(items) != 2 || items[0] != 1 || items[1] != 2 {
		t.Fatalf("items = %v, want page-1 items before error", items)
	}
}

func TestCollectSuccess(t *testing.T) {
	t.Parallel()
	got, err := pagination.Collect(context.Background(), pages3())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("len = %d, want 6", len(got))
	}
}

func TestCollectPartialOnError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("page 2 fail")
	fetch := func(_ context.Context, token string) ([]int, string, error) {
		if token == "" {
			return []int{1, 2}, "p2", nil
		}
		return nil, "", wantErr
	}
	got, err := pagination.Collect(context.Background(), fetch)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if len(got) != 2 {
		t.Fatalf("collected = %v, want partial [1, 2]", got)
	}
}
