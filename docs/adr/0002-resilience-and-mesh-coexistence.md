# ADR 0002: Resilience interceptors and mesh coexistence

- **Status:** Accepted
- **Date:** 2026-05-09
- **Deciders:** Pinguteca SDK team
- **Affects:** `retry`, `breaker`, `idempotency`, `presets` packages

## Cross-SDK contract

The user-visible contract (two named presets, the
OTel-Breaker-Idempotency-Retry-Auth composition order, the Mesh
preset skipping Breaker and Retry to avoid amplification, the
breaker error model carrying a structured retry-after hint, and the
idempotency-driven retry safety gate) is pinned at the cross-SDK
level in `sdk-scaffold/docs/rfc/0008-resilience-presets.md`. Every
SDK must implement that RFC; this package is the Go-side
implementation.

## Context

Two questions had to be resolved together:

1. Should the SDK ship a circuit breaker at all? Service meshes (Envoy in
   Istio/Consul, Linkerd's data plane, Cilium's eBPF redirector) already
   break circuits and retry at the data plane. Wiring those again in the SDK
   risks **amplification**: failure_recovery_time = mesh_open + sdk_open;
   total_attempts = mesh_retries × sdk_retries.

2. SDK consumers split into two populations with opposite needs:
   - **Mesh-resident services** (microservices behind a sidecar) — every
     transport-level concern is already handled by the mesh; SDK CB/retry is
     pure noise.
   - **External consumers** (mobile clients, CLIs, third-party backends,
     edge workers) — there is no mesh; SDK CB/retry is essential.

The SDK serves both populations from the same module, so leaving the
question implicit would push every consumer into the same foot-gun: either
double-CB or no-CB depending on which copy/paste they found first.

A related concern: retrying mutating RPCs is unsafe unless the server
deduplicates by idempotency key. The retry interceptor must default-protect
against this; the idempotency interceptor cooperates so that opting in is
explicit.

## Decision

1. **Ship the breaker package.** It is required for the external-consumer
   population and a no-op for mesh-resident services that simply do not wire
   it. Owning the implementation (rather than depending on `sony/gobreaker`
   or similar) keeps the dependency tree small and lets us tune defaults to
   Connect-specific signals (`google.rpc.RetryInfo` cooperation with the
   retry interceptor).

2. **Open-state errors carry `RetryInfo`.** When the breaker short-circuits
   it returns `connect.CodeUnavailable` with a `google.rpc.RetryInfo` detail
   advertising the remaining open duration. The retry interceptor reads
   `RetryInfo` and waits the suggested cooldown, so retry composes with
   breaker without the user wiring anything special.

3. **Two presets bundle the canonical compositions:**
   - `presets.Standalone(...)` → OTel, Breaker, Idempotency, Retry, Auth.
   - `presets.Mesh(...)` → OTel, Idempotency, Auth.

   Naming favours the deployment context the SDK *expects* (does it run by
   itself, or does it run inside a mesh?). Cilium and other proxyless meshes
   are still "mesh" for our purposes — the choice keys on whether the
   surrounding infra does retry/CB, not on whether a sidecar is present.

4. **Interceptor composition order is fixed by the preset:**

   ```
   outermost → innermost
   OTel  →  Breaker  →  Idempotency  →  Retry  →  Auth
   ```

   - OTel outermost: every other interceptor's work shows up under one span.
   - Breaker before retry: short-circuited calls do not consume retry
     budget or generate idempotency keys.
   - Idempotency before retry: the key is generated once on the first
     attempt and the same `Idempotency-Key` header is replayed on every
     retry attempt because retry reuses the same `connect.Request`.
   - Auth innermost: each retry attempt re-runs the auth interceptor, which
     calls `TokenSource.Token` and refreshes if needed. A token that
     expired between attempts is replaced before the next network call.

5. **Retry safety gate.** `retry.Config.AllowNonIdempotent` defaults to
   `false`. When false (the safe default), the retry loop skips entirely if
   `req.Spec().IdempotencyLevel == connect.IdempotencyUnknown`. The schema
   author opts a method into retry by adding
   `option idempotency_level = IDEMPOTENT;` (or `NO_SIDE_EFFECTS`).
   Callers who pair retry with the idempotency-key interceptor and a
   server that deduplicates by key may flip the gate off.

## Consequences

### Positive

- Mesh-resident consumers cannot accidentally double-CB; they see a preset
  named after their topology and are guided away from the wrong choice.
- External consumers get a one-call setup (`presets.Standalone`) that wires
  the full resilience stack in the canonical order.
- Breaker errors carry `RetryInfo`, which means the retry layer needs no
  special knowledge of the breaker. Cooperative composition without
  coupling.
- The retry safety gate makes the dangerous case (retrying a CreateOrder
  RPC and double-charging) impossible by default. Schema authors are
  forced to mark idempotency explicitly.

### Negative

- Two preset names for users to learn; some confusion possible at the
  boundary (e.g. a non-mesh internal service running on bare VMs may not
  fit either name cleanly). Mitigation: the preset doc comments are
  explicit about *what each one does and does not include* so callers can
  always handcraft the slice if neither preset fits.
- The breaker is per-interceptor-instance, not per-target-host. A consumer
  with multiple Connect clients to different services needs to wire one
  breaker per client. This matches the typical case (one client per
  service) and avoids runtime introspection of `req.Peer().Addr`. If we
  ever need per-host breakers, that becomes a separate
  `breaker/perhost` sub-package.
- Schemas without idempotency annotations get `Unknown`, which means no
  retries by default. Some legacy `.proto` files will need a one-line
  patch.

### Neutral

- We do *not* ship a client-side retry-throttling budget yet (gRPC's
  `retry_throttling`: stop retrying when retry-to-success ratio exceeds a
  threshold). Listed in **Revisit when** below.

## Revisit when

- A measurable fraction of consumers report retry amplification storms
  (typically: aggregate retry budget exhausted faster than expected). At
  that point, add a per-client retry throttler (gRPC-style ratio threshold).
- A new mesh implementation without per-client circuit breaking emerges and
  becomes popular. Today every major mesh does CB; if that changes, the
  Mesh preset may need an opt-in CB knob.
- Connect-Go gains a way to inspect the resolved peer address from inside
  an interceptor cheaply enough to make per-host breakers viable. Today
  `req.Peer().Addr` is set late and shifts across attempts.
- The user community asks for `presets.Bare` (or similar) — interceptors
  with no defaults applied — because their consumer mix is too irregular
  for either Standalone or Mesh.

## References

- [Envoy circuit breaking](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/upstream/circuit_breaking)
- [Linkerd retries and CB](https://linkerd.io/2/features/retries-and-timeouts/)
- [gRPC retry policy and throttling](https://github.com/grpc/proposal/blob/master/A6-client-retries.md)
- [Microsoft Azure Architecture: Circuit Breaker](https://learn.microsoft.com/en-us/azure/architecture/patterns/circuit-breaker)
- [Connect protocol error model](https://connectrpc.com/docs/protocol#error-codes)
