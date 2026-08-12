package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// TestNormalizeAskReviewerModelFailFast pins the loud-misconfig posture of the
// --subagent-ask-reviewer normalization, mirroring normalizeSubagentModel:
// empty = off, an unknown bare token or an inherit-alias is a BUILD error, a
// concrete id / mapped alias passes verbatim, and UseMock skips validation.
func TestNormalizeAskReviewerModelFailFast(t *testing.T) {
	if got, err := normalizeAskReviewerModel(Config{}); err != nil || got != "" {
		t.Fatalf("empty must be off: got %q err %v", got, err)
	}
	if _, err := normalizeAskReviewerModel(Config{SubagentAskReviewerModel: "bogus"}); err == nil {
		t.Fatalf("an unknown bare token must fail fast")
	}
	if _, err := normalizeAskReviewerModel(Config{SubagentAskReviewerModel: "sonnet"}); err == nil {
		t.Fatalf("a builtin inherit-alias must fail fast (the reviewer needs a concrete model)")
	}
	if got, err := normalizeAskReviewerModel(Config{SubagentAskReviewerModel: "gpt-5-mini"}); err != nil || got != "gpt-5-mini" {
		t.Fatalf("a concrete id must pass verbatim: got %q err %v", got, err)
	}
	aliased := Config{SubagentAskReviewerModel: "cheap", ModelAliases: map[string]string{"cheap": "gpt-5-mini"}}
	if got, err := normalizeAskReviewerModel(aliased); err != nil || got != "cheap" {
		t.Fatalf("a mapped alias must pass VERBATIM (resolved per session): got %q err %v", got, err)
	}
	if got, err := normalizeAskReviewerModel(Config{UseMock: true, SubagentAskReviewerModel: "bogus"}); err != nil || got != "bogus" {
		t.Fatalf("UseMock must skip validation: got %q err %v", got, err)
	}
}

// TestAskReviewerInertWarningWhenInteractive pins the runtime-discoverability
// fix (finding 0): a reviewer configured on an INTERACTIVE deployment is never
// consulted (asks surface to the client), so normalize WARNS that it is inert
// (no "ACTIVE" fact). It still VALIDATES the model alias first (finding-3 polish),
// so a typo is caught at config time rather than the day --headless is added.
func TestAskReviewerInertWarningWhenInteractive(t *testing.T) {
	diag := &capturingDiag{}
	got, err := normalizeAskReviewerModel(Config{
		Interactive:              true,
		SubagentAskReviewerModel: "gpt-5-mini",
		Diagnostics:              diag,
	})
	if err != nil || got != "gpt-5-mini" {
		t.Fatalf("a valid interactive reviewer config must not error: got %q err %v", got, err)
	}
	if !diag.has("INERT") || diag.has("ACTIVE") {
		t.Fatalf("an interactive deployment must WARN inert (not ACTIVE); lines=%v", diag.lines)
	}
	// A typo'd alias FAILS FAST even when inert (interactive) — caught now, not the
	// day someone adds --headless.
	if _, err := normalizeAskReviewerModel(Config{Interactive: true, SubagentAskReviewerModel: "bogus"}); err == nil {
		t.Fatalf("an unknown alias must fail fast even on an interactive (inert) deployment")
	}
	// And on a headless deployment it narrates ACTIVE, not inert.
	diag2 := &capturingDiag{}
	if _, err := normalizeAskReviewerModel(Config{SubagentAskReviewerModel: "gpt-5-mini", Diagnostics: diag2}); err != nil {
		t.Fatalf("headless reviewer config: %v", err)
	}
	if !diag2.has("ACTIVE") || diag2.has("INERT") {
		t.Fatalf("a headless deployment must narrate ACTIVE, not inert; lines=%v", diag2.lines)
	}
}

// TestAskReviewerEngineDisablesNoProgressNudge is the finding-2 pin: the
// composition-built reviewer engine disables the no-progress nudge, so an EMPTY
// (verdict-less) reviewer turn ends in EXACTLY ONE provider call (and the caller
// therefore denies) rather than being nudged into a second call.
func TestAskReviewerEngineDisablesNoProgressNudge(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
	)
	observe := func(port.LLMRequest) {
		mu.Lock()
		calls++
		mu.Unlock()
	}
	// A reviewer LLM that returns an EMPTY turn (no text, no tool call): pre-fix
	// this would draw a no-progress nudge + a second call.
	reviewerLLM := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(observe)},
		mockllm.TextTurn(""), mockllm.TextTurn(""))
	cfg := Config{Model: "m", UseMock: true, SubagentAskReviewerModel: "reviewer-model"}
	reviewer := buildAskAdjudicator(cfg, nil, reviewerLLM, "mock", "m")
	if reviewer == nil {
		t.Fatalf("reviewer must be built")
	}
	if _, err := reviewer.Review(context.Background(), agent.ChildAskReviewRequest{
		Ask: session.PendingAsk{Tool: "Bash", Args: json.RawMessage(`{"command":"ls"}`)},
	}); err == nil {
		t.Fatalf("an empty reviewer turn must yield a failure (fail-safe deny), not a verdict")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("an empty reviewer turn must end in exactly ONE provider call (nudge disabled); calls=%d", calls)
	}
}

