package app

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/providercatalog"
)

// regWithProvider builds a single-entry registry whose one provider id is backed
// by a provider with the given capabilities (an offline mock). It is the
// composition-layer fixture for modelCapability, which reads the catalog by id +
// the adapter's Capabilities().
func regWithProvider(id string, caps port.ProviderCapabilities) *providerRegistry {
	p := mockllm.NewWith([]mockllm.Option{mockllm.WithCapabilities(caps)}, mockllm.TextTurn("x"))
	return &providerRegistry{
		entries:   map[string]providerEntry{id: {id: id, provider: p, available: true}},
		defaultID: id,
	}
}

// regWithMeta builds regWithProvider's registry but ALSO attaches a live meta store
// Swap'd with the supplied live modelEntry list for the provider — the fixture for the
// live-first modality path of modelCapability (the same store the picker reads). It
// mirrors livemeta_test.go's newLiveMetaStore + Swap convention.
func regWithMeta(id string, caps port.ProviderCapabilities, live []modelEntry) *providerRegistry {
	reg := regWithProvider(id, caps)
	meta := newMetadataFixture()
	meta.setMetadataFixture(map[string][]modelEntry{id: live})
	reg.meta = meta
	return reg
}

// catalogImageModel returns a (model id) that the catalog marks image-capable for
// the given provider, plus a model id that is NOT image-capable, so the
// intersection tests assert against the SAME data the catalog ships.
func catalogModels(t *testing.T, providerID string) (imageModel, noImageModel string) {
	t.Helper()
	p, ok := providercatalog.Default().Provider(providerID)
	if !ok {
		t.Fatalf("provider %q not in catalog", providerID)
	}
	for _, m := range p.Models() {
		if m.SupportsImageInput() && imageModel == "" {
			imageModel = m.ID()
		}
		if !m.SupportsImageInput() && noImageModel == "" {
			noImageModel = m.ID()
		}
	}
	return imageModel, noImageModel
}

// TestModelCapabilityIntersection_AdapterYes: a catalog model with image + an
// adapter that reports Image:true ⇒ Image:true (the common P0 case).
func TestModelCapabilityIntersection_AdapterYes(t *testing.T) {
	imageModel, _ := catalogModels(t, providerOpenAI)
	if imageModel == "" {
		t.Skip("no catalogued openai image model")
	}
	reg := regWithProvider(providerOpenAI, port.ProviderCapabilities{Image: true})
	if got := modelCapability(reg, providerOpenAI, imageModel); !got.Image {
		t.Fatalf("Image = false, want true (catalog image ∩ adapter Image:true)")
	}
}

// TestModelCapabilityIntersection_AdapterNo: the load-bearing "adapter says no"
// branch P1 relies on, provable in P0 via the test-double provider. The catalog
// model claims image but the adapter reports Image:false ⇒ Image:false. This is the
// whole reason the AND lives in composition.
func TestModelCapabilityIntersection_AdapterNo(t *testing.T) {
	imageModel, _ := catalogModels(t, providerOpenAI)
	if imageModel == "" {
		t.Skip("no catalogued openai image model")
	}
	reg := regWithProvider(providerOpenAI, port.ProviderCapabilities{Image: false})
	if got := modelCapability(reg, providerOpenAI, imageModel); got.Image {
		t.Fatalf("Image = true, want false (catalog image ∩ adapter Image:false must be false)")
	}
}

// TestModelCapabilityIntersection_CatalogNoImage: a catalog model WITHOUT image +
// an adapter that reports Image:true ⇒ Image:false (the catalog gates it).
func TestModelCapabilityIntersection_CatalogNoImage(t *testing.T) {
	_, noImageModel := catalogModels(t, providerOpenAI)
	if noImageModel == "" {
		t.Skip("no catalogued openai non-image model")
	}
	reg := regWithProvider(providerOpenAI, port.ProviderCapabilities{Image: true})
	if got := modelCapability(reg, providerOpenAI, noImageModel); got.Image {
		t.Fatalf("Image = true, want false (catalog non-image ∩ adapter Image:true must be false)")
	}
}

