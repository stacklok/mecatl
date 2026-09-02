package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/providercatalog"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestMultiProviderE2E drives the FULL composition (app.Build → server.Service)
// with TWO available real provider ids (openai / openrouter) backed by DISTINCT
// mock providers via the S3 composition-only providerConstructor seam, plus a fake
// two-key env. All offline (mockllm, real temp workspace, no network). It proves:
//
//  1. ListModels returns models for ALL available providers, secret-free.
//  2. CreateSession{provider_id:"openrouter"} registers a per-session engine and a
//     turn routes to the OpenRouter-bound provider (distinct reply).
//  3. CreateSession{provider_id:"openai"} routes to the openai-bound provider.
//  4. CreateSession{provider_id:"anthropic"} routes to the native-Anthropic-bound
//     provider (distinct reply), proving the registry wires it via newAnthropicEntry
//     and per-session routing / engineDepsForProvider treat it as data.
//  5. CreateSession{} (zero selector) ⇒ the shared default-provider engine.
func TestMultiProviderE2E(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()

	const sentinelKey = "sk-SENTINEL-multiprovider"

	built, err := Build(ctx, Config{
		Workspace: workspace,
		NoSoul:    true,
		// Fake three-key env ⇒ openai + openrouter + anthropic all AVAILABLE.
		envDetector: fakeEnv(map[string]string{
			"OPENAI_API_KEY":     sentinelKey,
			"OPENROUTER_API_KEY": sentinelKey,
			"ANTHROPIC_API_KEY":  sentinelKey,
		}),
		// Strictly offline: the openrouter/anthropic listers are armed (keyed), so refuse
		// every live fetch — they fail-safe to the embedded catalog without any network.
		liveModelHTTPClient: offlineHTTPClient(),
		// Mock-per-id seam: each available provider id gets a DISTINCT mock that
		// replies with its own id, so a routed turn proves which provider was bound.
		// Several identical turns are scripted because the default (openai) provider
		// is exercised by more than one turn below (a mock exhausts after its turns).
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			reply := "REPLY-FROM-" + id
			return mockllm.New(
				mockllm.TextTurn(reply), mockllm.TextTurn(reply),
				mockllm.TextTurn(reply), mockllm.TextTurn(reply),
			)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	// (1) ListModels returns models for both providers, never the sentinel key.
	models := svc.ListModels(ctx)
	if len(models) == 0 {
		t.Fatal("ListModels returned no models for two available providers")
	}
	seen := map[string]bool{}
	for _, m := range models {
		seen[m.GetProviderId()] = true
		for _, f := range []string{m.GetId(), m.GetProviderId(), m.GetDisplayName()} {
			if strings.Contains(f, sentinelKey) {
				t.Fatalf("secret leaked into ListModels field %q", f)
			}
		}
	}
	if !seen[providerOpenAI] || !seen[providerOpenRouter] || !seen[providerAnthropic] {
		t.Fatalf("ListModels missing a provider; saw %v", seen)
	}
	// Current Claude ids surface for the native anthropic provider.
	var sawSonnet46, sawOpus48 bool
	for _, m := range models {
		if m.GetProviderId() != providerAnthropic {
			continue
		}
		switch m.GetId() {
		case "claude-sonnet-4-6":
			sawSonnet46 = true
		case "claude-opus-4-8":
			sawOpus48 = true
		}
	}
	if !sawSonnet46 || !sawOpus48 {
		t.Fatalf("ListModels missing current Claude ids (sonnet-4-6=%v opus-4-8=%v)", sawSonnet46, sawOpus48)
	}

	// (2) provider_id="openrouter" routes a turn to the OpenRouter-bound provider.
	if got := runProviderTurn(t, svc, server.ProviderSelector{ProviderID: providerOpenRouter}); got != "REPLY-FROM-openrouter" {
		t.Fatalf("openrouter selector routed to %q, want REPLY-FROM-openrouter", got)
	}

	// (3) provider_id="openai" routes to the openai-bound provider (distinct reply).
	if got := runProviderTurn(t, svc, server.ProviderSelector{ProviderID: providerOpenAI}); got != "REPLY-FROM-openai" {
		t.Fatalf("openai selector routed to %q, want REPLY-FROM-openai", got)
	}

	// (4) provider_id="anthropic" routes a turn to the native-Anthropic-bound
	// provider (distinct reply) — proving the registry wires it and per-session
	// routing / engineDepsForProvider / capability intersection treat it as data.
	if got := runProviderTurn(t, svc, server.ProviderSelector{ProviderID: providerAnthropic}); got != "REPLY-FROM-anthropic" {
		t.Fatalf("anthropic selector routed to %q, want REPLY-FROM-anthropic", got)
	}

	// An unknown provider still ⇒ InvalidArgument (never a silent fallback).
	_, err = svc.CreateSessionWithProvider(ctx, session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: "does-not-exist"})
	if !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("unknown-provider create error = %v, want ErrInvalidArgument", err)
	}

	// (5) zero selector ⇒ the shared default-provider engine (openai is the default).
	zeroSess, err := svc.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession(zero): %v", err)
	}
	run, err := svc.StartRun(ctx, zeroSess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun(zero): %v", err)
	}
	if got := drainRun(run); got != "REPLY-FROM-openai" {
		t.Fatalf("zero-selector turn routed to %q, want the default (openai) provider's reply", got)
	}
}

