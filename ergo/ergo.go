// Package ergo provides the primitive kit for the Layer 1.5
// ergonomic API contract pinned in
// sdk-scaffold/docs/rfc/0016-layer-1-5-ergonomic-api.md.
//
// This module ships the small set of building blocks that
// hand-curated L1.5 resource methods rely on: a composed-op
// orchestrator that derives per-leg idempotency keys and threads
// a correlation id, a long-running-operation poller, and the
// context helpers that interop with the Layer 2 interceptor chain.
//
// No service-specific resource code lives here. Resource methods
// (`client.Users.Create`, `client.Files.Upload`, ...) are written
// against this kit by each per-service SDK once the api-surface.yaml
// for that service exists.
//
// Stability: pre-release while Layer 1.5 takes shape. Tag releases
// as v0.x or v0.x-alpha until the primitive set proves stable
// across more than one consumer-facing service.
package ergo

// Version is the human-readable kit version. Useful for surface
// telemetry; not a substitute for the module's semver tag.
const Version = "0.0.1-alpha"