// TestModelCapabilityIntersection_CatalogAudioAlwaysFalse pins the documented
// invariant: a CATALOGUED model yields Audio==false even when the injected adapter
// reports Audio:true, because the catalog carries no audio field today ⇒ the
// catAudio term is always false ⇒ the AND is false for any catalogued model. Guards
// against a future catalog that adds an audio modality silently flipping the echo.
func TestModelCapabilityIntersection_CatalogAudioAlwaysFalse(t *testing.T) {
	imageModel, _ := catalogModels(t, providerOpenAI)
	if imageModel == "" {
		t.Skip("no catalogued openai model")
	}
	reg := regWithProvider(providerOpenAI, port.ProviderCapabilities{Image: true, Audio: true})
	if got := modelCapability(reg, providerOpenAI, imageModel); got.Audio {
		t.Fatalf("Audio = true for a catalogued model with adapter Audio:true; " +
			"want false (catalog carries no audio field, so the catAudio term is always false)")
	}
}

// TestModelCapabilityIntersection_PassthroughModel: an uncatalogued model id falls
// back to ADAPTER-ONLY caps (image NOT zeroed) — a passthrough model trusts the
// adapter when the catalog is silent. This fixture's registry carries NO meta store
// (meta:nil from regWithProvider), so it documents the LIVE-ABSENT fallback: with no
// live entry AND no catalog row, modelCapability returns the adapter caps verbatim.
func TestModelCapabilityIntersection_PassthroughModel(t *testing.T) {
	reg := regWithProvider(providerOpenAI, port.ProviderCapabilities{Image: true, Audio: true})
	got := modelCapability(reg, providerOpenAI, "totally-made-up-model-not-in-catalog")
	if !got.Image {
		t.Fatal("passthrough Image = false, want adapter-only true (catalog silent must not zero it)")
	}
	if !got.Audio {
		t.Fatal("passthrough Audio = false, want adapter-only true")
	}
}

// TestModelCapability_LiveModalitiesAuthoritative_TextOnly reproduces the reported
// bug: an OpenRouter TEXT-ONLY model (openai/gpt-4) shares the openai adapter whose
// Capabilities() is Image:true, but its LIVE input_modalities are ["text"]. The live
// store is authoritative ⇒ Image must be FALSE (adapter Image:true AND no image
// modality). Before the live-first wiring this returned true (the bug).
func TestModelCapability_LiveModalitiesAuthoritative_TextOnly(t *testing.T) {
	reg := regWithMeta(providerOpenRouter, port.ProviderCapabilities{Image: true},
		[]modelEntry{{ID: "openai/gpt-4", InputModalities: []string{"text"}}})
	if got := modelCapability(reg, providerOpenRouter, "openai/gpt-4"); got.Image {
		t.Fatalf("Image = true for a live text-only model, want false (adapter Image:true ∩ no image modality)")
	}
}

// TestModelCapability_LiveModalitiesAuthoritative_Vision: the same live path but the
// model's live modalities include "image" ⇒ Image:true (adapter Image:true ∩ image).
func TestModelCapability_LiveModalitiesAuthoritative_Vision(t *testing.T) {
	reg := regWithMeta(providerOpenRouter, port.ProviderCapabilities{Image: true},
		[]modelEntry{{ID: "openai/gpt-4o", InputModalities: []string{"text", "image"}}})
	if got := modelCapability(reg, providerOpenRouter, "openai/gpt-4o"); !got.Image {
		t.Fatalf("Image = false for a live vision model, want true (adapter Image:true ∩ image modality)")
	}
}

// TestModelCapability_LiveMissCatalogFloor: with a meta store present but EMPTY of the
// queried model (live miss), modelCapability falls through to the catalog floor — a
// catalogued non-image model is still gated to Image:false. Confirms the live path
// does not break the catalog floor when the live store simply lacks the model.
func TestModelCapability_LiveMissCatalogFloor(t *testing.T) {
	_, noImageModel := catalogModels(t, providerOpenAI)
	if noImageModel == "" {
		t.Skip("no catalogued openai non-image model")
	}
	// meta store seeded for a DIFFERENT provider; the openai query is a live miss.
	reg := regWithMeta(providerOpenAI, port.ProviderCapabilities{Image: true},
		[]modelEntry{{ID: "unrelated-live-model", InputModalities: []string{"text", "image"}}})
	if got := modelCapability(reg, providerOpenAI, noImageModel); got.Image {
		t.Fatalf("Image = true on live-miss catalog floor, want false (catalog non-image gates it)")
	}
}

