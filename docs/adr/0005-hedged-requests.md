# ADR 0005: Hedged requests are opt-in and gated on idempotency

- **Status:** Accepted
- **Date:** 2026-05-09
- **Deciders:** Pinguteca SDK team
- **Affects:** `hedge/`; consumers wiring tail-latency-sensitive Connect clients.

## Context

Hedged requests dispatch multiple parallel attempts of the same RPC and
return the first successful response, cancelling the others. The technique
is a well-known tail-latency killer for read-heavy workloads against
variable-latency backends (Tail at Scale, Dean & Barroso 2013). It is also
load-amplifying: an N-attempt hedge with the wrong policy multiplies
request volume on a backend that may already be the reason latency is
variable.

Two decisions had to be made together:

1. **What is the policy default?** Hedging always-on amplifies cost for
   every consumer, including those whose backends are healthy and need
   no help. Hedging off-by-default forces consumers to opt in for the
   cases where it actually helps.
2. **How does it compose with retry, breaker, idempotency-key, and the
   rest of the interceptor stack?** Each composition order has different
   amplification properties.

A third concern is the safety floor: hedging a non-idempotent RPC is
always a bug, since the server may process more than one of the parallel
attempts before the loser cancels propagate. This is independent of
deliberate backend cost — it can corrupt data.

## Decision

1. **Hedging is opt-in.** The interceptor must be explicitly added to a
   client's interceptor chain; it is not part of either preset
   (`presets.Standalone` or `presets.Mesh`). Consumers who wire it
   acknowledge the load amplification trade-off.
2. **Default scope is `IdempotencyNoSideEffects` only.** Methods marked
   `option idempotency_level = NO_SIDE_EFFECTS` in the proto schema are
   safe to hedge (queries, lookups, reads). Methods marked
   `IDEMPOTENT` (writes that tolerate duplicates, e.g. set-by-key) are
   skipped unless `Config.HedgeIdempotent` is set, because IDEMPOTENT
   only guarantees a method tolerates duplicates, not that the
   duplicates are free; hedging an IDEMPOTENT mutation doubles the
   billing-relevant load. Methods marked `UNKNOWN` (the proto default)
   are never hedged.
3. **Default tuning:** 3 total attempts, 50 ms between launches.
   Stagger Delay close to the typical p50 of the target RPC so
   hedges fire only when an attempt is "definitely slow"; lowering
   Delay amplifies load on the median case for marginal tail benefit.
4. **Composition: hedge goes inside retry.** Each hedge attempt is one
   retry attempt from the perspective of the outer retry interceptor.
   When hedging is on, lower retry's MaxAttempts accordingly; total
   request volume is bounded by `retry.MaxAttempts * hedge.MaxAttempts`.
5. **Streaming RPCs pass through unchanged.** A stream cannot be
   replayed safely. Revisit when Connect-Go gains a stream-replay
   primitive that can multiplex per-message acknowledgements across
   parallel attempts (no such primitive exists today).
6. **First-success-wins semantics.** All siblings receive context
   cancellation immediately. The interceptor returns the winning
   `connect.AnyResponse`. If every attempt fails, the LAST observed
   error is returned (not the first); the last error is more likely to
   reflect the steady state of the backend rather than a transient
   that the first attempt happened to hit.

## Consequences

### Positive

- Tail-latency reductions on read-heavy SDK consumers (P99 typically
  drops 30–60% in the published Tail at Scale evaluation when Delay is
  set close to P50).
- The default-deny-on-Unknown gate prevents the worst class of bug
  (duplicate writes from accidentally hedging a non-idempotent RPC).
- Composition with the existing retry / breaker stack is unambiguous;
  no new error model needed.

### Negative

- N times request volume on hedged calls. Backends already throttling
  via `RESOURCE_EXHAUSTED` will see worse contention. The breaker
  partly mitigates this (an open breaker short-circuits all parallel
  attempts), but consumers must size their backends with the hedge
  amplification in mind.
- The `MaxAttempts` knob on hedge multiplies with retry's
  `MaxAttempts`. Forgetting to lower retry's budget when adding hedge
  silently doubles or triples the request volume. Documented in the
  package comment and the preset README.
- Tests that hedge a real backend are tricky — race ordering depends
  on real network latency. We use a deterministic clock and a
  stand-alone unit test for the orchestration loop; integration
  testing is left to consumer test suites.

### Neutral

- Hedging today does not respect a per-host or per-target retry
  budget. When we add the gRPC-style retry-throttling pattern (see
  ADR 0002 revisit conditions), hedge attempts should consume the
  same budget. Captured in the revisit list below.

## Alternatives considered

### Hedging outside retry (each hedge attempt has its own retry budget)

Rejected. Even worse amplification: `hedge.MaxAttempts *
retry.MaxAttempts` request volume per logical call. The composition
makes hedge a pure multiplier on retry's already-amplifying behaviour.

### Aggressive defaults (MaxAttempts = 5, Delay = 0)

Rejected. Best for tail-latency reduction in benchmarks; worst for
backend cost in production. The ~50 ms / 3 attempts default trades
some tail benefit for a load profile sustainable on typical
read-replicas.

### Default-allow on `IDEMPOTENT`

Rejected. The class of methods marked IDEMPOTENT is "writes that
tolerate duplicates", not "writes that are cheap to duplicate".
Defaulting hedge on them would double the load of every billed write
that an SDK consumer makes. Opt-in via `HedgeIdempotent` lets a
consumer decide per use case (e.g. internal metrics writes vs.
external billing API calls).

### Hedging streaming RPCs

Rejected for v1. Streaming hedge requires per-message acknowledgement
multiplexing across parallel streams, with reconciliation on cancel.
Connect-Go has no primitive for this.

### Returning the FIRST error when all attempts fail

Rejected in favour of LAST. The first attempt is the longest in
flight by definition; its error is more likely to reflect the
state-at-the-time-it-was-launched, which has been superseded by
later attempts. The last attempt's error reflects the most recent
backend state.

## Revisit when

- Connect-Go ships first-class hedging. Drop the `hedge/` package and
  delegate to whatever protocol-level support arrives.
- The retry interceptor gains a retry-throttling budget (gRPC's
  `retry_throttling`). Hedge attempts should consume the same budget
  so opt-in hedging cannot bypass the throttle.
- Per-host load awareness becomes available (e.g. via Connect's
  Peer information or a future load-balancer interceptor). Hedge
  should NOT fire when the target is already saturated; today it
  fires unconditionally on idempotent RPCs.
- A vulnerability or bug in the deterministic clock / channel
  orchestration is reported. The implementation is small (~150 LoC)
  but has subtle goroutine-leak surfaces.

## References

- Dean and Barroso, *The Tail at Scale*, CACM 2013.
- gRPC retry_throttling design: https://github.com/grpc/proposal/blob/master/A6-client-retries.md
- resty's `hedging.go` was prior art; we stayed on Connect interceptors
  for the layering reasons in ADR 0003.
- ADR 0002 — same amplification reasoning applied to mesh + SDK CB.
