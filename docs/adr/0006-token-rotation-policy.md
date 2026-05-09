# ADR 0006: Token rotation policy

- **Status:** Accepted
- **Date:** 2026-05-09
- **Deciders:** Pinguteca SDK team
- **Affects:** `auth` package (`RotatingTokenSource`, `RotationInterceptor`, `cachingOAuth2Source`)

## Context

OAuth 2.0 access tokens expire. The naive flow — `auth.Interceptor` calls
`TokenSource.Token(ctx)` per RPC and the underlying `oauth2.ReuseTokenSource`
refreshes on expiry — handles the *predictable* expiry case where the local
clock and the server's clock agree on validity. It does **not** handle the
race where:

1. The local cache says the token is still valid for 30 seconds.
2. The auth interceptor attaches it.
3. The server's clock skews ahead, or the IdP revokes the token mid-flight,
   and the server replies `Unauthenticated`.

The SDK observes a 401 with a still-cached "valid" token. Without a
rotation step the next call sees the same cached token and fails again
until natural expiry. With rotation, the SDK invalidates the cache,
re-fetches, and retries once.

Two implementation choices needed pinning down:

- **Where in the chain** — outside auth or inside retry?
- **Whether to retry at all** when the proto schema doesn't declare the RPC
  idempotent. The server may have already processed the original request
  before returning `Unauthenticated` (auth checks happening after mutations
  in poorly-implemented servers, or a race between request commit and
  response auth check). Retrying after rotation could double-mutate.

## Decision

1. **Add a `RotatingTokenSource` interface** that extends `TokenSource` with
   `Invalidate()`. Existing `TokenSource` consumers are unaffected; only
   token sources that opt in get the rotation behaviour.
2. **Rewrite `cachingOAuth2Source` to own its cache** instead of delegating
   to `oauth2.ReuseTokenSource`. The stdlib helper does not expose cache
   invalidation, which would have left rotation a no-op. Track the
   `*oauth2.Token` directly under a mutex; `Invalidate()` clears it.
3. **Ship `auth.RotationInterceptor`** as a separate interceptor, not a flag
   on `auth.Interceptor`. Keeps each interceptor's responsibility tight and
   lets users compose without rotation when their `TokenSource` is static.
4. **Composition order: outside auth, inside retry.** Canonical preset
   chain becomes:
   ```
   otel → breaker → idempotency → retry → rotation → auth → network
   ```
   Why inside retry: a successful rotation followed by a transient transport
   failure should still benefit from retry's backoff. Why outside auth: the
   rotation interceptor needs to wrap the call in which auth's token
   attachment happened, so the retry-after-invalidate sees the inner auth
   pull a fresh token.
5. **One-shot retry, never a loop.** A persistent `Unauthenticated` after
   rotation is bad credentials, not credential expiry. Looping would mask
   misconfiguration and amplify load.
6. **Idempotency safety gate enabled by default.** `RotationOptions.AllowNonIdempotent`
   defaults to false. When false, the interceptor skips rotation+retry if
   `Spec.IdempotencyLevel == connect.IdempotencyUnknown`. The original RPC
   may have been processed server-side before the 401 came back, and a
   blind retry could create a duplicate write. Callers wiring the
   idempotency-key interceptor with a server that deduplicates by key may
   set `AllowNonIdempotent: true`.
7. **Streaming RPCs pass through.** A stream cannot be replayed safely.

## Consequences

### Positive

- Token expiry no longer requires the consumer to wait for the natural
  refresh window.
- Static `TokenSource` implementations (e.g. `StaticBearer`) keep working
  unchanged because they are not required to satisfy `RotatingTokenSource`.
- The idempotency gate makes the dangerous case (retrying a CreateOrder
  after a 401) impossible by default.
- Rotation composes with retry without coupling: rotation handles credential
  expiry, retry handles transport failures; both can fire on the same call
  in their natural order.

### Negative

- The auth package now owns its OAuth 2.0 token cache instead of leaning on
  the stdlib's. The Token() implementation grew slightly and we are
  responsible for keeping it correct under concurrent reads.
- Custom `TokenSource` implementations that want rotation must implement
  `Invalidate()` themselves. Documentation has to flag this; future
  contributors might add a `TokenSource` and forget to wire `Invalidate`,
  silently disabling rotation.

### Neutral

- Bad-credentials and expired-credentials look identical on the wire (both
  surface as `Unauthenticated`). The interceptor cannot distinguish them
  before the rotation attempt; the cost is one extra RPC plus one IdP token
  fetch on misconfigured deployments. Acceptable.

## Alternatives considered

- **Rotate inside `auth.Interceptor`** via a `RetryOn401 bool` flag.
  Rejected: tangles two responsibilities (token attachment vs.
  rotation-on-401) and forces every consumer to opt out individually.
- **Rotate outside retry.** Rejected: a transient post-rotation network
  failure would not benefit from retry's backoff, and the user would see a
  bare error after a perfectly successful re-auth.
- **Loop rotation indefinitely.** Rejected: hides misconfiguration and
  amplifies load on the IdP.
- **Allow rotation for `IdempotencyUnknown` by default.** Rejected: the
  silent-double-mutation risk is too large for an opt-out default.

## Revisit when

- An IdP we adopt distinguishes "expired" from "revoked" via a header or
  body attribute. We could read that and skip rotation on revocation
  (where rotation helps zero, but a fresh fetch costs an IdP call).
- `oauth2.ReuseTokenSource` gains a public `Invalidate()`. We could revert
  the in-package cache rewrite to thin delegation.
- Connect-Go ships first-class auth-rotation primitives.
- A pattern emerges where multiple RPCs in a single call chain share a
  rotation event (today rotation is per-RPC; high-fanout consumers may
  prefer a request-scoped barrier).

## References

- ADR 0002 (resilience and mesh coexistence) — composition order rationale.
- `golang.org/x/oauth2` — token source semantics.
- RFC 6749 §5.2 — `invalid_token` error responses.
