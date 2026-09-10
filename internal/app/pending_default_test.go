package app

import (
	"context"
	"net/http"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// --- F1 (issue #262 review finding 1): the healed default must reach a
// zero-selector session, not just the picker/accessor. ---

// TestSessionEngineFactory_HealAdoption is the UNIT-level pin for the
// sessionEngineFactory heal-adoption branch (build.go): a zero selector
// (sel == server.ProviderSelector{}) against a registry whose defaultModel is
// STILL "" resolves the engine to the adapter/endpoint default (empty model);
// once reg.defaultModel is healed (simulating healDefaultModel having run
// concurrently), the SAME zero-selector call resolves to the healed model AND
// picks up the re-minted entry.provider — proven by actually DRIVING each
// returned engine one turn and asserting the REPLY TEXT ("pre-heal" vs.
// "post-heal", two distinct mockllm instances), not just the ModelID/
// ProviderID strings. A string-only assertion here is VACUOUS on the
// fresh-Lookup axis: dropping the fresh reg.Lookup in adoptHealedDefault
// (reusing the Build-captured `provider` param instead) still reports the
// healed ModelID/ProviderID (those come from reg.ResolvedDefaultModel() and
// resolvedProviderID, untouched by which provider instance is bound) while
// silently keeping the STALE pre-heal provider bound — only driving a real
// turn catches that.
func TestSessionEngineFactory_HealAdoption(t *testing.T) {
	preHealProvider := mockllm.New(mockllm.TextTurn("pre-heal"))
	postHealProvider := mockllm.New(mockllm.TextTurn("post-heal"))

	reg := &providerRegistry{
		entries: map[string]providerEntry{
			providerToolhive: {
				id: providerToolhive, intentDriven: true, provider: preHealProvider,
				remint: func(string, port.ProviderCapabilities) port.LLMProvider { return postHealProvider },
			},
		},
		defaultID: providerToolhive, // defaultModel intentionally left "" (probe-down boot)
	}
	store := memstore.New()
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	factory := sessionEngineFactory(Config{}, reg, preHealProvider, store, policy,
		hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)

	// Pre-heal: a zero-selector session resolves to the still-empty model AND
	// is actually bound to the pre-heal provider instance.
	res1, err := factory(context.Background(), server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory (pre-heal): %v", err)
	}
	if res1.ModelID != "" {
		res1.Close()
		t.Fatalf("pre-heal ModelID = %q, want empty (adapter/endpoint default)", res1.ModelID)
	}
	if got := driveOneTurn(t, res1.Engine); got != "pre-heal" {
		t.Errorf("pre-heal engine reply = %q, want %q (bound to the pre-heal provider)", got, "pre-heal")
	}
	res1.Close()

	// Simulate healDefaultModel having run (it sets reg.defaultModel + re-mints
	// the entry — here directly, mirroring what remintEntry itself would do,
	// to isolate the ASSERTION to the factory's adoption of it).
	reg.defaultModelMu.Lock()
	reg.defaultModel = "claude-sonnet-4-6"
	reg.defaultModelMu.Unlock()
	reg.remintEntry(providerToolhive, "claude-sonnet-4-6")

	// Post-heal: the SAME zero selector now resolves to the healed model, and the
	// factory used the FRESH Lookup (the re-minted provider), not the Build-time
	// `provider` param it closed over — proven by driving a real turn and
	// asserting the REPLY came from postHealProvider, not preHealProvider.
	res2, err := factory(context.Background(), server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("factory (post-heal): %v", err)
	}
	defer res2.Close()
	if res2.ModelID != "claude-sonnet-4-6" {
		t.Fatalf("post-heal ModelID = %q, want %q", res2.ModelID, "claude-sonnet-4-6")
	}
	if res2.ProviderID != providerToolhive {
		t.Fatalf("post-heal ProviderID = %q, want %q", res2.ProviderID, providerToolhive)
	}
	if got := driveOneTurn(t, res2.Engine); got != "post-heal" {
		t.Fatalf("post-heal engine reply = %q, want %q (the factory must adopt the RE-MINTED provider via a fresh Lookup, not the Build-captured one)", got, "post-heal")
	}
}

// driveOneTurn runs eng through one turn against a fresh in-memory session +
// workspace and returns the terminal text — the shared drive helper for
// tests that need to prove WHICH provider instance an engine is actually
// bound to (a ModelID/ProviderID string assertion alone can pass even when
// the wrong provider instance is bound, since those strings are derived
// independently of the provider value).
func driveOneTurn(t *testing.T, eng *agent.Engine) string {
	t.Helper()
	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 5}, time.Now())
	ws := memfs.NewWorkspace("/ws")
	return drainRun(eng.Run(context.Background(), sess, testEnvironment(ws, nil), agent.RunRequest{Text: "hi", Parts: nil}))
}

// The sessionNeedsPerFactory/needsRehydration table extensions for the
// DefaultModelPending arm live in internal/adapter/server
// (default_model_pending_test.go) — that package owns those predicates.