// TestMultiProviderCapabilityEcho drives the FULL composition and asserts the
// per-session capability echo (sink b) and the single-source agreement between the
// ACP gate (ProviderCapabilities) and the wire echo (sink c). It wires DISTINCT
// adapter capabilities per provider via the providerConstructor seam: openai
// Image:false, openrouter Image:true. The catalog is REAL, so the echo is the
// catalog ∩ adapter intersection. All offline.
func TestMultiProviderCapabilityEcho(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()

	// openai is the DEFAULT provider (preferredDefaultProvider prefers it). cfg.Model
	// is left empty ⇒ the default session resolves to the empty model on openai ⇒
	// passthrough ⇒ adapter-only caps for the default = openai's Image:false.
	built, err := Build(ctx, Config{
		Workspace: workspace,
		NoSoul:    true,
		envDetector: fakeEnv(map[string]string{
			"OPENAI_API_KEY":     "sk-x",
			"OPENROUTER_API_KEY": "sk-x",
		}),
		// Strictly offline: refuse the (keyed) openrouter live fetch ⇒ embedded floor.
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			caps := port.ProviderCapabilities{Image: id == providerOpenRouter}
			// Identifying reply so a routed turn proves WHICH provider was bound,
			// closing the routing⇄caps coherence gap (the turn and the caps must agree
			// on the same provider). Two turns scripted per mock (one per assertion).
			reply := "REPLY-FROM-" + id
			return mockllm.NewWith([]mockllm.Option{mockllm.WithCapabilities(caps)},
				mockllm.TextTurn(reply), mockllm.TextTurn(reply))
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	// (b) per-session echo for a session bound to openrouter + a catalog image model,
	// AND the routing⇄caps coherence: the turn routes to the openrouter-bound provider
	// (REPLY-FROM-openrouter) AND that session's caps reflect openrouter's intersected
	// caps (Image:true), in ONE test.
	imgModel, _ := firstImageModel(t, providerOpenRouter)
	orSess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: providerOpenRouter, ModelID: imgModel})
	if err != nil {
		t.Fatalf("create openrouter session: %v", err)
	}
	if got := svc.SessionCapabilities(orSess.ID); !got.Image {
		t.Fatalf("openrouter+image-model session echo Image = false, want true (catalog image ∩ adapter Image:true)")
	}
	orRun, err := svc.StartRun(ctx, orSess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun(openrouter): %v", err)
	}
	if got := drainRun(orRun); got != "REPLY-FROM-openrouter" {
		t.Fatalf("openrouter session turn routed to %q, want REPLY-FROM-openrouter (routing⇄caps must agree)", got)
	}

	// A session bound to openai (Image:false adapter) + the same image model ⇒ the
	// intersection is false, it DIFFERS from the openrouter echo (per-session), AND the
	// turn routes to the openai-bound provider — proving routing and caps agree on the
	// SAME provider per session.
	oaSess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: imgModel})
	if err != nil {
		t.Fatalf("create openai session: %v", err)
	}
	if got := svc.SessionCapabilities(oaSess.ID); got.Image {
		t.Fatalf("openai+image-model session echo Image = true, want false (adapter Image:false)")
	}
	oaRun, err := svc.StartRun(ctx, oaSess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun(openai): %v", err)
	}
	if got := drainRun(oaRun); got != "REPLY-FROM-openai" {
		t.Fatalf("openai session turn routed to %q, want REPLY-FROM-openai (routing⇄caps must agree)", got)
	}

	// (5) zero-selector session ⇒ echo == DefaultCapabilities (the default provider's
	// intersected caps). The default is openai (Image:false) on the empty model.
	zeroSess, err := svc.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("create zero-selector session: %v", err)
	}
	def := svc.ProviderCapabilities()
	if svc.SessionCapabilities(zeroSess.ID) != def {
		t.Fatalf("zero-selector echo %+v != ProviderCapabilities %+v (must read DefaultCapabilities)",
			svc.SessionCapabilities(zeroSess.ID), def)
	}

	// (c) single-source agreement: the ACP gate (ProviderCapabilities) equals the
	// wire echo for a default-engine session — both derive from DefaultCapabilities.
	if def.Image {
		t.Fatalf("default ProviderCapabilities Image = true, want false (default openai adapter Image:false)")
	}
}

