# ADR 0003: HTTP client choice — net/http plus Connect interceptors

- **Status:** Accepted
- **Date:** 2026-05-09
- **Deciders:** Pinguteca SDK team
- **Affects:** every package that constructs Connect clients; specifically `presets/`, `auth/`, `retry/`, `breaker/`, `idempotency/`, `otel/`, and the future `transport/mtls/`, `transport/http3/`, `compression/` packages.

## Context

The SDK builds on Connect-Go, which accepts any value satisfying
`connect.HTTPClient` (`Do(*http.Request) (*http.Response, error)`).
Two batteries-included Go HTTP clients are popular alternatives to the
standard library:

- **resty v3** (`github.com/go-resty/resty/v3`, currently beta) ships with
  retry, three-state circuit breaker (count-based and ratio-based with
  sliding window), hedging, load balancing, certificate hot-reload, digest
  auth, SSE, multipart, and configurable redirect handling.
- **req** (`github.com/imroc/req/v3`) ships with retry, automatic gzip,
  HTTP/3 transport, multipart, and proxy/middleware utilities.

Both materially overlap with the Phase 1 features we already shipped at the
Connect interceptor layer (auth, retry, breaker, idempotency, OTel) and
with the Phase 2/3 roadmap items (hedging, mTLS, HTTP/3, compression).

The question: do we adopt one of these as the underlying `HTTPClient` and
delete or thin our interceptor stack, or stay on `net/http` and keep
building at the Connect protocol layer?

A first-cut comparison underestimated resty: it actually owns most
features we plan to build. The honest comparison after corrections:

| Capability | Our stack | resty v3 |
|---|---|---|
| Token attachment | `auth.Interceptor` | `OnBeforeRequest` middleware |
| OAuth 2.0 client_credentials | `auth.ClientCredentials` (wraps `x/oauth2`) | hand-wire `x/oauth2` in middleware |
| Authorization Code / PKCE | not yet | hand-wire `x/oauth2` |
| Token refresh on 401 | not yet (Phase 2) | not built-in |
| mTLS + cert hot-reload | not yet (Phase 3) | yes, `cert_watcher.go` |
| Retry | Full + Decorrelated, idempotency-aware, `RetryInfo`-respecting | basic exp backoff with conditions |
| Circuit breaker | count-based, 3-state | count + **ratio**-based, 3-state, sliding window |
| Hedging | Phase 2 | yes, `hedging.go` |
| Load balancing | none | yes, `load_balancer.go` |
| Compression | shipping brotli + zstd + gzip | gzip auto, custom for others |
| HTTP/3 | Phase 3 | uncertain in resty; req has it |
| OTel + correlation IDs | `otel.Interceptor` | manual middleware |
| Digest auth | none | yes |
| SSE | none | yes |
| Connect-protocol awareness | **yes** | **no** |

resty/req are richer at the HTTP layer. Our stack is richer at the
Connect protocol layer. The question is which layer matters for an SDK
that only speaks Connect.

## Decision

**Stick with `net/http` as the underlying HTTPClient and keep building
features at the Connect interceptor layer.** Reject resty and req as
dependencies of `sdk-core-go` (and by extension every Pinguteca SDK
generated from the scaffold).

We borrow specific algorithmic ideas from resty when implementing our
own equivalents:

- **Ratio-based circuit breaker** as an additional `breaker.Strategy`
  alongside the count-based default (Phase 2 follow-up).
- **Hedging policy** modelled on resty's: parallel attempts on RPCs that
  the proto schema marks `idempotency_level = NO_SIDE_EFFECTS`, first
  response wins, others cancelled. Phase 2.
- **Certificate hot-reload watcher** for the future `transport/mtls`
  package. Phase 3.

## Consequences

### Positive

- **Connect protocol awareness preserved.** Our retry honours
  `google.rpc.RetryInfo` from `connect.Error.Details()`. The idempotency
  safety gate reads `Spec.IdempotencyLevel` from the Connect schema,
  not from URL path heuristics. The breaker emits Connect-typed errors
  with structured `RetryInfo` so retry composes without coupling. None
  of this is reachable from an HTTP-layer interceptor.
- **No retry / CB amplification.** Adopting resty alongside our Connect
  interceptors would stack retries (HTTP attempt × Connect attempt) and
  trip two breakers per failure. We documented the same anti-pattern
  for service-mesh + SDK in ADR 0002; it applies identically to HTTP
  client + Connect interceptors.