// TestModelCapability_LiveMissCatalogFloor_ImageModel: the live-miss path must not
// SWALLOW the catalog's true. A live store present but lacking the queried model
// (live miss) + a catalogued IMAGE model + adapter Image:true ⇒ Image:true via the
// catalog floor. Pairs with the non-image case above (which proves the floor gates).
func TestModelCapability_LiveMissCatalogFloor_ImageModel(t *testing.T) {
	imageModel, _ := catalogModels(t, providerOpenAI)
	if imageModel == "" {
		t.Skip("no catalogued openai image model")
	}
	reg := regWithMeta(providerOpenAI, port.ProviderCapabilities{Image: true},
		[]modelEntry{{ID: "unrelated-live-model", InputModalities: []string{"text"}}})
	if got := modelCapability(reg, providerOpenAI, imageModel); !got.Image {
		t.Fatalf("Image = false on live-miss image catalog floor, want true (catalog image ∩ adapter Image:true)")
	}
}

// TestModelCapability_LiveVisionAdapterNo is the live twin of the catalog
// CatalogNoImage→AdapterNo gate: live modalities include "image" but the ADAPTER
// reports Image:false ⇒ the live branch yields Image:false (adapter is the transmit
// ceiling that clamps the live path, the same AND the catalog path enforces).
func TestModelCapability_LiveVisionAdapterNo(t *testing.T) {
	reg := regWithMeta(providerOpenRouter, port.ProviderCapabilities{Image: false},
		[]modelEntry{{ID: "openai/gpt-4o", InputModalities: []string{"text", "image"}}})
	if got := modelCapability(reg, providerOpenRouter, "openai/gpt-4o"); got.Image {
		t.Fatalf("Image = true with adapter Image:false on the live path, want false (adapter ceiling clamps live)")
	}
}

// TestModelCapability_LiveAudioModality exercises the LIVE audio path (hasAudioModality):
// live modalities ["text","audio"] + adapter Audio:true ⇒ Audio:true; the same live
// modalities with adapter Audio:false ⇒ Audio:false (the AND gate). Audio is dormant in
// P0 adapters, so this is the only coverage of the live audio AND.
func TestModelCapability_LiveAudioModality(t *testing.T) {
	regYes := regWithMeta(providerOpenRouter, port.ProviderCapabilities{Image: true, Audio: true},
		[]modelEntry{{ID: "some/audio-model", InputModalities: []string{"text", "audio"}}})
	if got := modelCapability(regYes, providerOpenRouter, "some/audio-model"); !got.Audio {
		t.Fatalf("Audio = false for live audio modality + adapter Audio:true, want true")
	}
	regNo := regWithMeta(providerOpenRouter, port.ProviderCapabilities{Image: true, Audio: false},
		[]modelEntry{{ID: "some/audio-model", InputModalities: []string{"text", "audio"}}})
	if got := modelCapability(regNo, providerOpenRouter, "some/audio-model"); got.Audio {
		t.Fatalf("Audio = true with adapter Audio:false on the live path, want false (the AND gate)")
	}
}