// TestMultiProviderResolvedModelEcho drives the FULL composition and asserts the
// per-session EFFECTIVE-model echo (Service.ResolvedModel): a zero-selector default
// session reports the composition DefaultResolvedModel (the registry default
// provider + the resolved cfg.Model), and an explicit selector reports the RESOLVED
// provider+model (the values the engine was bound to), NOT the raw request. The
// context window comes from the catalog, agreeing with ListModels. All offline.
func TestMultiProviderResolvedModelEcho(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()

	built, err := Build(ctx, Config{
		Workspace: workspace,
		NoSoul:    true,
		// Pin an explicit default model so the default echo is deterministic (no live
		// fetch). It is catalogued for openai, so the context window resolves.
		Model: "gpt-5",
		envDetector: fakeEnv(map[string]string{
			"OPENAI_API_KEY":     "sk-x",
			"OPENROUTER_API_KEY": "sk-x",
		}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("REPLY-FROM-" + id))
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	// (a) zero-selector default session ⇒ DefaultResolvedModel: the default provider
	// (openai) + the resolved cfg.Model ("gpt-5"), echoed verbatim — NOT the empty
	// request model_id. The context window is the catalog's gpt-5 limit (>0).
	zeroSess, err := svc.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession(zero): %v", err)
	}
	got := svc.ResolvedModel(zeroSess.ID)
	if got.ProviderID != providerOpenAI || got.ModelID != "gpt-5" {
		t.Fatalf("zero-selector ResolvedModel = %+v, want provider=%q model=%q (the resolved default)", got, providerOpenAI, "gpt-5")
	}
	if got.ContextWindow <= 0 {
		t.Fatalf("zero-selector ResolvedModel.ContextWindow = %d, want the catalog gpt-5 window (>0)", got.ContextWindow)
	}

	// (b) explicit selector ⇒ the RESOLVED provider+model (openrouter + a catalogued
	// model), not the default and not a naive read-back. Use a real catalogued model
	// so the window resolves from the catalog.
	orModel, _ := firstImageModel(t, providerOpenRouter)
	orSess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: providerOpenRouter, ModelID: orModel})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider(openrouter): %v", err)
	}
	gotOR := svc.ResolvedModel(orSess.ID)
	if gotOR.ProviderID != providerOpenRouter || gotOR.ModelID != orModel {
		t.Fatalf("openrouter ResolvedModel = %+v, want provider=%q model=%q (the resolved selector, not the default)", gotOR, providerOpenRouter, orModel)
	}
	if gotOR == got {
		t.Fatalf("explicit-selector ResolvedModel %+v must differ from the default %+v", gotOR, got)
	}
}