- **No bloat from features Connect does not use.** Digest auth, SSE,
  multipart, redirect handling, and load balancing are HTTP concerns;
  Connect carries its own streaming protocol, single-body serialisation,
  and a different auth posture. We do not pay for code paths we will
  never exercise.
- **Stable transitive dependency tree.** `net/http` is stdlib;
  `golang.org/x/oauth2` and `connectrpc.com/connect` are the only large
  transitives. resty v3 is in beta with active feature additions per
  release, which would mean tracking pre-1.0 churn in our supply chain.
- **CGO posture preserved.** Both compression libraries we add
  (`andybalholm/brotli`, `klauspost/compress/zstd`) are pure Go, so the
  SDK still builds cleanly with `CGO_ENABLED=0` and stays compatible
  with `GOFIPS140`.

### Negative

- We re-implement what resty already has for hedging, ratio-based CB,
  and cert hot-reload. Estimated cost: ~150 LoC each, well-bounded by
  Connect's interceptor surface, with cleaner tests because the inputs
  are typed Connect requests rather than arbitrary HTTP payloads.
- Contributors familiar with resty have to learn our interceptor API.
  Mitigated by the fact that our API is the standard `connect.Interceptor`
  shape; no Pinguteca-specific abstractions.
- We carry the maintenance burden for retry, CB, and (future) hedging.
  Resty's implementations are well-tested and battle-hardened; ours are
  in a smaller library with less production exposure. Mitigated by
  comprehensive tests, deterministic clock and jitter sources, and
  explicit revisit conditions in this ADR.

### Neutral

- The decision is reversible. The Connect interceptor stack and resty
  are not exclusive in principle: a future Connect-aware HTTP client
  could expose typed Connect state to outer layers, at which point we
  could selectively delegate (e.g. let resty handle redirect policy
  while keeping our retry).

## Alternatives considered

### Adopt resty v3 as the HTTPClient and thin our interceptors

Rejected. We would lose Connect-protocol awareness (idempotency-level
gate, RetryInfo cooperation, typed `connect.Error` extraction) without
gaining anything proportionate, since Connect SDKs do not need digest
auth, SSE, or multipart. The cost-to-value ratio inverts as soon as the
SDK scope is "Connect-only".

### Adopt resty AND keep Connect interceptors

Rejected. Two retry layers, two breakers, two compression layers.
Recovery-time and request amplification, identical to the mesh + SDK
anti-pattern in ADR 0002.

### Adopt req v3

Same layer-mismatch concerns as resty. req's HTTP/3 is attractive but
HTTP/3 is a transport choice that should be exposed independently of
the HTTPClient implementation; we plan a `transport/http3` sub-package
on `quic-go/http3` directly.

### Build a Connect-aware HTTP client wrapper

Rejected for v1. The Connect-Go interceptor pattern already gives us
typed access to Spec, Request, Response, and Error. Adding a custom
HTTPClient wrapper on top would duplicate state and complicate the
threading of context cancellation, deadlines, and trace propagation.

## Revisit when

- A Connect-aware Go HTTP client emerges (one that exposes typed Connect
  state to outer middleware). Today no such project exists; if one ships
  with mature CB, hedging, and load balancing, this ADR's cost/benefit
  inverts.
- Connect-Go itself ships first-party hedging, ratio CB, or load
  balancing. Then we can drop the corresponding sub-packages and
  delegate.
- Pinguteca starts shipping non-Connect HTTP-only SDKs. At that point
  resty/req become viable for the non-Connect surface; this ADR would
  then need a sibling explicitly about the HTTP-only path.
- Our re-implementation drift from resty's algorithms causes
  observable production issues (e.g. our ratio CB diverges from
  resty's well-tuned defaults). Then port directly from resty's source
  rather than from its public API.

## References

- [resty v3 source](https://github.com/go-resty/resty)
  including `circuit_breaker.go`, `hedging.go`, `cert_watcher.go`.
- [resty v3 beta-6 release notes](https://github.com/go-resty/resty/releases)
  detailing the ratio-based CB and sliding-window changes.
- [req v3](https://github.com/imroc/req)
- [Connect-Go interceptor docs](https://connectrpc.com/docs/go/interceptors)
- [google/oauth2](https://pkg.go.dev/golang.org/x/oauth2)
  the OAuth 2.0 lifecycle library that resty users wire into middleware
  and we wire into `auth.ClientCredentials`.
- [ADR 0002](./0002-resilience-and-mesh-coexistence.md) — same
  amplification reasoning applied to service-mesh CB.