// capturingDiag records Log message substrings for the reachability assertions.
type capturingDiag struct {
	mu    sync.Mutex
	lines []string
}

func (d *capturingDiag) Log(_ context.Context, _ port.Level, msg string, _ ...any) {
	d.mu.Lock()
	d.lines = append(d.lines, msg)
	d.mu.Unlock()
}
func (d *capturingDiag) With(...any) port.Diagnostics { return d }
func (d *capturingDiag) has(sub string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, l := range d.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// count reports how many captured lines contain sub (for the build-once
// exactly-N-facts assertions).
func (d *capturingDiag) count(sub string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, l := range d.lines {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// TestAskAdjudicatorDepsShape pins the reviewer engine's deps literal — the
// per-session re-derivation seam (childExplorerDeps precedent): the SESSION's
// provider, the ALIAS-RESOLVED reviewer model, the "ask-reviewer" role (the
// roleFamily "child" bucket), a tool-less catalog, and — the no-nesting pin —
// a nil ChildAskReviewer even though the parent cfg configures one.
func TestAskAdjudicatorDepsShape(t *testing.T) {
	provider := mockllm.New(mockllm.TextTurn("x"))
	cfg := Config{
		Model:                    "session-model",
		SubagentAskReviewerModel: "cheap",
		ModelAliases:             map[string]string{"cheap": "cheap-model-id"},
	}
	deps, ok := askAdjudicatorDeps(cfg, nil, provider, "openai", "session-model")
	if !ok {
		t.Fatalf("a configured reviewer must yield deps")
	}
	if deps.LLM != provider {
		t.Fatalf("the reviewer must run on the SESSION's provider instance")
	}
	if deps.Model != "cheap-model-id" {
		t.Fatalf("Model = %q, want the alias-resolved reviewer model", deps.Model)
	}
	if deps.Role != "ask-reviewer" {
		t.Fatalf("Role = %q, want ask-reviewer", deps.Role)
	}
	if got := len(deps.Catalog.Specs(session.ModeDefault)); got != 0 {
		t.Fatalf("the reviewer catalog must be tool-less, got %d specs", got)
	}
	if deps.ChildAskReviewer != nil {
		t.Fatalf("the reviewer engine must never carry a nested adjudicator (construct-recursion)")
	}
	if deps.Interactive {
		t.Fatalf("the reviewer engine must be non-interactive")
	}

	if _, ok := askAdjudicatorDeps(Config{Model: "m"}, nil, provider, "openai", "m"); ok {
		t.Fatalf("an empty reviewer model must yield no deps (the reviewer is off)")
	}
}

// TestAttachAskAdjudicatorMainDeps pins the ONE shared assignment helper both
// main-engine sites use: a configured reviewer lands on the deps (with the
// breaker threshold), an unconfigured one leaves them untouched.
func TestAttachAskAdjudicatorMainDeps(t *testing.T) {
	provider := mockllm.New(mockllm.TextTurn("x"))
	base := agent.Deps{LLM: provider, Model: "m"}

	on := attachAskAdjudicator(base, Config{
		Model:                        "m",
		SubagentAskReviewerModel:     "gpt-5-mini",
		SubagentAskReviewerMaxDenies: 5,
	}, nil, provider, "openai", "m")
	if on.ChildAskReviewer == nil {
		t.Fatalf("a configured reviewer must be attached to the MAIN deps")
	}
	if on.ChildAskReviewMaxDenies != 5 {
		t.Fatalf("ChildAskReviewMaxDenies = %d, want 5", on.ChildAskReviewMaxDenies)
	}

	off := attachAskAdjudicator(base, Config{Model: "m"}, nil, provider, "openai", "m")
	if off.ChildAskReviewer != nil {
		t.Fatalf("an unconfigured reviewer must stay nil (the zero-cost default)")
	}
}

// TestChildDepsClearAskAdjudicator pins the no-nesting posture on BOTH child
// deps builders: even with the reviewer configured on cfg, a child engine's
// deps never carry it (children's asks resolve via the PARENT run's caps, and
// the reviewer engine itself builds through this path).
func TestChildDepsClearAskAdjudicator(t *testing.T) {
	provider := mockllm.New(mockllm.TextTurn("x"))
	cfg := Config{Model: "m", SubagentAskReviewerModel: "gpt-5-mini", SubagentAskReviewerMaxDenies: 5}
	pc := promptConfig(cfg, "")

	forProvider := childEngineDepsForProvider(cfg, "task", provider, "m", func() int { return defaultContextWindowTokens }, tool.NewCatalog(), pc, hookexec.New(nil))
	if forProvider.ChildAskReviewer != nil || forProvider.ChildAskReviewMaxDenies != 0 {
		t.Fatalf("childEngineDepsForProvider must clear the adjudicator (no nesting)")
	}
	plain := childEngineDeps(cfg, "task", provider, tool.NewCatalog(), "m", pc, hookexec.New(nil))
	if plain.ChildAskReviewer != nil {
		t.Fatalf("childEngineDeps must not carry the adjudicator")
	}
}

// --- e2e: real engine + supervisor, headless, scripted reviewer-allow -------

// appFakeBash is a recording non-read-only Bash stand-in (the engine test
// fixture, replicated here because test helpers do not cross packages).
type appFakeBash struct {
	mu       sync.Mutex
	executed []string
}

func (*appFakeBash) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Bash", Description: "fake bash",
		Schema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)}
}
func (*appFakeBash) ReadOnly() bool { return false }
func (b *appFakeBash) Execute(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
	var args struct {
		Command string `json:"command"`
	}
	_, _ = session.ParseArgs(in, &args)
	b.mu.Lock()
	b.executed = append(b.executed, args.Command)
	b.mu.Unlock()
	return session.NewToolResult(in.ID, "bash ran: "+args.Command), nil
}
func (b *appFakeBash) ran() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.executed...)
}

