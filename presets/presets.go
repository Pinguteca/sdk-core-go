// Package presets bundles common interceptor combinations for typical
// deployment contexts. Two presets ship out of the box:
//
//   - [Standalone]: full resilience stack for clients that own their network
//     posture (mobile apps, CLIs, third-party backend SDKs). Includes auth,
//     idempotency, retry, breaker, and OTel.
//   - [Mesh]: stack tailored for clients that run behind a service mesh
//     (Envoy/Linkerd/Cilium/Consul) where the data plane already does retry
//     and circuit breaking. Skips those interceptors to avoid amplification;
//     keeps auth, idempotency, and OTel because the mesh cannot generate
//     bearer tokens, idempotency keys, or correlate spans inside the SDK.
//
// Interceptor order matters. The presets emit them in the canonical
// outer-to-inner order so that:
//
//   - OTel is outermost: every other interceptor's work shows up under one
//     span per RPC.
//   - Breaker comes before retry: short-circuited calls do not consume retry
//     budget or generate idempotency keys.
//   - Idempotency comes before retry: the key is generated once and the same
//     header is replayed on every retry attempt.
//   - Auth is innermost: each retry attempt gets a fresh token from the
//     [auth.TokenSource] in case the previous attempt's token expired.
package presets

import (
	"fmt"

	"connectrpc.com/connect"

	"github.com/Pinguteca/sdk-core-go/auth"
	"github.com/Pinguteca/sdk-core-go/breaker"
	"github.com/Pinguteca/sdk-core-go/idempotency"
	"github.com/Pinguteca/sdk-core-go/otel"
	"github.com/Pinguteca/sdk-core-go/retry"
)

// StandaloneConfig parameterises the [Standalone] preset. Every field is
// optional; nil means "use the package default" except [Auth], which must be
// non-nil to enable credential injection. Pass nil for [Auth] when the
// service is unauthenticated.
type StandaloneConfig struct {
	Auth        *auth.Options
	Retry       *retry.Config
	Breaker     *breaker.Config
	Idempotency *idempotency.Options
	OTel        *otel.Options
}

// MeshConfig parameterises the [Mesh] preset. Same conventions as
// [StandaloneConfig]; retry and breaker are intentionally absent.
type MeshConfig struct {
	Auth        *auth.Options
	Idempotency *idempotency.Options
	OTel        *otel.Options
}

// Sizes of the interceptor slices each preset returns. Used to pre-size the
// allocation. Update if a preset gains or loses an interceptor.
const (
	standaloneInterceptorCount = 5 // OTel, Breaker, Idempotency, Retry, Auth
	meshInterceptorCount       = 3 // OTel, Idempotency, Auth
)

// Standalone returns the full-resilience interceptor stack. Wire the returned
// slice into [connect.WithInterceptors] when constructing your generated
// Connect client.
func Standalone(cfg StandaloneConfig) ([]connect.Interceptor, error) {
	ics := make([]connect.Interceptor, 0, standaloneInterceptorCount)

	ics = append(ics, otel.Interceptor(deref(cfg.OTel)))

	breakerCfg := breaker.DefaultConfig()
	if cfg.Breaker != nil {
		breakerCfg = *cfg.Breaker
	}
	ics = append(ics, breaker.Interceptor(breakerCfg))

	ics = append(ics, idempotency.Interceptor(deref(cfg.Idempotency)))

	retryCfg := retry.DefaultConfig()
	if cfg.Retry != nil {
		retryCfg = *cfg.Retry
	}
	ics = append(ics, retry.Interceptor(retryCfg))

	if cfg.Auth != nil {
		authIc, err := auth.Interceptor(*cfg.Auth)
		if err != nil {
			return nil, fmt.Errorf("presets.Standalone: %w", err)
		}
		ics = append(ics, authIc)
	}
	return ics, nil
}

// Mesh returns the trimmed interceptor stack for service-mesh deployments.
// Retry and breaker are deliberately omitted; the mesh data plane handles
// transport-level resilience.
func Mesh(cfg MeshConfig) ([]connect.Interceptor, error) {
	ics := make([]connect.Interceptor, 0, meshInterceptorCount)

	ics = append(ics, otel.Interceptor(deref(cfg.OTel)))
	ics = append(ics, idempotency.Interceptor(deref(cfg.Idempotency)))

	if cfg.Auth != nil {
		authIc, err := auth.Interceptor(*cfg.Auth)
		if err != nil {
			return nil, fmt.Errorf("presets.Mesh: %w", err)
		}
		ics = append(ics, authIc)
	}
	return ics, nil
}

// deref returns *p or the zero value of T when p is nil.
func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
