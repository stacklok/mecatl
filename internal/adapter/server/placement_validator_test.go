package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
)

// validatingPlacementProvider allocates on Bind, so it opts into the
// side-effect-free PlacementValidator preflight.
type validatingPlacementProvider struct {
	revisionPlacementProvider
	validations int
	err         error
}

func (p *validatingPlacementProvider) ValidatePlacement(context.Context) error {
	p.validations++
	return p.err
}

func TestPlacementValidatorPreflightNeverBinds(t *testing.T) {
	for _, validation := range []error{nil, ErrPlacementUnavailable} {
		provider := &validatingPlacementProvider{err: validation}
		svc, err := NewService(Config{
			Engine: repairEngine(), Store: memstore.New(), PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/same-root",
		})
		if svc != nil {
			svc.Close()
		}
		if validation == nil && err != nil || validation != nil && !errors.Is(err, validation) {
			t.Fatalf("validation=%v startup error=%v", validation, err)
		}
		if provider.validations != 1 || provider.binds != 0 {
			t.Fatalf("validation=%v: validations=%d binds=%d, want one validation and no bind", validation, provider.validations, provider.binds)
		}
	}
}

// A provider without the seam keeps the legacy bind-to-validate preflight.
func TestPlacementPreflightBindsLegacyProviders(t *testing.T) {
	provider := &revisionPlacementProvider{}
	svc, err := NewService(Config{
		Engine: repairEngine(), Store: memstore.New(), PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/same-root",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.Close()
	if provider.binds != 1 {
		t.Fatalf("legacy preflight binds = %d, want 1", provider.binds)
	}
}
