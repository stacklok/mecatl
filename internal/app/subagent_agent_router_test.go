package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// subagent_agent_router_test.go is the composition e2e for issue #286: routing an UNPINNED
// agent-def delegation through the REAL Build → Service, and pinning that a `model: inherit`
// def is NOT routed. All offline (mockllm shared-cursor + the providerConstructor seam).

// reqRec records one observed LLM request's model + rendered system prompt.
type reqRec struct{ model, system string }

// writeAgentDefFile writes a minimal agent-def markdown file. An empty modelLine leaves the
// def UNPINNED (routable); a non-empty one pins it. body is the def's prompt (used as a
// scoped-rebuild marker).
func writeAgentDefFile(t *testing.T, dir, name, modelLine, body string) {
	t.Helper()
	fm := "---\nname: " + name + "\ndescription: " + name + " specialist\n"
	if modelLine != "" {
		fm += "model: " + modelLine + "\n"
	}
	fm += "---\n" + body
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(fm), 0o644); err != nil {
		t.Fatalf("write def %s: %v", name, err)
	}
}

// routerTaxonomyCategories is the small/large taxonomy the e2es share (large → routerLarge).
func routerTaxonomyCategories() []permconfig.RouterCategory {
	return []permconfig.RouterCategory{
		{Name: "small", Description: "trivial mechanical tasks", Model: routerSmall},
		{Name: "large", Description: "deep multi-step reasoning", Model: routerLarge},
	}
}

