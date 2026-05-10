# ADR 0010: Multi-module repository split

- **Status:** Accepted
- **Date:** 2026-05-10
- **Deciders:** SDK team
- **Affects:** every package in this repo; release tagging convention.
- **Implements:** RFC 0002 (layered SDK architecture) for Go.

## Context

RFC 0002 fixes the layer model for every SDK: Layer 2 ships in the core
module with stdlib-only deps; Layer 3 ships as companion sub-modules.
Today this repo is a single Go module with every package, every
third-party dependency landing in every consumer's `go.sum` whether or
not they use the package that needs it.

`go.opentelemetry.io/otel/*`, `andybalholm/brotli`,
`klauspost/compress`, and `software.sslmate.com/src/go-pkcs12` are
pulled by every consumer of the SDK even when they only want, say,
retry and auth.

Go modules natively support nested modules: a sub-directory with its
own `go.mod` is an independent publication unit. AWS SDK Go v2,
OpenTelemetry Go, and the `golang.org/x/*` modules all use this shape.

## Decision

1. **Convert this repo to a multi-module mono-repo.** Root `go.mod`
   keeps Layer 2 packages (stdlib plus `golang.org/x/*` only).
   Layer 3 packages each get their own `go.mod` in their existing
   directory.

2. **Layer 2 stays in root module:**
   - `retry/`
   - `idempotency/`
   - `auth/` (`golang.org/x/oauth2` is on the RFC 0002 allow list)
   - `pagination/`
   - `errors/`
   - `clock/`
   - `presets/`
   - `transport/mtls/` (PEM path only after the split below)

3. **Layer 3 moves to sub-modules** (each with its own `go.mod`):
   - `otel/` - depends on `go.opentelemetry.io/otel/*`
   - `logging/` - slog adapter, depends on nothing extra today
     but is L3 per RFC 0002 (every SDK adapts to its ecosystem
     logger; for Go, that is `log/slog`)
   - `compression/` - depends on `andybalholm/brotli` and
     `klauspost/compress`
   - `breaker/` - no extra 3P deps, but L3 per RFC 0002 (cross-SDK
     consistency: every SDK's breaker is a companion)
   - `hedge/` - L3 (Nice tier per RFC 0001)
   - `transport/mtls/pkcs12/` - new sub-package, depends on
     `software.sslmate.com/src/go-pkcs12`. The PEM constructor stays
     in `transport/mtls/`; only `ConfigFromP12` moves.

4. **Module path convention.** Each sub-module is
   `github.com/Pinguteca/sdk-core-go/<dir>`. Companion code imports
   the core module (`require github.com/Pinguteca/sdk-core-go vX.Y.Z`),
   never the reverse.

5. **Tag convention.** Sub-module releases tag as `<dir>/vX.Y.Z`. Root
   module releases tag as `vX.Y.Z`. Same semver discipline; release
   notes cross-link.

6. **Local development uses `go.work`.** A workspace file at the repo
   root lists all modules so editors and `go test ./...` work without
   relying on published versions.

7. **Migration is incremental, one component per commit.** Each
   component's split is its own atomic change so bisect stays clean.

## Consequences

### Positive

- Consumer of `sdk-core-go` proper pulls only stdlib plus
  `golang.org/x/oauth2`. Build size and audit surface drop.
- Companion consumers opt in: `go get .../sdk-core-go/otel` is the
  only way to pull OTel deps.
- One repo, one CI, one issue tracker. Maintenance unit unchanged.
- Behavioural parity contract (Layer 2) is now a separate module
  that companions across other languages can mirror exactly.

### Negative

- Multi-module tagging is fiddlier than single-module. `git tag
  otel/v1.0.0` semantics need to be encoded in release tooling
  (cog.toml, scheduled-release workflow).
- `transport/mtls/` API breaks: callers using `ConfigFromP12` must
  switch to the new sub-package import. This is pre-1.0 so the
  break is acceptable.
- Contributors learn the workspace flow. `go.work` plus per-module
  `go test` is mostly transparent but unfamiliar to some.

### Neutral

- The companion adapter contract (Layer 2 types that companions
  depend on) lives in the core module for now. RFC 0002 left the
  question of a separate `contract/` sub-module open; we revisit
  if companion stability constraints start blocking core changes.

## Alternatives considered

- **Stay single-module, accept the dep bloat.** Simpler tooling,
  but every consumer pays for every dep. Fails the supply-chain
  audit case.
- **One repo per companion.** Five-plus extra repos for Go alone,
  forty-plus across all Primary languages. Maintenance unit too
  large.
- **Build tags or conditional compilation.** Does not solve it:
  `go.mod` deps are module-level, not file-level.

## Revisit when

- A contributor or release tool stumbles on tag conventions.
  Document the convention as part of the migration if needed.
- The Layer 2 / companion coupling forces a breaking core change
  that cascades through every companion. At that point split out
  `contract/` as its own sub-module.
- Go modules add a feature that obviates the sub-module pattern
  (extremely unlikely; the pattern is now load-bearing for the
  ecosystem).

## References

- RFC 0001 (parity baseline) and RFC 0002 (layered architecture).
- AWS SDK Go v2 multi-module layout: https://github.com/aws/aws-sdk-go-v2
- OpenTelemetry Go component modules:
  https://github.com/open-telemetry/opentelemetry-go
- Go modules reference (nested modules):
  https://go.dev/ref/mod#modules-overview
