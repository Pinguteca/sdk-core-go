# ADR 0008: Pagination API shape

- **Status:** Accepted (retroactive — see "Process note")
- **Date:** 2026-05-09
- **Deciders:** Pinguteca SDK team
- **Affects:** `pagination` package; cross-language consistency commitments
  for future `sdk-core-dotnet`, `sdk-core-ts`, etc.

## Process note

This ADR was written *after* the `pagination` package shipped, in
violation of the "ADR before code" rule. Captured here so future
contributors see the decision as a deliberate one rather than reverse-
engineering it from the source.

## Context

Token-paginated RPCs (List/Search/etc. returning `items + next_page_token`)
are a near-universal pattern in protobuf-defined services. Without a
helper, every consumer hand-rolls the loop:

```go
var token string
for {
    resp, err := client.List(ctx, req.WithPageToken(token))
    if err != nil { return err }
    for _, item := range resp.Items { handle(item) }
    if resp.NextPageToken == "" { break }
    token = resp.NextPageToken
}
```

Repeating this in every consumer is wasteful and bug-prone (forgotten
context cancellation, dropped errors, mishandled empty pages). A shared
helper turns the loop into a one-liner.

The interesting question is the **shape** of the helper, because Go has
several options and each has cross-language implications when we replicate
the pattern in `sdk-core-dotnet` (`IAsyncEnumerable<T>`), `sdk-core-ts`
(`AsyncIterator<T>`), `sdk-core-rust` (`Stream<Item = T>`), etc.

## Decision

1. **API surface centred on `iter.Seq2[T, error]`.** Go 1.23+ stdlib type.
   Consumers iterate with `for item, err := range pagination.Iter(ctx, fetch)`.
   Errors are surfaced via the second yield value, not a separate channel
   or callback.

2. **`FetchPage[T any]` is a function type, not an interface.** Closures
   over the consumer's generated client are simpler than wrapping the
   client in a one-method interface.

3. **Two iteration variants:**
   - `Iter` — sequential, one outstanding fetch at a time.
   - `IterParallel(ctx, fetch, lookahead)` — single producer goroutine
     fetching ahead of the consumer up to `lookahead` pages. Pages are
     yielded in order; an error from page N appears after all items from
     pages 0..N-1.

4. **`Collect` returns partial results on error.** Caller decides whether
   to discard or salvage. All-or-nothing semantics are easy to layer on
   top (`if err != nil { return nil, err }`); the inverse is not.

5. **`DefaultLookahead = 2`.** Two pages of headroom amortises typical
   per-fetch latency without ballooning memory for large pages.

6. **Pagination is consumer-side only.** Not a Connect interceptor. Each
   underlying RPC still goes through the full interceptor stack
   (auth, retry, breaker, etc.).

## Consequences

### Positive

- One line of consumer code for the common case
  (`for item, err := range pagination.Iter(ctx, fetch) { ... }`).
- `iter.Seq2[T, error]` matches the Go community's emerging idiom for
  fallible iteration. New consumers will recognize it.
- Parallel prefetch is opt-in (separate function, separate name) so the
  default path stays easy to reason about.
- Cross-language: the chosen shape maps cleanly to async iterators in
  every target language.

### Negative

- Requires Go 1.23+. Some consumer codebases stuck on older toolchains
  cannot import the package without bumping. Documented in the README;
  the rest of the SDK already requires Go 1.26 so this is a non-issue
  for SDK consumers.
- `iter.Seq2` is unfamiliar to some Go developers (it landed in 1.23 and
  adoption is uneven). Mitigated by the example in the package doc.
- `Collect` returning partial-on-error is a footgun for callers who do
  not check `err`. Documented; static analysis tools will flag the
  ignored error in any case.

### Neutral

- Parallel page fetching is impossible (page N depends on page N-1's
  token); the "parallel" variant is producer-runs-ahead-of-consumer, not
  N concurrent fetches. We chose the name `IterParallel` anyway because
  the consumer-visible behaviour (work happening on a separate goroutine)
  matches the term's intuitive meaning.

## Alternatives considered

- **Channel-based API: `func Pages[T any](ctx, fetch) <-chan PageResult[T]`.**
  Rejected: callers must remember to drain on early exit to avoid
  goroutine leaks. `iter.Seq2`'s yield-returns-bool primitive solves
  early-exit cleanly without the leak risk.
- **Callback-based API: `func ForEach[T any](ctx, fetch, fn func(T) error)`.**
  Rejected: closures-with-state in the callback push complexity to the
  call site. The range syntax with `iter.Seq2` lets the consumer keep
  per-iteration state in plain locals.
- **Return `[]T, error` only (always materialize).** Rejected: forces
  every consumer to load the full result set into memory. Streaming
  iteration matters for large lists. We provide `Collect` as opt-in
  materialization.
- **Reflection-based "iterate any List* method".** Rejected: brittle and
  hides the schema. The explicit `FetchPage[T]` closure is one extra line
  but obvious in code review.

## Revisit when

- Cross-language SDK packages land. Consistency between `sdk-core-go`'s
  `iter.Seq2`, `sdk-core-dotnet`'s `IAsyncEnumerable`, and equivalents in
  TS/Rust may need a unifying naming convention (`Iter` vs `Pages` vs
  `Stream`).
- A real-world hot path benefits from server-streaming pagination (Connect
  server-streaming RPC instead of unary List + token). The API may need a
  streaming-aware variant.
- Consumer feedback shows confusion around `Collect`'s partial-on-error
  semantics. We may add `CollectAll` (all-or-nothing) as the friendlier
  default.

## References

- Go 1.23 `iter` package release notes.
- ADR 0002 (resilience and mesh coexistence) — interceptor composition,
  for the reason pagination is consumer-side.
- Stripe's pagination iterators (Ruby, Python) — prior art for
  auto-paginating SDK helpers.