// TestToolhiveSole_ProbeDown_HealedDefaultReachesZeroSelectorSession is the
// issue #262 review finding 1 FULL e2e (app.Build -> server.Service,
// offline): a sole ToolHive gateway provider boots with the proxy down
// (defaultModel==""), the proxy then comes up, the heal is driven through the
// REAL on-demand refresh (Service.ListModels, which invokes the wired
// refreshStaleModels refresher — R1.4's actual production mechanism), and a
// FRESH zero-selector session must resolve to the healed model — not "".
//
// This test MUST FAIL on the pre-fix code (the echo/registered model stays
// ""): mutation-checked by reverting sessionEngineFactory's heal-adoption
// branch (build.go step 5) and confirming failure.
func TestToolhiveSole_ProbeDown_HealedDefaultReachesZeroSelectorSession(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	cfgPath := writeToolhiveConfig(t, "https://upstream.example/gw")
	transport := &toggleTransport{body: toolhiveFixtureJSON} // starts DOWN (up=false)

	built, err := Build(ctx, Config{
		Workspace:           workspace,
		NoSoul:              true,
		ToolhiveLLM:         true,
		toolhiveConfigPath:  cfgPath,
		liveModelHTTPClient: &http.Client{Transport: transport},
		envDetector:         fakeEnv(nil), // hermetic: never read the real process environment
	})
	if err != nil {
		t.Fatalf("Build (sole toolhive, probe down): %v", err)
	}
	defer built.Close()

	// Pre-heal: a zero-selector session's resolved model is empty (the
	// R1.4-broken state the review flagged).
	preSess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession (pre-heal): %v", err)
	}
	if got := built.Service.ResolvedModel(preSess.ID).ModelID; got != "" {
		t.Fatalf("pre-heal resolved model = %q, want empty (proxy still down)", got)
	}

	// "The proxy comes up."
	transport.up.Store(true)

	// Drive the heal through the REAL on-demand mechanism production uses:
	// Service.ListModels invokes the wired refreshStaleModels refresher, which
	// re-fetches the stale (non-"ok") toolhive provider and, via
	// publishSnapshot, calls healDefaultModel.
	built.Service.ListModels(ctx)

	// A FRESH zero-selector session, created AFTER the heal, must resolve to
	// the healed model — proving the heal reaches session creation, not just
	// the registry accessor / picker.
	postSess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession (post-heal): %v", err)
	}
	rm := built.Service.ResolvedModel(postSess.ID)
	if want := "claude-sonnet-4-6"; rm.ModelID != want {
		t.Fatalf("post-heal resolved model = %q, want %q (the healed default)", rm.ModelID, want)
	}
	if rm.ProviderID != providerToolhive {
		t.Fatalf("post-heal resolved provider = %q, want %q", rm.ProviderID, providerToolhive)
	}
}

// TestToolhiveAvailableNotDefault_E2E is the WAVE-1 wire-path e2e (this wave):
// it drives the FULL composition (app.Build → server.Service → gRPC handler)
// with a toolhive entry probed-ok via the liveModelHTTPClient offline seam AND a
// keyed openrouter entry (so openrouter outranks toolhive on the precedence
// ladder and becomes the default — the precedence ladder UNCHANGED), then
// asserts the ListModels gRPC handler returns one provider_status row for
// toolhive with available_not_default==true AND model_count==N (the live
// listing's length).
//
// NOTE: this e2e lives in internal/app (not internal/adapter/server, where the
// TestGRPCListModelsCarriesProviderStatus sibling lives) because the
// liveModelHTTPClient + toolhiveConfigPath composition-only test seams are
// unexported Config fields reachable only from within package app; the server
// package cannot import app (composition→adapter layering, never the reverse —
// an import cycle). It drives the SAME gRPC ListModels handler
// (HarnessServer.ListModels, grpc.go) a real gRPC client hits, which builds the
// ListModelsResponse from svc.ProviderStatuses() — the snapshot
// providerStatusProto(reg) populates — so it proves the wire projection
// end-to-end. The wire SERIALIZATION round-trip (proto marshal/unmarshal of
// the two new fields) is covered by the server_test companion assertions.
func TestToolhiveAvailableNotDefault_E2E(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	cfgPath := writeToolhiveConfig(t, "https://upstream.example/gw")

	built, err := Build(ctx, Config{
		Workspace: workspace,
		NoSoul:    true,
		// toolhive auto-detected + probed-ok offline (2 models in the fixture).
		ToolhiveLLM:         true,
		toolhiveConfigPath:  cfgPath,
		liveModelHTTPClient: toolhiveModelsClient(t, toolhiveFixtureJSON),
		// A keyed openrouter ⇒ openrouter outranks toolhive and is the default
		// (the precedence ladder is UNCHANGED by this wave).
		envDetector: fakeEnv(map[string]string{"OPENROUTER_API_KEY": "sk-or"}),
		// Mock the openrouter provider so the keyed entry constructs offline.
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("ok"))
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	// Drive the REAL gRPC ListModels handler — the wire projection path a gRPC
	// client hits (it assembles ListModelsResponse from svc.ProviderStatuses()).
	resp, err := server.NewHarnessServer(built.Service).ListModels(ctx, &mecatlv1.ListModelsRequest{})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	status := resp.GetProviderStatus()
	if len(status) != 2 {
		t.Fatalf("provider_status = %d rows, want 2 (both ToolHive protocols): %+v", len(status), status)
	}
	row := status[0]
	if row.GetProviderId() != providerToolhive {
		t.Fatalf("provider_status[0].provider_id = %q, want %q", row.GetProviderId(), providerToolhive)
	}
	if row.GetState() != statusOK {
		t.Errorf("provider_status[0].state = %q, want ok", row.GetState())
	}
	// available_not_default==true SELF-PROVES toolhive is NOT the default (if it
	// were, the field would be false) — so the precedence ladder held: the keyed
	// openrouter outranked the intent-driven toolhive.
	if !row.GetAvailableNotDefault() {
		t.Errorf("provider_status[0].available_not_default = false, want true (toolhive ok but a keyed provider is the default)")
	}
	if want := int32(2); row.GetModelCount() != want { // toolhiveFixtureJSON has 2 models
		t.Errorf("provider_status[0].model_count = %d, want %d", row.GetModelCount(), want)
	}
	if row.GetDefaultModelAutoSelected() {
		t.Errorf("provider_status[0].default_model_auto_selected = true, want false (toolhive is NOT the default provider)")
	}
}
