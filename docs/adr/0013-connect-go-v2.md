# ADR 0013: Move to connect-go v2

- **Status:** Accepted
- **Date:** 2026-10-12
- **Deciders:** SDK team
- **Affects:** every package importing `connectrpc.com/connect`: auth,
  breaker, compression, errors, hedge, idempotency, logging, otel,
  presets, retry, timeout. The L2 allow list
  (`tools/check-l2-deps/allowlist.txt`).
- **Amends:** ADR 0005 for how hedge isolates attempts. Its placement
  inside the interceptor chain stands.

## Context

connect-go v2.0.0 replaces the v1 interceptor model. In v1 a unary
interceptor receives a single request value that the library writes to
while sending, and the interface is sealed so an interceptor cannot copy
it. Hedging needs one independent request per concurrent attempt, which
v1 cannot provide at the interceptor layer.

v2 removes the request value. An interceptor is a function that wraps
the opening of a stream, messages travel through the stream, and
request metadata lives on a per-call `CallInfo` carried by the context.
`connect.NewClientContext` attaches a fresh one, and the library
documents deriving a separate context per concurrent call.

`experimental/hedgev2` tested this. Each attempt gets its own
`CallInfo`, request header copy and request message copy. Under
`go test -race`, a fan-out across concurrent callers with a
header-writing interceptor inside it ran clean, while a variant whose
attempts share the caller's `CallInfo` failed the same test with data
races. The design holds on v2 and the test can tell it apart from the
broken one.

v2's migration tool rewrites import paths, client and server
construction, and ecosystem interceptors. It does not rewrite custom
interceptors, and every interceptor this SDK ships is custom, so the
port is hand work.

## Decision

1. **Port every connect-importing package to connect-go v2.** v1 and v2
   can be imported in one module, so packages move one at a time, one
   PR each. Order: errors first (shared helpers), then the leaf
   interceptors (timeout, idempotency, retry, breaker, auth, logging,
   otel, compression), then hedge, then presets, which wires the
   others.
2. **Expose the v2 interceptor types.** Each package returns
   `connect.ClientInterceptor`, plus a `connect.ServerInterceptor` where
   it has server-side behaviour today (otel, logging, compression).
3. **Hedge stays an interceptor.** Each attempt derives its own context
   with `connect.NewClientContext` and receives copies of the request
   headers and message. `experimental/hedgev2` is the reference; it is
   promoted into `hedge` when that package ports, and the experimental
   module is deleted in the same PR.
4. **Per-call stream wrappers may keep the call context.**
   `ClientStream` methods take no context, so a wrapper that does work
   after `Send` keeps the one it was opened with, as connect-go's own
   streams do. Such fields carry `//nolint:containedctx` with a reason.
   The rule stays enabled so a long-lived struct holding a context is
   still caught.
5. **Allow list.** Add `connectrpc.com/connect/v2` alongside the v1
   entry. Remove the v1 entry with the last ported package.
6. **Releases.** All modules are pre-1.0, so each port ships as a minor
   bump with no import path change. The changelog for each states that
   consumers move to connect-go v2 with it.
7. **Until hedge ports,** its package documentation states that it must
   not be composed with interceptors that set request headers.

## Consequences

- Consumers upgrade to connect-go v2 in step with the SDK. Mixing a v1
  client with ported v2 interceptors does not compile.
- v2 bounds read sizes by default where v1 did not. Ported packages
  inherit that behaviour change and their tests cover it.
- During the port the repo depends on both v1 and v2.
- v2.0.0 is a fresh major release; early patch releases are likely and
  Renovate will carry them.

## Unresolved

- Whether presets ports in one step or keeps a v1 variant for a release
  so consumers can stage their own migration.