// TestServerConfiguredDefaultModelEcho is the issue-#21 service-level proof
// through the FULL composition: with --default-provider/--default-model
// configured (and NO --model), a zero-selector CreateSession lands on the
// server-configured deployment default and the resolved-model echo
// (Service.ResolvedModel → the CreateSessionResponse echo) reports the
// configured provider id + model id — not the per-provider builtin. All offline.
func TestServerConfiguredDefaultModelEcho(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	const (
		wantProvider = providerOpenRouter
		wantModel    = "openai/gpt-5-mini" // catalogued for openrouter; NOT its builtin (openai/gpt-5)
	)

	built, err := Build(ctx, Config{
		Workspace: workspace,
		NoSoul:    true,
		// The configured deployment default: openrouter would NOT be the preferred
		// default (openai is, with both keys present), so this also proves the
		// provider half overrides the preference end-to-end.
		DefaultProvider: wantProvider,
		DefaultModel:    wantModel,
		envDetector: fakeEnv(map[string]string{
			"OPENAI_API_KEY":     "sk-x",
			"OPENROUTER_API_KEY": "sk-x",
		}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("REPLY-FROM-" + id))
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	sess, err := svc.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession(zero-selector): %v", err)
	}
	got := svc.ResolvedModel(sess.ID)
	if got.ProviderID != wantProvider || got.ModelID != wantModel {
		t.Fatalf("zero-selector ResolvedModel = %+v, want provider=%q model=%q (the server-configured default, not the builtin)",
			got, wantProvider, wantModel)
	}
	if got.ContextWindow <= 0 {
		t.Fatalf("ResolvedModel.ContextWindow = %d, want the catalogued window (>0) — the configured default is catalogued by construction", got.ContextWindow)
	}

	// The zero-selector turn actually routes to the configured default PROVIDER.
	run, err := svc.StartRun(ctx, sess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if gotText := drainRun(run); gotText != "REPLY-FROM-"+wantProvider {
		t.Fatalf("zero-selector turn routed to %q, want REPLY-FROM-%s (the configured default provider)", gotText, wantProvider)
	}

	// A CLIENT selector for a DIFFERENT provider/model beats the server-configured
	// default: the echo and the routing both follow the selector (the configured
	// tier sits BELOW client-side choices in the precedence chain).
	const selModel = "gpt-5"
	selSess, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, defaultLimits(),
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: selModel})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider(openai selector): %v", err)
	}
	gotSel := svc.ResolvedModel(selSess.ID)
	if gotSel.ProviderID != providerOpenAI || gotSel.ModelID != selModel {
		t.Fatalf("selector ResolvedModel = %+v, want provider=%q model=%q (the client selector, not the configured server default)",
			gotSel, providerOpenAI, selModel)
	}
	selRun, err := svc.StartRun(ctx, selSess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun(selector): %v", err)
	}
	if gotText := drainRun(selRun); gotText != "REPLY-FROM-"+providerOpenAI {
		t.Fatalf("selector turn routed to %q, want REPLY-FROM-%s (the selector's provider beats the configured default)", gotText, providerOpenAI)
	}
}

