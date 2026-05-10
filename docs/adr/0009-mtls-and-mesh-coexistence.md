# ADR 0009: mTLS helper and mesh coexistence

- **Status:** Accepted
- **Date:** 2026-05-10
- **Deciders:** Pinguteca SDK team
- **Affects:** new `transport/mtls` package; `presets` documentation.

## Context

Service-to-service mTLS is a standard ask. Same split as ADR 0002:

- **Mesh-resident services**: sidecar/eBPF already handles mTLS. SDK
  mTLS duplicates or breaks the handshake.
- **Standalone consumers**: no sidecar. SDK mTLS is the only way to
  present a client identity.

Without a helper, standalone consumers hand-roll `*tls.Config` and tend
to forget `MinVersion`, root pools, or SAN handling. With a helper, mesh
consumers may wire it by accident.

## Decision

1. **Ship `transport/mtls` as a transport-layer helper.** Builder
   returns `*tls.Config` plus a `Transport(...)` convenience that
   returns `*http.Transport`. Caller plugs it into `*http.Client` and
   passes that to `connect.NewClient`. Not an interceptor; TLS
   negotiation belongs below the Connect layer.

2. **Defaults:**
   - `MinVersion: tls.VersionTLS13`. Override via `Options.MinVersion`.
   - System root pool, plus optional `CACertPath` for private CAs.
   - `ServerName` derived by stdlib from the request URL host. Not
     overridable.
   - `InsecureSkipVerify: true` rejected at builder time.

3. **PEM and PKCS#12 supported.** Two constructors:
   - `Config(certPath, keyPath, caCertPath, opts)` for PEM.
   - `ConfigFromP12(p12Path, password, caCertPath, opts)` for PKCS#12.

   PKCS#12 is common in Java/Windows pipelines and in some
   cert-manager outputs; including it now is cheaper than turning it
   away later.

4. **Mesh awareness lives in preset docs, not in the helper.** No
   auto-detection (envvar/port sniffing is fragile). Preset docs:
   - `presets.Standalone(...)` notes how to wire `transport/mtls`.
   - `presets.Mesh(...)` notes that the sidecar handles client identity
     and the SDK should not wire mTLS.

5. **No hot-reload in v1.** Certs load once at construction. Rotation
   ships later as `transport/mtls.Watcher`, borrowing resty's
   `cert_watcher.go` shape (already on the ADR 0003 list).

6. **No SPIFFE/SPIRE bindings in v1.** Add `transport/mtls/spiffe` if a
   non-mesh consumer needs it.

7. **Strict failure on misconfiguration.** Missing files, mismatched
   key, or `InsecureSkipVerify: true` return an error.

## Consequences

### Positive

- Standalone setup is one call with safe defaults.
- Mesh consumers get a documented warning instead of a silent foot-gun.
- Helper is transport-layer, so it composes with every interceptor with
  no ordering surprises.

### Negative

- A misconfigured deploy that wires `transport/mtls` inside a mesh fails
  at handshake time. Looks like a cert error, is a topology error.
- Long-lived clients with short-lived certs must restart until the
  watcher ships.

### Neutral

- No connection probe at construction. The first RPC surfaces TLS
  errors.

## Alternatives considered

- **Auto-detect mesh and silently no-op mTLS.** Detection is unreliable
  and silent no-op hides deploy bugs.
- **mTLS as an interceptor.** TLS happens below the Connect layer; an
  interceptor cannot influence the handshake.
- **Hot-reload in v1.** fsnotify dependency, atomic-replace and
  Kubernetes secret-mount semantics deserve their own pass.
- **TLS 1.2 default.** Known weaker; require explicit opt-in.
- **Allow `InsecureSkipVerify: true`.** Tests that need it can build
  `*tls.Config` directly without this helper.
- **PEM only.** PKCS#12 is standard enough across Java and Windows
  ecosystems that excluding it would push consumers to bypass the
  helper.

## Revisit when

- A consumer hits cert-rotation pain. Ship `transport/mtls/Watcher`.
- A non-mesh SPIFFE consumer surfaces. Add `transport/mtls/spiffe`.
- TPM/HSM-backed keys become a stated requirement.
- A mesh implementation expects application-layer mTLS. Update preset
  docs.

## References

- ADR 0002 (resilience and mesh coexistence).
- ADR 0003 (HTTP client choice): borrow list including resty's
  `cert_watcher.go`.
- [Istio mutual TLS](https://istio.io/latest/docs/concepts/security/#mutual-tls-authentication)
- [Linkerd automatic mTLS](https://linkerd.io/2/features/automatic-mtls/)
- [SPIFFE & SPIRE](https://spiffe.io/)
- Go `crypto/tls` package: `Config` and `MinVersion` semantics.
- Go `software.sslmate.com/src/go-pkcs12`: likely PKCS#12 dependency.