// TestRoutableDefRoutesToClassifiedModelE2E: an UNPINNED def, delegated via `agent`, IS
// classified and its SCOPED engine is rebuilt on the routed model. The routed child's
// request carries BOTH the routed model AND the def's scoped body (proving the def rebuild,
// not the generic explorer). The shared cursor serialises: parent → classifier → child →
// parent (4 requests — the classifier turn proves the def was routed).
func TestRoutableDefRoutesToClassifiedModelE2E(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	agentsDir := t.TempDir()
	const bodyMarker = "REVIEWER-DEF-SCOPED-BODY-MARKER"
	writeAgentDefFile(t, agentsDir, "reviewer", "" /* unpinned → routable */, bodyMarker)

	var (
		mu   sync.Mutex
		reqs []reqRec
	)
	built, err := buildIsolated(t, ctx, Config{
		Workspace:             ws,
		NoSoul:                true,
		Model:                 "gpt-5",
		AgentsDirs:            []string{agentsDir},
		RouterCategories:      routerTaxonomyCategories(),
		RouterDefaultCategory: "small",
		AllowAllTools:         true,
		GuardrailsDisabled:    true,
		envDetector:           fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient:   offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
				mu.Lock()
				reqs = append(reqs, reqRec{r.Model, r.System.Render()})
				mu.Unlock()
			})},
				mockllm.ToolCallTurn(session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"review the diff","agent":"reviewer"}`))),
				mockllm.TextTurn(`{"category":"large"}`), // the classifier verdict
				mockllm.TextTurn("CHILD SUMMARY"),        // the routed child's turn
				mockllm.TextTurn("parent done"),
			)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainRun(run); got != "parent done" {
		t.Fatalf("terminal text = %q, want parent done", got)
	}

	mu.Lock()
	defer mu.Unlock()
	// parent (Subagent call) → classifier → routed child → parent (final). 4 requests proves
	// the classifier fired for the routable def (a non-routable one would skip it → 3).
	if len(reqs) != 4 {
		t.Fatalf("recorded %d requests (%+v), want 4 (parent→classifier→child→parent)", len(reqs), reqs)
	}
	if reqs[2].model != routerLarge {
		t.Fatalf("routed child request model = %q, want the classified model %q", reqs[2].model, routerLarge)
	}
	if !strings.Contains(reqs[2].system, bodyMarker) {
		t.Fatalf("routed child must run the def's SCOPED prompt (body marker %q); system=%q", bodyMarker, reqs[2].system)
	}
}

// TestPinnedInheritDefDoesNotRouteE2E: a `model: inherit` def is PINNED (issue #286 — any
// non-empty def.Model is expressed intent), so an `agent` delegation to it is NOT routed —
// no classifier turn — and the child inherits the session model. The script has NO
// classifier turn; a fired classifier would consume the child turn and desync the run.
func TestPinnedInheritDefDoesNotRouteE2E(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	agentsDir := t.TempDir()
	writeAgentDefFile(t, agentsDir, "pinned", "inherit" /* pinned */, "PINNED-DEF-BODY")

	var (
		mu   sync.Mutex
		reqs []reqRec
	)
	built, err := buildIsolated(t, ctx, Config{
		Workspace:             ws,
		NoSoul:                true,
		Model:                 "gpt-5",
		AgentsDirs:            []string{agentsDir},
		RouterCategories:      routerTaxonomyCategories(), // router ON, but the pinned def must not route
		RouterDefaultCategory: "small",
		AllowAllTools:         true,
		GuardrailsDisabled:    true,
		envDetector:           fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient:   offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			// NO classifier turn: parent → child → parent. If the pinned def routed, the
			// classifier would eat the child turn and the run would desync.
			return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
				mu.Lock()
				reqs = append(reqs, reqRec{r.Model, r.System.Render()})
				mu.Unlock()
			})},
				mockllm.ToolCallTurn(session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"review","agent":"pinned"}`))),
				mockllm.TextTurn("CHILD SUMMARY"),
				mockllm.TextTurn("parent done"),
			)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainRun(run); got != "parent done" {
		t.Fatalf("terminal text = %q, want parent done (a fired classifier would desync)", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 3 {
		t.Fatalf("recorded %d requests (%+v), want 3 (parent→child→parent, NO classifier)", len(reqs), reqs)
	}
	// The child (index 1) inherits the session model (inherit → parent), and NO request
	// carried a routed model.
	if reqs[1].model != "gpt-5" {
		t.Fatalf("pinned-inherit child model = %q, want the inherited session model gpt-5", reqs[1].model)
	}
	for i, r := range reqs {
		if r.model == routerLarge {
			t.Fatalf("no request may carry the routed model for a PINNED def; reqs[%d]=%q", i, r.model)
		}
	}
}

// TestRoutableDefHallucinatedCategoryFailSoftE2E (ADVERSARIAL): the classifier names an
// UNLISTED category for a routable def → the delegation FAILS SOFT to the pre-built def
// engine (session model) AND the WP2 per-miss INFO fires with reason "unknown-category".
func TestRoutableDefHallucinatedCategoryFailSoftE2E(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	agentsDir := t.TempDir()
	writeAgentDefFile(t, agentsDir, "reviewer", "" /* unpinned → routable */, "REVIEWER-BODY")

	diag := &kvDiag{}
	var (
		mu   sync.Mutex
		reqs []reqRec
	)
	built, err := buildIsolated(t, ctx, Config{
		Workspace:             ws,
		NoSoul:                true,
		Model:                 "gpt-5",
		AgentsDirs:            []string{agentsDir},
		RouterCategories:      routerTaxonomyCategories(),
		RouterDefaultCategory: "small",
		AllowAllTools:         true,
		GuardrailsDisabled:    true,
		Diagnostics:           diag,
		envDetector:           fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
		liveModelHTTPClient:   offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
				mu.Lock()
				reqs = append(reqs, reqRec{r.Model, r.System.Render()})
				mu.Unlock()
			})},
				mockllm.ToolCallTurn(session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"review","agent":"reviewer"}`))),
				mockllm.TextTurn(`{"category":"ghost-category"}`), // hallucinated (unlisted)
				mockllm.TextTurn("CHILD SUMMARY"),                 // fail-soft pre-built def child
				mockllm.TextTurn("parent done"),
			)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if got := drainRun(run); got != "parent done" {
		t.Fatalf("terminal text = %q, want parent done (a hallucinated category must fail soft, not error)", got)
	}

	mu.Lock()
	defer mu.Unlock()
	// parent → classifier(hallucination) → fail-soft child → parent. The child ran the
	// pre-built def engine on the session model (the unpinned def's default), NOT a routed model.
	if len(reqs) != 4 {
		t.Fatalf("recorded %d requests (%+v), want 4 (parent→classifier→child→parent)", len(reqs), reqs)
	}
	if reqs[2].model != "gpt-5" {
		t.Fatalf("fail-soft child model = %q, want the pre-built def engine's session model gpt-5", reqs[2].model)
	}
	for i, r := range reqs {
		if r.model == routerLarge {
			t.Fatalf("a hallucinated category must NOT route; reqs[%d]=%q", i, r.model)
		}
	}
	// The WP2 per-miss INFO fired with reason "unknown-category".
	if !diag.hasMissReason("classification MISSED", "unknown-category") {
		t.Fatalf("expected a per-miss INFO with reason=unknown-category; msgs=%v args=%v", diag.msgs, diag.args)
	}
}

// hasMissReason reports whether the recorder holds a log line whose message CONTAINS msgSub
// and whose args carry a "reason" key equal to want.
func (d *kvDiag) hasMissReason(msgSub, want string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, m := range d.msgs {
		if !strings.Contains(m, msgSub) {
			continue
		}
		args := d.args[i]
		for j := 0; j+1 < len(args); j += 2 {
			if k, ok := args[j].(string); ok && k == "reason" {
				if v, ok := args[j+1].(string); ok && v == want {
					return true
				}
			}
		}
	}
	return false
}