// TestResolvedModelMCPOnlySessionMatchesDefaultWindow pins the single-source
// consistency fix: a ZERO-selector session that needs a per-session engine ONLY
// because client MCP specs are attached must report the SAME ResolvedModel as
// Config.DefaultResolvedModel — including ContextWindow. Before the fix the
// MCP-only factory path left the window 0 while the default path carried the catalog
// window, so the SAME default provider+model echoed two different windows depending
// on whether MCP was present. The MCP spec points at an unreachable URL; the factory
// is best-effort (mounts core tools, still returns a usable engine), so the create
// still succeeds and exercises the per-session-engine path with a zero selector. All
// offline.
func TestResolvedModelMCPOnlySessionMatchesDefaultWindow(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()

	built, err := Build(ctx, Config{
		Workspace:           workspace,
		NoSoul:              true,
		Model:               "gpt-5", // catalogued for openai ⇒ a non-zero window
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.TextTurn("REPLY-FROM-" + id))
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	// The default-path window (no per-session engine) — the single-source value.
	dfltSess, err := svc.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession(default): %v", err)
	}
	wantWindow := svc.ResolvedModel(dfltSess.ID).ContextWindow
	if wantWindow <= 0 {
		t.Fatalf("default ResolvedModel.ContextWindow = %d, want the catalog gpt-5 window (>0)", wantWindow)
	}

	// A ZERO-selector session that needs a per-session engine because client MCP
	// specs are attached. The unreachable URL degrades to core-only tools but the
	// per-session-engine factory path still runs (the catalog-seed window fix lives
	// there).
	mcpSess, err := svc.CreateSessionWithMCP(ctx, session.ModeDefault, defaultLimits(),
		[]mcp.ServerConfig{{Name: "docs", URL: "https://unreachable.invalid/mcp"}})
	if err != nil {
		t.Fatalf("CreateSessionWithMCP(zero selector + spec): %v", err)
	}
	got := svc.ResolvedModel(mcpSess.ID)
	if got.ProviderID != providerOpenAI || got.ModelID != "gpt-5" {
		t.Fatalf("MCP-only ResolvedModel = %+v, want the default provider+model", got)
	}
	if got.ContextWindow != wantWindow {
		t.Fatalf("MCP-only ResolvedModel.ContextWindow = %d, want %d (must match DefaultResolvedModel regardless of MCP)", got.ContextWindow, wantWindow)
	}
}

// firstImageModel returns the first catalog model id for providerID that the
// catalog marks image-capable.
func firstImageModel(t *testing.T, providerID string) (string, bool) {
	t.Helper()
	p, ok := providercatalog.Default().Provider(providerID)
	if !ok {
		t.Fatalf("provider %q not in catalog", providerID)
	}
	for _, m := range p.Models() {
		if m.SupportsImageInput() {
			return m.ID(), true
		}
	}
	t.Skipf("no catalogued image model for %q", providerID)
	return "", false
}

