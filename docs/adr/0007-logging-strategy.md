# ADR 0007: Logging strategy — canonical logs, wide events, slog

- **Status:** Accepted
- **Date:** 2026-05-09
- **Deciders:** Pinguteca SDK team
- **Affects:** `logging` package; documentation around the OTel boundary.

## Context

Two questions had to be answered for the SDK's logging story:

1. **What gets logged per RPC?** The naive default is "every interesting
   step": auth attached, retry attempt N, breaker state changed, response
   received, etc. This produces N log lines per RPC. At any non-trivial QPS
   the signal-to-noise ratio collapses; investigating a single failed
   request means filtering across many lines that share no obvious link
   beyond a thin correlation ID.
2. **Which library?** The Go ecosystem split between `zap`, `zerolog`,
   `logrus`, and stdlib `log/slog`. Picking one for the SDK forces every
   consumer to either adopt it or shim around it.

The constraint we accepted: this SDK is consumed by services that already
have their own observability stack. We must not mandate a specific log
backend, but the *shape* of what we emit should be opinionated enough to
plug into modern wide-event log stores (Honeycomb, Axiom, Datadog Logs,
Elastic, OpenSearch, ClickHouse, etc.) without per-consumer remapping.

## Decision

1. **Adopt the canonical-log-and-wide-event pattern**
   ([loggingsucks.com](https://loggingsucks.com/)). Emit **one** structured
   record per RPC at completion. The record carries every attribute a
   responder needs — method, duration, status code, request id, trace and
   span ids, custom caller-supplied attrs. No per-step debug spam from the
   interceptor.

2. **Use stdlib `log/slog`.** Three reasons:
   - Stdlib means zero new dependencies and a stable, versioned API.
   - `slog.Handler` is the contract; the consumer plugs whatever backend
     they want (`slog.JSONHandler`, OTel-native `slog.Handler`, custom
     stdriver, etc.) and the SDK does not care.
   - Migration cost from `zap`/`zerolog` is low for consumers because slog
     is the default direction for new Go code.

3. **Boundary with OTel.** Tracing is the *causal* graph (parent-child
   spans, durations, attributes per step). Logging is the *event* store
   (one row per RPC, indexable by every attribute). They join via
   `trace.id`/`span.id`. The interceptor writes both and lets the consumer
   correlate downstream. We do not duplicate per-step span events into
   logs.

4. **Default redaction posture.** Headers like `Authorization`, `Cookie`,
   `Set-Cookie`, `Proxy-Authorization`, and `X-Api-Key` are masked when
   `LogHeaders` is enabled. Header logging itself is opt-in (`LogHeaders:
   true`) because the headers blob bloats every record and offers little
   value once a request id is present.

5. **Caller hooks.** Two function fields, `AddRequestAttrs` and
   `AddResponseAttrs`, let consumers inject business-domain attrs (e.g.
   `tenant_id`, `actor_id`, `entity_kind`) without forking the interceptor.
   These run pre/post-call; their output is appended to the canonical
   record, not emitted as separate log lines.

6. **Streaming RPCs pass through.** Per-message logging contradicts the
   one-record-per-RPC model. The streaming posture is the subject of
   ADR 0009 (deferred); until then, a single canonical record per stream
   open/close is the right next step but not yet implemented.

## Consequences

### Positive

- One record per RPC means one row in any log backend that lets you query
  by attribute (`rpc.method`, `rpc.code`, `request.id`, `trace.id`).
  Investigation is "find the row" instead of "reconstruct from N lines".
- Stdlib `log/slog` has zero dependency cost and consumers can swap
  handlers freely.
- Trace/span correlation is automatic when OTel is wired, and silently
  absent when it is not.

### Negative

- Per-step debug logs from the interceptor itself are gone. Consumers who
  expected to grep for "retrying attempt 2" inside the SDK won't find it
  in logs — they have to inspect spans (which OTel records do represent).
  Net win in production, mild friction during initial integration.
- Wide records get large. A canonical row with caller attrs, headers (when
  enabled), error messages, and trace ids can easily exceed 1 KB. Most
  modern log backends absorb this fine; older syslog-style pipelines may
  truncate. We document the recommendation: use a wide-event-friendly
  backend.
- Custom redaction lists must be configured per consumer; the defaults
  cover the obvious headers but not e.g. `X-Tenant-Token` or other
  vendor-specific names.

### Neutral

- We do not provide a default `slog.Logger` instance. Constructor returns
  an error if `Config.Logger` is nil. Consumer must wire one.

## Alternatives considered

- **Zap or zerolog.** Both faster than slog in microbenchmarks. Rejected:
  performance gap is irrelevant at typical SDK call rates, and either
  would force consumers to adopt the same library for the rest of their
  codebase. slog is the lowest-common-denominator choice that still
  supports structured handlers.
- **Per-step interceptor logs (debug level on, info default).** Rejected:
  even off, the call-site bookkeeping clutters the codebase, and turning
  it on in production is a foot-gun (volume can spike 10x).
- **OTel logs only (no slog).** Rejected: OTel logs are an emerging
  standard; backend support varies. slog over a `slog.Handler` that bridges
  to OTel logs is strictly more flexible — consumers who want pure OTel
  logs plug `otelslog` and we are agnostic.
- **Roll our own logger interface.** Rejected: every Go SDK that did this
  in the last decade ended up reinventing slog poorly.

## Revisit when

- OTel logs become the de-facto Go standard with stable backend support.
  At that point we may register a default OTel-bridging `slog.Handler` to
  reduce wiring boilerplate.
- ADR 0009 (streaming posture) lands. The streaming case may need
  per-message canonical records or a streaming-aware variant of this
  interceptor.
- Pinguteca picks a primary log backend and we want to ship pre-shaped
  attrs (e.g. Honeycomb's `app.*` namespace, Datadog's `dd.*`, etc.).
  Today the attrs follow OTel semantic conventions (`rpc.system`,
  `rpc.service`, `rpc.method`, `rpc.code`).

## References

- [https://loggingsucks.com/](https://loggingsucks.com/) — canonical logs
  and wide events.
- [Stripe's "canonical log lines"](https://stripe.com/blog/canonical-log-lines) —
  the pattern's original public articulation.
- OTel semantic conventions for RPC.
- `log/slog` Go stdlib docs.
- ADR 0002 (resilience and mesh coexistence) — interceptor composition order.