// appFakeForker hands out in-memory fork workspaces (the engine test fixture's
// shape) so a read-only member can carry Bash.
type appFakeForker struct{}

func (appFakeForker) Fork(_ context.Context, _ tool.Workspace, label string) (tool.Workspace, func() error, string, error) {
	return memfs.NewWorkspace("/fork/" + label), func() error { return nil }, "", nil
}

// TestAskReviewerE2EHeadlessTeamAllow drives the WHOLE wired chain offline: a
// HEADLESS parent engine assembled through the real composition deps builders
// (engineDepsForProvider + attachAskAdjudicator), a Team supervisor member built
// through the real child deps path (childEngineDepsForProvider, which clears the
// nested adjudicator), and the REAL engineAskAdjudicator over a mockllm SCRIPTED
// to allow — the member's substitution-floored Bash (`cat $(zap)`) executes,
// where the pre-#31 posture blanket-denied it.
func TestAskReviewerE2EHeadlessTeamAllow(t *testing.T) {
	bash := &appFakeBash{}
	cfg := Config{
		Model: "m",
		// UseMock skips alias validation; the reviewer model is a literal id the
		// scripted provider ignores anyway.
		UseMock:                  true,
		SubagentAskReviewerModel: "reviewer-model",
	}

	memberFactory := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		cat.MustRegister(bash)
		memberLLM := mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("a1", "Bash", json.RawMessage(`{"command":"cat $(zap)"}`))),
			mockllm.TextTurn("lead: inspected"),
			mockllm.TextTurn("CONSOLIDATED: done"),
		)
		eng := agent.NewEngine(childEngineDepsForProvider(cfg, "member:lead", memberLLM, "m", func() int { return defaultContextWindowTokens }, cat, promptConfig(cfg, ""), nil))
		return agent.MemberBuild{Engine: eng, IsolateReadOnly: true}
	}
	teamTool := agent.NewTeamTool(memberFactory, agent.WithTeamToolReadOnlyForker(appFakeForker{}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("p1", "Team",
			json.RawMessage(`{"goal":"inspect","members":[{"name":"lead","role":"inspect the tree"}]}`))),
		mockllm.TextTurn("parent: done"),
	)
	reviewerLLM := mockllm.New(mockllm.TextTurn(`{"allow": true, "reason": "read-only inspection"}`))

	deps := engineDepsForProvider(cfg, parentLLM, "m", func() int { return defaultContextWindowTokens }, nil, childPermPolicy(cfg), hookexec.New(nil), nil, nil)
	cat := tool.NewCatalog()
	cat.MustRegister(teamTool)
	deps.Catalog = cat
	// The REAL assignment helper, with the reviewer's scripted provider standing
	// in as the session provider it would normally share.
	deps = attachAskAdjudicator(deps, cfg, nil, reviewerLLM, "mock", "m")
	if deps.ChildAskReviewer == nil {
		t.Fatalf("the reviewer must be wired")
	}
	engine := agent.NewEngine(deps) // headless: Interactive false

	sess := session.New("e2e-ask-reviewer", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	run := engine.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "inspect the tree"})

	deadline := time.After(15 * time.Second)
	var last *session.ResultPayload
	for done := false; !done; {
		select {
		case ev, ok := <-run.Events():
			if !ok {
				done = true
				break
			}
			if ev.Type == session.EvPermissionAsk {
				t.Fatalf("a headless adjudicated ask must never surface")
			}
			if ev.Type == session.EvResult {
				last = ev.Result
			}
		case <-deadline:
			run.Cancel()
			t.Fatalf("run wedged")
		}
	}

	if last == nil || last.Stop == session.StopError {
		t.Fatalf("run did not complete cleanly: %+v", last)
	}
	if got := bash.ran(); len(got) != 1 || !strings.Contains(got[0], "cat $(zap)") {
		t.Fatalf("the reviewer-allowed substitution-floored Bash must execute; ran=%v", got)
	}
}
