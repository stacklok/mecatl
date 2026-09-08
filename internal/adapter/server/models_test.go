package server_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// modelsService builds a Service carrying the given selectable-model snapshot,
// mirroring agentsService but for the ListModels RPC.
func modelsService(t *testing.T, snapshot []*mecatlv1.ModelInfo) *server.Service {
	t.Helper()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(allowRules(), nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now:    func() time.Time { return time.Unix(0, 0) },
		Models: snapshot,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func cannedModels() []*mecatlv1.ModelInfo {
	return []*mecatlv1.ModelInfo{
		{
			Id:           "gpt-5",
			ProviderId:   "openai",
			DisplayName:  "GPT-5",
			Image:        true,
			Reasoning:    true,
			ContextLimit: 400000,
		},
		{
			Id:           "anthropic/claude-opus-4.5",
			ProviderId:   "openrouter",
			DisplayName:  "Claude Opus 4.5",
			Image:        true,
			Reasoning:    false,
			ContextLimit: 200000,
		},
	}
}

func TestGRPCListModels(t *testing.T) {
	svc := modelsService(t, cannedModels())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	resp, err := client.ListModels(context.Background(), &mecatlv1.ListModelsRequest{})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(resp.GetModels()) != 2 {
		t.Fatalf("models = %d, want 2", len(resp.GetModels()))
	}
	first := resp.GetModels()[0]
	if first.GetId() != "gpt-5" || first.GetProviderId() != "openai" || first.GetDisplayName() != "GPT-5" {
		t.Fatalf("model[0] metadata = %+v", first)
	}
	if !first.GetImage() || !first.GetReasoning() || first.GetContextLimit() != 400000 {
		t.Fatalf("model[0] catalog-derived fields = %+v", first)
	}
	second := resp.GetModels()[1]
	if second.GetProviderId() != "openrouter" || second.GetReasoning() {
		t.Fatalf("model[1] = %+v", second)
	}
}

func TestGRPCListModelsEmpty(t *testing.T) {
	// No snapshot (zero providers available) => empty list, no error.
	svc := modelsService(t, nil)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	resp, err := client.ListModels(context.Background(), &mecatlv1.ListModelsRequest{})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(resp.GetModels()) != 0 {
		t.Fatalf("empty models = %d, want 0", len(resp.GetModels()))
	}
}

func TestHTTPListModels(t *testing.T) {
	svc := modelsService(t, cannedModels())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	var resp mecatlv1.ListModelsResponse
	if code := httpGet(t, srv, "/v1/models", &resp); code != 200 {
		t.Fatalf("GET /v1/models status = %d", code)
	}
	if len(resp.GetModels()) != 2 {
		t.Fatalf("http models = %d, want 2", len(resp.GetModels()))
	}
	if resp.GetModels()[0].GetId() != "gpt-5" || resp.GetModels()[1].GetProviderId() != "openrouter" {
		t.Fatalf("http models = %+v", resp.GetModels())
	}

	// Empty snapshot still returns 200 with an empty list.
	emptySrv := httptest.NewServer(server.NewHTTPHandler(modelsService(t, nil)))
	defer emptySrv.Close()
	var empty mecatlv1.ListModelsResponse
	if code := httpGet(t, emptySrv, "/v1/models", &empty); code != 200 {
		t.Fatalf("empty GET /v1/models status = %d", code)
	}
	if len(empty.GetModels()) != 0 {
		t.Fatalf("empty http models = %d, want 0", len(empty.GetModels()))
	}
}

// TestSetModelsSwap proves the atomic swap: ListModels reflects the SEED snapshot
// at first, then the SetModels-swapped set afterward, and the ModelSelection cap
// stays honest across the swap (it reads the same atomic length). This is the
// deterministic server-side proof of the async-swap mechanism (composition drives
// SetModels from its background live refresh).
func TestSetModelsSwap(t *testing.T) {
	// Seed with one (embedded-floor stand-in) model.
	seed := []*mecatlv1.ModelInfo{{Id: "seed/model", ProviderId: "openrouter", DisplayName: "Seed"}}
	svc := modelsService(t, seed)

	got := svc.ListModels(context.Background())
	if len(got) != 1 || got[0].GetId() != "seed/model" {
		t.Fatalf("seed ListModels = %+v, want the seed model", got)
	}
	if !capsFromCreate(t, svc).GetModelSelection() {
		t.Fatal("ModelSelection cap false with a non-empty seed")
	}

	// Swap in a larger live set (the composition refresh's effect).
	live := []*mecatlv1.ModelInfo{
		{Id: "live/a", ProviderId: "openrouter", DisplayName: "A"},
		{Id: "live/b", ProviderId: "openrouter", DisplayName: "B"},
		{Id: "live/c", ProviderId: "openrouter", DisplayName: "C"},
	}
	svc.SetModels(live)

	got = svc.ListModels(context.Background())
	if len(got) != 3 || got[0].GetId() != "live/a" {
		t.Fatalf("post-swap ListModels = %+v, want the 3 live models", got)
	}
	if !capsFromCreate(t, svc).GetModelSelection() {
		t.Fatal("ModelSelection cap false after a non-empty swap")
	}

	// A nil swap stores an empty (non-nil) slice and flips the cap off.
	svc.SetModels(nil)
	if len(svc.ListModels(context.Background())) != 0 {
		t.Fatal("nil swap did not empty ListModels")
	}
	if capsFromCreate(t, svc).GetModelSelection() {
		t.Fatal("ModelSelection cap true after an empty swap")
	}
}

// TestCapabilitiesModelSelectionRefresherWired is the regression for the
// uncatalogued-provider picker bug: an EMPTY model inventory (the static catalog
// seed contributes nothing for an uncatalogued provider) must STILL advertise
// ModelSelection=true when an on-demand refresher is wired — otherwise a session
// created before the async live swap lands freezes the picker off for its whole
// life (caps are read once at CreateSession). Opening the picker fires ListModels,
// which runs the refresher and fills the list.
func TestCapabilitiesModelSelectionRefresherWired(t *testing.T) {
	svc := modelsService(t, nil) // empty inventory
	if capsFromCreate(t, svc).GetModelSelection() {
		t.Fatal("precondition: empty inventory + no refresher should be false")
	}
	svc.SetModelsRefresher(func(context.Context) {})
	if !capsFromCreate(t, svc).GetModelSelection() {
		t.Fatal("ModelSelection cap false with an empty inventory but a wired refresher (uncatalogued-provider picker regresses)")
	}
}
func TestCapabilitiesModelSelection(t *testing.T) {
	on := capsFromCreate(t, modelsService(t, cannedModels()))
	if !on.GetModelSelection() {
		t.Errorf("model_selection cap = false, want true (non-empty Models snapshot)")
	}
	off := capsFromCreate(t, modelsService(t, nil))
	if off.GetModelSelection() {
		t.Errorf("model_selection cap = true, want false (empty Models snapshot)")
	}
}
