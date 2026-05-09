# ADR 0004: Compression strategy — Brotli default, Zstd alt, Gzip fallback

- **Status:** Accepted
- **Date:** 2026-05-09
- **Deciders:** Pinguteca SDK team
- **Affects:** `compression/`; every Connect client and handler that imports `compression.ClientOptions()` / `compression.HandlerOptions()`.

## Context

Connect-Go ships with Gzip support out of the box on both client and
handler sides. Brotli and Zstd require explicit registration via
`connect.WithAcceptCompression` (client) and `connect.WithCompression`
(handler), backed by user-provided `Compressor` and `Decompressor`
implementations.

Two questions:

1. **Which algorithms to register?** Brotli and Zstd are the two
   modern alternatives to Gzip; both improve compression ratio
   (Brotli for text, Zstd for binary) and decompression speed.
2. **Which to use by default for outbound requests?** Brotli has
   ~95%+ proxy/CDN acceptance (Cloudflare, Fastly, Akamai, AWS
   CloudFront, Google Cloud CDN, all major nginx/HAProxy/Envoy
   builds). Zstd is at ~70% acceptance: Cloudflare/Fastly/CloudFront
   shipped support in 2024, but enterprise on-prem proxies and many
   nginx deployments still lack it. Connect-Web clients in browsers
   negotiate based on Accept-Encoding regardless of default; the
   Send default only matters for requests that go uncompressed when
   the registered default's algorithm cannot be matched.

Library options:

- **Brotli:** No Go stdlib support. `github.com/andybalholm/brotli`
  is the de-facto pure-Go implementation, actively maintained
  (commits within ~3 weeks of this ADR, version 1.2.1 released
  2026). Pure Go, no CGO, compatible with `CGO_ENABLED=0` and
  `GOFIPS140` builds. The Google CGO bindings (google/brotli)
  would break our static-binary posture.
- **Zstd:** Stdlib has `internal/zstd` for `archive/tar`'s `.tar.zst`
  support but it is not exported. `github.com/klauspost/compress/zstd`
  is the de-facto pure-Go implementation (5.5k stars, daily
  commits, version 1.18.6 at the time of writing). DataDog's CGO
  bindings would break the same posture as Brotli's.
- **Gzip:** `compress/gzip` in stdlib. Connect-Go uses it by
  default. No third-party dep.

## Decision

Register **Brotli** and **Zstd** alongside Gzip on both client and
handler:

- `compression.ClientOptions()` returns `WithAcceptCompression` for
  Brotli and Zstd plus `WithSendCompression(NameBrotli)`. Connect-Go's
  default Gzip registration stays in place and remains available for
  servers that only support Gzip.
- `compression.HandlerOptions()` returns `WithCompression` for Brotli
  and Zstd. Gzip is left to Connect-Go's default.

**Brotli is the default Send compression** because it has the
highest realistic acceptance and the best ratio for the JSON and
text-encoded protobuf payloads typical in our SDK consumers'
traffic. Zstd is available via Accept-Encoding negotiation when both
ends support it; it remains opt-out from being the default until
its server-side acceptance approaches Brotli's.

**Brotli quality level is 4** (range 0..11). Level 4 gives ~90% of
the best-level ratio at a fraction of the CPU cost. The sweet spot
for streaming RPC traffic where compression and decompression
both happen on the request path.

**Zstd uses the library default** speed/ratio setting from
`zstd.NewWriter(io.Discard)` with no options. For high-throughput
servers we may revisit (`zstd.WithEncoderLevel(zstd.SpeedFastest)`),
but the default is well tuned for general use.

## Consequences

### Positive

- Three algorithms cover every realistic deployment matrix without
  requiring per-call configuration. Connect content negotiation
  picks the best mutually supported encoding.
- Pure-Go dependency tree — no CGO, no FIPS-mode complications.
- Defaulting to Brotli without removing Gzip means servers that
  cannot decode Brotli still get a working request path; they fall
  back through Accept-Encoding negotiation.

### Negative

- Two new third-party dependencies (`andybalholm/brotli`,
  `klauspost/compress/zstd`). Both are actively maintained and
  pure Go, but they grow our supply-chain surface.
- We re-implement small wrapper types around Brotli's and Zstd's
  decompressors to satisfy `connect.Decompressor`'s
  `Close() error` and `Reset(io.Reader) error` shapes (the
  upstream types differ slightly). ~50 LoC of glue, fully tested.
- Brotli quality level 4 is a fixed default. Some payloads might
  benefit from level 6 or higher. Future revisit: expose
  `compression.Config` if production traffic shows uneven results.

### Neutral

- Stdlib does not ship Brotli or Zstd today. Once it does (rumours
  of `compress/zstd` being exported alongside Brotli have circulated
  but neither has landed in Go 1.26), we revisit and drop the
  third-party dependencies.

## Alternatives considered

### Gzip-only (stdlib-only)

Rejected. Gzip is universally supported but produces ~20-30% larger
payloads than Brotli for JSON and text-encoded protobuf, with
slower decompression than Zstd. SDK consumers care about wire size
and request latency; gzip-only forfeits both.

### Zstd default

Rejected. Server-side acceptance is ~70% as of 2026-05; defaulting
to Zstd means a non-trivial fraction of consumers see uncompressed
requests when their server sits behind a proxy that does not
support Zstd. Brotli hits the 95%+ acceptance band that justifies
"compress everything" defaults.

### CGO bindings (google/brotli, DataDog/zstd)

Rejected. CGO breaks `CGO_ENABLED=0` static-binary builds and is
incompatible with the `GOFIPS140` build mode we target for FIPS
compliance.

### Skip Brotli, ship only Zstd as alt to Gzip

Rejected. Brotli's text-payload ratio still leads Zstd on the
JSON-heavy workloads our SDK consumers see most often; dropping it
would push consumers toward gzip on browsers (where Brotli has
shipped since 2017) and miss the ratio win.

## Revisit when

- Go stdlib exports `compress/brotli` or `compress/zstd`. Migrate to
  stdlib and drop the third-party dependencies.
- Production traffic shows the Brotli quality 4 default is a wrong
  trade-off for typical payloads. Make the level configurable via
  `compression.Config{BrotliQuality: int}`.
- Zstd server-side acceptance crosses 90%. Re-evaluate Send default;
  Zstd's decompression speed advantage matters more for high-RPS
  internal services than for browser-facing APIs.
- A new Connect-Go release ships first-class Brotli/Zstd
  registrations (so we can drop the wrappers entirely).
- A vulnerability is reported in `andybalholm/brotli` or
  `klauspost/compress`. Both are pinned via go.sum; bump
  immediately, snapshot in CHANGELOG.

## References

- [andybalholm/brotli](https://github.com/andybalholm/brotli) — pure Go Brotli, actively maintained.
- [klauspost/compress](https://github.com/klauspost/compress) — pure Go zstd, daily commits.
- [Connect-Go compression option docs](https://pkg.go.dev/connectrpc.com/connect#WithAcceptCompression)
- [Brotli RFC 7932](https://datatracker.ietf.org/doc/html/rfc7932)
- [Zstd RFC 8478](https://datatracker.ietf.org/doc/html/rfc8478)
- [Cloudflare zstd announcement (2024)](https://blog.cloudflare.com/new-standards/) for context on when zstd shipped at the major edges.
