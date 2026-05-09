# ADR 0001: Cryptographic RNG and jitter strategy

- **Status:** Accepted
- **Date:** 2026-05-09
- **Deciders:** Pinguteca SDK team
- **Affects:** `retry` package (and any future package that needs randomness)

## Context

The retry interceptor needs randomness for two related concerns:

1. **Jitter** — perturb each backoff so concurrent clients do not synchronise into retry storms.
2. **Future SDK-Core needs** — idempotency keys (UUIDv7 already uses crypto-quality entropy), hedging delays, sampling decisions, etc.

Two orthogonal questions:

- **Which RNG?** `math/rand`, `math/rand/v2`, `crypto/rand`, or a third-party package.
- **Which jitter formula?** No-jitter, proportional, equal, full, or decorrelated.

### Compliance constraint

Pinguteca SDKs target environments that may be audited under FIPS 140-3 or equivalent. A FIPS audit fails when *any* randomness in the binary comes from a non-validated module — including "non-security" use cases like jitter. Go 1.24+ ships a validated module that `crypto/rand` automatically routes through under `GOFIPS140=v1.0.0`. `math/rand` does not, and never will.

### Jitter trade-off

The AWS Architecture Blog ("Exponential Backoff And Jitter", Brooker 2015) measured the desync quality of common jitter schemes. Ranked best to worst at avoiding retry storms:

1. **Full jitter:** `delay = rand(0, min(cap, base * mult^N))`
2. **Decorrelated jitter:** `delay = rand(initial, min(cap, prev * factor))`
3. Equal jitter
4. Proportional jitter
5. No jitter

Equal and proportional jitter offer no advantage over full and decorrelated; they only complicate the API surface. Full jitter is the industry default (gRPC-Go, AWS SDKs, Google SRE book). Decorrelated is the right choice when sustained load makes the attempt counter meaningless because every attempt is "attempt 1 again."

## Decision

1. **All randomness in this library reads from `crypto/rand`.** No `math/rand`, no `math/rand/v2`, anywhere.

2. **Jitter is exposed as a pluggable hook:** `Config.JitterSource func() float64` returning a uniform value in `[0, 1)`. The default `DefaultJitterSource` reads 8 bytes from `crypto/rand`, takes the top 53 bits (IEEE 754 double-precision significand width), and divides by `1 << 53` for an unbiased uniform conversion. If the kernel CSPRNG fails (essentially never on Linux/macOS/Windows), it returns `0.5` — degrading to mid-jitter is acceptable for backoff and avoids panicking on boot-time entropy starvation.

3. **Two jitter strategies are supported,** selected via `Config.Strategy`:
   - `StrategyFull` (default) — AWS full jitter.
   - `StrategyDecorrelated` — AWS decorrelated jitter, parameterised by `DecorrelationFactor` (default 3.0).

   Equal and proportional jitter are intentionally **not** offered.

4. **No `nolint` suppressions** are used to silence `gosec G404`. Removing `math/rand` removes the warning.

## Consequences

### Positive

- `GOFIPS140=v1.0.0` builds work without code change.
- One RNG entry point (`crypto/rand.Read`) for the entire library; easy for auditors to grep.
- Jitter strategy is clearly named, no magic numbers in the API.
- Tests inject deterministic `JitterSource` for reproducibility without coupling to either rand package.

### Negative

- ~500ns per `crypto/rand.Read` vs ~10ns for `math/rand.Float64`. Irrelevant for retry (we are about to sleep for milliseconds). Could matter if jitter is reused for hot-path sampling later — at which point we would revisit with a measured benchmark.
- 8 bytes per call is more than the 53 bits we use. Acceptable; reading less is fiddly and saves nothing on the kernel side.

### Neutral

- Callers who want non-FIPS-grade speed can supply their own `JitterSource`; the library does not police the choice. This is a deliberate escape hatch.

## Revisit when

- Go stdlib adds `crypto/rand.Float64` or `crypto/rand.Uint64` helpers (proposed for some 1.x release post-1.26). Migrate `DefaultJitterSource` to call them.
- Real-world telemetry shows that decorrelated jitter would be a better default for our workload. Switch `DefaultStrategy`.
- A consumer reports FIPS audit failure traceable to this library. Investigate; the answer is "all RNG already routes through `crypto/rand`," but verify under their specific build.

## References

- [AWS Architecture Blog: Exponential Backoff And Jitter (Brooker, 2015)](https://aws.amazon.com/blogs/architecture/exponential-backoff-and-jitter/)
- [AWS Builders' Library: Timeouts, retries, and backoff with jitter](https://aws.amazon.com/builders-library/timeouts-retries-and-backoff-with-jitter/)
- [Go release notes: crypto/rand FIPS 140-3 module (1.24)](https://go.dev/doc/go1.24)
- [Google SRE Book, ch. "Addressing Cascading Failures"](https://sre.google/sre-book/addressing-cascading-failures/)