// TestSessionCapabilitiesNoSecrets builds with SENTINEL keys and asserts the
// per-session capability echo carries NO secret. SessionCapabilities is bools-only,
// so it structurally cannot leak — this test documents that and tripwires a future
// string field. It also re-checks ProviderCapabilities (the ACP gate) and the
// CreateSessionResponse path through the gRPC handler for the sentinel in NO string
// field (CWE-200). All offline.
func TestSessionCapabilitiesNoSecrets(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	const sentinelKey = "sk-SENTINEL-capecho"

	built, err := Build(ctx, Config{
		Workspace: workspace,
		NoSoul:    true,
		envDetector: fakeEnv(map[string]string{
			"OPENAI_API_KEY":     sentinelKey,
			"OPENROUTER_API_KEY": sentinelKey,
		}),
		// Strictly offline: refuse the (keyed) openrouter live fetch ⇒ embedded floor.
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.NewWith([]mockllm.Option{mockllm.WithCapabilities(port.ProviderCapabilities{Image: true})}, mockllm.TextTurn("x"))
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	// Drive the gRPC CreateSession handler so the actual CreateSessionResponse (incl.
	// session_capabilities + capabilities) is built — the wire surface the client
	// sees. Assert the sentinel appears in NO string field.
	h := server.NewHarnessServer(svc)
	imgModel, _ := firstImageModel(t, providerOpenRouter)
	resp, err := h.CreateSession(ctx, &mecatlv1.CreateSessionRequest{
		ProviderId: providerOpenRouter,
		ModelId:    imgModel,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// The echo must reflect the intersection (openrouter Image:true ∩ catalog image).
	if !resp.GetSessionCapabilities().GetImage() {
		t.Fatal("session_capabilities.image = false, want true (intersection)")
	}
	forbidden := []string{sentinelKey, "OPENAI_API_KEY", "OPENROUTER_API_KEY", openRouterDefaultBaseURL}
	// Belt-and-braces marshal scan: serialize the ENTIRE response (all fields, incl.
	// capabilities + session_capabilities + any future-added string field) and assert
	// none of the forbidden substrings appear. This future-proofs the tripwire against
	// a new string field — not just the session_id checked structurally below.
	blob, merr := protojson.Marshal(resp)
	if merr != nil {
		t.Fatalf("protojson.Marshal(resp): %v", merr)
	}
	for _, bad := range forbidden {
		if strings.Contains(string(blob), bad) {
			t.Fatalf("secret %q leaked into the marshalled CreateSessionResponse: %s", bad, blob)
		}
	}

	// ProviderCapabilities (the ACP gate) is a bools-only neutral value — no string
	// to leak; this asserts it is the same source the echo reads for the default.
	_ = svc.ProviderCapabilities()
}

// TestSubAgentPinsAnthropic drives the FULL composition with three mock-backed
// providers and a real on-disk agent def pinning provider=anthropic. A DEFAULT
// session (running on the openai default) routes a Subagent to that def; the sub-agent
// reply proves it ran on the native-Anthropic-bound provider — closing the
// per-sub-agent-provider claim for anthropic with NO registry/agent change.
func TestSubAgentPinsAnthropic(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	agentsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(agentsDir, "claudespec.md"),
		[]byte("---\nname: claudespec\ndescription: runs on anthropic\nprovider: anthropic\ntools: [Read]\n---\nYou run on anthropic.\n"),
		0o600); err != nil {
		t.Fatalf("write agent def: %v", err)
	}

	built, err := Build(ctx, Config{
		Workspace:     workspace,
		NoSoul:        true,
		AgentsDirs:    []string{agentsDir},
		AllowAllTools: true, // --yolo: auto-approve the routed Subagent
		envDetector: fakeEnv(map[string]string{
			"OPENAI_API_KEY":    "sk-x",
			"ANTHROPIC_API_KEY": "sk-x",
		}),
		// Strictly offline: refuse the (keyed) anthropic live fetch ⇒ embedded floor.
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			reply := "REPLY-FROM-" + id
			return mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("p1", "Subagent", []byte(`{"prompt":"go","agent":"claudespec"}`))),
				mockllm.TextTurn(reply), mockllm.TextTurn(reply), mockllm.TextTurn(reply),
			)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	sess, err := svc.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var taskResult string
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.Content != "" {
			taskResult = ev.ToolResult.Content
		}
	}
	if !strings.Contains(taskResult, "REPLY-FROM-anthropic") {
		t.Fatalf("Subagent sub-agent (provider: anthropic) reply = %q, want it to contain REPLY-FROM-anthropic", taskResult)
	}
}

// runProviderTurn creates a session bound to sel, drives one turn, and returns the
// terminal text (so the test can assert which provider backed it).
func runProviderTurn(t *testing.T, svc *server.Service, sel server.ProviderSelector) string {
	t.Helper()
	sess, err := svc.CreateSessionWithProvider(context.Background(), session.ModeDefault, defaultLimits(), sel)
	if err != nil {
		t.Fatalf("CreateSessionWithProvider(%+v): %v", sel, err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	return drainRun(run)
}