// TestModelCapability_LivePresentButEmpty_AgreesWithPicker is the regression guard for
// the picker≠echo Medium: a live entry that EXISTS but carries an EMPTY modality list
// for a model id that is ALSO catalogued image-capable. A PRESENT live entry is
// authoritative (text-only), so BOTH the session echo (modelCapability) and the picker
// (projectModelEntry) must report Image:false — they AGREE. If modalitiesFor re-added a
// len>0 guard, the echo would fall through to the catalog image row (Image:true) while
// the picker stays false, breaking the single-source invariant.
func TestModelCapability_LivePresentButEmpty_AgreesWithPicker(t *testing.T) {
	imageModel, _ := catalogModels(t, providerOpenRouter)
	if imageModel == "" {
		t.Skip("no catalogued openrouter image model")
	}
	empty := modelEntry{ID: imageModel, InputModalities: []string{}} // explicitly declared, but empty
	reg := regWithMeta(providerOpenRouter, port.ProviderCapabilities{Image: true}, []modelEntry{empty})

	echo := modelCapability(reg, providerOpenRouter, imageModel).Image
	if echo {
		t.Errorf("session echo Image = true for a present-but-empty live entry (catalogued image), want false")
	}
	picker := projectModelEntry(reg, Config{}, reg.meta.current(), providerOpenRouter, empty).Image
	if picker {
		t.Errorf("picker Image = true for a present-but-empty live entry, want false")
	}
	if echo != picker {
		t.Errorf("picker/echo DISAGREE for present-but-empty %q: echo=%v picker=%v", imageModel, echo, picker)
	}
}

func TestModelCapability_LiveOmittedModalitiesFallsBack(t *testing.T) {
	imageModel, _ := catalogModels(t, providerOpenRouter)
	if imageModel == "" {
		t.Skip("no catalogued openrouter image model")
	}
	reg := regWithMeta(providerOpenRouter, port.ProviderCapabilities{Image: true}, []modelEntry{{ID: imageModel}})
	if got := modelCapability(reg, providerOpenRouter, imageModel); !got.Image {
		t.Fatal("catalogued image model with omitted live modalities lost catalog capability")
	}
	const unknown = "gateway/uncatalogued"
	reg.meta.setMetadataFixture(map[string][]modelEntry{providerOpenRouter: {{ID: unknown}}})
	if got := modelCapability(reg, providerOpenRouter, unknown); !got.Image {
		t.Fatal("uncatalogued model with omitted live modalities lost adapter capability")
	}
}

// TestModelCapabilityIntersection_UnknownProvider: an unknown provider id yields the
// zero value (text-only), no panic (fail-safe — a provider we cannot reach
// transmits nothing).
func TestModelCapabilityIntersection_UnknownProvider(t *testing.T) {
	reg := regWithProvider(providerOpenAI, port.ProviderCapabilities{Image: true})
	got := modelCapability(reg, "no-such-provider", "whatever")
	if got != (port.ProviderCapabilities{}) {
		t.Fatalf("unknown provider caps = %+v, want zero value (text-only)", got)
	}
}

// TestModelCapabilityIntersection_NilRegistry: a nil registry is fail-safe
// (text-only), never a panic.
func TestModelCapabilityIntersection_NilRegistry(t *testing.T) {
	if got := modelCapability(nil, providerOpenAI, "gpt-5"); got != (port.ProviderCapabilities{}) {
		t.Fatalf("nil-registry caps = %+v, want zero value", got)
	}
}

// TestModelCapabilityIntersection_ReasoningNotInCaps documents that reasoning is
// NOT a port.ProviderCapabilities bit and is therefore NOT intersected here — it
// stays catalog-sourced on ModelInfo. The intersection only carries image/audio/
// embedded-context, so a reasoning-capable model's caps are governed solely by the
// image/audio AND (this guards against someone smuggling reasoning into the echo).
func TestModelCapabilityIntersection_ReasoningNotInCaps(t *testing.T) {
	// port.ProviderCapabilities has no Reasoning field; this test exists as a
	// living assertion of the decision. Verify the catalog DOES expose reasoning so
	// the ModelInfo path (not the caps path) remains the reasoning source.
	p, ok := providercatalog.Default().Provider(providerOpenAI)
	if !ok {
		t.Fatal("openai not in catalog")
	}
	anyReasoning := false
	for _, m := range p.Models() {
		if m.SupportsReasoning() {
			anyReasoning = true
			break
		}
	}
	if !anyReasoning {
		t.Skip("no reasoning model in catalog to anchor the decision")
	}
	// The caps struct simply does not have a reasoning bit — the intersection cannot
	// carry it. (Compile-time guaranteed; this asserts the catalog still sources it.)
}
