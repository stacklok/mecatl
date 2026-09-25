package app

import (
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/providercatalog"
)

func TestCatalogMetadataAllocations(t *testing.T) {
	p, ok := providercatalog.Default().Provider(providerOpenRouter)
	if !ok {
		t.Fatal("openrouter missing from catalog")
	}
	models := p.Models()
	if len(models) == 0 {
		t.Fatal("openrouter models missing from catalog")
	}
	model := models[len(models)/2]
	for _, tc := range []struct {
		name   string
		max    float64
		lookup func() bool
	}{
		{"context/hit", 0, func() bool { return catalogContextWindow(providerOpenRouter, model.ID()) == model.ContextLimit() }},
		{"context/miss", 0, func() bool { return catalogContextWindow(providerOpenRouter, "test/uncatalogued-model") == 0 }},
		// A modality hit retains the defensive InputModalities slice copy.
		{"modalities/hit", 1, func() bool {
			_, _, found := catalogModalities(providerOpenRouter, model.ID())
			return found
		}},
		{"modalities/miss", 0, func() bool {
			image, audio, found := catalogModalities(providerOpenRouter, "test/uncatalogued-model")
			return !image && !audio && !found
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			valid := true
			allocs := testing.AllocsPerRun(100, func() {
				if !tc.lookup() {
					valid = false
				}
			})
			if !valid {
				t.Fatal("catalogue lookup returned unexpected metadata")
			}
			if allocs > tc.max {
				t.Fatalf("allocations = %v, want <= %v (no full catalogue copy)", allocs, tc.max)
			}
		})
	}
}

func BenchmarkCatalogMetadata(b *testing.B) {
	p, ok := providercatalog.Default().Provider(providerOpenRouter)
	if !ok {
		b.Fatal("openrouter missing from catalog")
	}
	models := p.Models()
	if len(models) == 0 {
		b.Fatal("openrouter models missing from catalog")
	}
	hit := models[len(models)/2].ID()
	const miss = "benchmark/uncatalogued-model"
	if got := catalogContextWindow(providerOpenRouter, hit); got == 0 {
		b.Fatalf("catalogContextWindow(%q) = 0, want catalogued model context", hit)
	}
	if image, audio, found := catalogModalities(providerOpenRouter, hit); !found {
		b.Fatalf("catalogModalities(%q) = (%t, %t, %t), want found", hit, image, audio, found)
	}
	if got := catalogContextWindow(providerOpenRouter, miss); got != 0 {
		b.Fatalf("catalogContextWindow miss = %d, want 0", got)
	}
	if image, audio, found := catalogModalities(providerOpenRouter, miss); image || audio || found {
		b.Fatalf("catalogModalities miss = (%t, %t, %t), want all false", image, audio, found)
	}

	b.Run("context/hit", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if got := catalogContextWindow(providerOpenRouter, hit); got == 0 {
				b.Fatal("catalogued lookup missed")
			}
		}
	})
	b.Run("context/miss", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if got := catalogContextWindow(providerOpenRouter, miss); got != 0 {
				b.Fatal("uncatalogued lookup hit")
			}
		}
	})
	b.Run("modalities/hit", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, _, found := catalogModalities(providerOpenRouter, hit); !found {
				b.Fatal("catalogued lookup missed")
			}
		}
	})
	b.Run("modalities/miss", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, _, found := catalogModalities(providerOpenRouter, miss); found {
				b.Fatal("uncatalogued lookup hit")
			}
		}
	})
}
