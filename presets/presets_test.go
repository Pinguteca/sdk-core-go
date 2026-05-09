package presets_test

import (
	"testing"

	"github.com/Pinguteca/sdk-core-go/auth"
	"github.com/Pinguteca/sdk-core-go/presets"
)

func TestStandaloneIncludesEveryInterceptor(t *testing.T) {
	t.Parallel()
	ics, err := presets.Standalone(presets.StandaloneConfig{
		Auth: &auth.Options{Source: auth.StaticBearer("test-token")},
	})
	if err != nil {
		t.Fatalf("Standalone: %v", err)
	}
	// OTel + breaker + idempotency + retry + auth = 5 interceptors.
	if got := len(ics); got != 5 {
		t.Fatalf("Standalone interceptors = %d, want 5", got)
	}
}

func TestStandaloneSkipsAuthWhenNil(t *testing.T) {
	t.Parallel()
	ics, err := presets.Standalone(presets.StandaloneConfig{})
	if err != nil {
		t.Fatalf("Standalone: %v", err)
	}
	// No auth: 4 interceptors.
	if got := len(ics); got != 4 {
		t.Fatalf("Standalone (no auth) interceptors = %d, want 4", got)
	}
}

func TestMeshOmitsRetryAndBreaker(t *testing.T) {
	t.Parallel()
	ics, err := presets.Mesh(presets.MeshConfig{
		Auth: &auth.Options{Source: auth.StaticBearer("test-token")},
	})
	if err != nil {
		t.Fatalf("Mesh: %v", err)
	}
	// OTel + idempotency + auth = 3 interceptors.
	if got := len(ics); got != 3 {
		t.Fatalf("Mesh interceptors = %d, want 3", got)
	}
}

func TestStandaloneInvalidAuthReturnsError(t *testing.T) {
	t.Parallel()
	// Empty Source triggers auth.Interceptor to fail.
	_, err := presets.Standalone(presets.StandaloneConfig{
		Auth: &auth.Options{},
	})
	if err == nil {
		t.Fatalf("expected auth validation error")
	}
}
