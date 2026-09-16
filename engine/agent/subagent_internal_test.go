package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestSurfaceAskAttribution proves the parentCaps.surfaceAsk closure ATTRIBUTES the
// surfaced parent EvPermissionAsk to its delegation family: a subagent goal, a team
// member name, or a parallel branch label rides the framed Reason (the pre-composed
// requester phrase), while an empty label falls back to the generic "subagent" framing.
// It also asserts the attribution is CLAMPED — a control-character-bearing label arrives
// neutralised (clampPreview strips C0/C1) — and that the raw child args never ride.
func TestSurfaceAskAttribution(t *testing.T) {
	e := NewEngine(Deps{
		LLM:         mockllm.New(mockllm.TextTurn("x")),
		Catalog:     tool.NewCatalog(),
		Policy:      permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:       "m",
		Interactive: true,
	})

	tests := []struct {
		name       string
		label      string
		wantPrefix string
		wantClean  bool // assert no raw control byte survived in the Reason
	}{
		{"subagent goal", `subagent "fix flaky tests"`, `subagent "fix flaky tests" requests approval to run Shell`, false},
		{"team member name", `team member "researcher"`, `team member "researcher" requests approval to run Shell`, false},
		{"parallel branch label", `parallel branch "branch-2"`, `parallel branch "branch-2" requests approval to run Shell`, false},
		{"empty label keeps generic framing", "", "subagent requests approval to run Shell", false},
		{"control-bearing label is neutralised", "team member \"a\x07b\"", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &Run{
				events:    make(chan session.Event, 8),
				asks:      newAskRegistry(),
				ctx:       context.Background(),
				diag:      port.NopDiagnostics{},
				childAsks: newChildAskRouter(),
				children:  newChildRunRegistry(),
			}
			// The surfaced ask now rides the registry's guarded emit (A4c); bind it
			// to this run's channel the way Engine.Run does.
			r.children.emit = func(ev session.Event) { r.emitOrAbort(ev, r.children.emitAbort) }
			caps := e.parentCaps(r, nil, 0)
			if caps.surfaceAsk == nil {
				t.Fatalf("interactive engine must install a surfaceAsk seam")
			}
			child := &Run{events: make(chan session.Event, 1), asks: newAskRegistry()}
			ask := session.PendingAsk{
				AskID:  "child-sess:1:k1",
				Tool:   "Shell",
				Args:   json.RawMessage(`{"command":"cat data.txt"}`),
				Reason: "command substitution requires approval",
			}
			caps.surfaceAsk(ask.AskID, "child-sess", child, ask, tc.label)

			var got session.Event
			select {
			case got = <-r.events:
			default:
				t.Fatalf("surfaceAsk emitted no event")
			}
			if got.Type != session.EvPermissionAsk || got.Ask == nil {
				t.Fatalf("expected a surfaced EvPermissionAsk, got %v", got.Type)
			}
			if tc.wantPrefix != "" && !strings.Contains(got.Ask.Reason, tc.wantPrefix) {
				t.Fatalf("surfaced reason %q must contain %q", got.Ask.Reason, tc.wantPrefix)
			}
			if tc.wantClean && strings.ContainsRune(got.Ask.Reason, '\x07') {
				t.Fatalf("control byte leaked into surfaced reason: %q", got.Ask.Reason)
			}
			// Gauntlet #7: the raw JSON args field is NEVER forwarded (only the framed
			// reason + clamped command preview ride).
			if len(got.Ask.Args) != 0 {
				t.Fatalf("gauntlet #7: raw child args must not ride the surfaced ask; got %q", string(got.Ask.Args))
			}
		})
	}
}

// TestTightenLimit is the unit table for the tighten-only clamp, including the
// inherited==0 (unlimited) branch: a positive override against an unlimited (0) bound
// TIGHTENS to the override; a nil/zero override is a no-op; a higher override never
// loosens. It relies on session.Limits treating 0 as unlimited (see tightenLimit's
// doc) — this table is the regression guard if that zero-semantics ever changes.
func TestTightenLimit(t *testing.T) {
	ptr := func(n int) *int { return &n }
	tests := []struct {
		name      string
		inherited int
		override  *int
		want      int
	}{
		{"unlimited inherited tightens to override", 0, ptr(5), 5},
		{"override lower wins", 5, ptr(3), 3},
		{"override higher does not loosen", 5, ptr(10), 5},
		{"nil override is a no-op", 5, nil, 5},
		{"zero override is a no-op", 5, ptr(0), 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tightenLimit(tc.inherited, tc.override); got != tc.want {
				t.Fatalf("tightenLimit(%d, %v) = %d, want %d", tc.inherited, tc.override, got, tc.want)
			}
		})
	}
}

// TestTightenTeamTokenBudget is the unit table for the exported wire-facing
// tighten-only fold (issue #36): a non-positive request inherits the server
// budget; a positive request applies only when LOWER (a 0 server budget being
// "unlimited", any positive request tightens it). It delegates to tightenLimit —
// this table guards the delegation against drifting into a second algorithm.
func TestTightenTeamTokenBudget(t *testing.T) {
	tests := []struct {
		name    string
		server  int
		request int
		want    int
	}{
		{"request below server tightens", 1000, 500, 500},
		{"request above server is capped", 1000, 2000, 1000},
		{"request equal to server keeps server", 1000, 1000, 1000},
		{"zero request inherits server", 1000, 0, 1000},
		{"unlimited server tightens to request", 0, 500, 500},
		{"both zero stays disabled", 0, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TightenTeamTokenBudget(tc.server, tc.request); got != tc.want {
				t.Fatalf("TightenTeamTokenBudget(%d, %d) = %d, want %d", tc.server, tc.request, got, tc.want)
			}
		})
	}
}

// TestBuildSubagentRunRequestFloor is the focused unit test for the per-call token budget
// floor: buildSubagentRunRequest must clamp a positive value below MinSubagentRunTokens up
// to MinSubagentRunTokens, pass through values at or above the floor unchanged, and leave
// a zero/absent budget as zero (inherit/unlimited — never raised to the floor).
func TestBuildSubagentRunRequestFloor(t *testing.T) {
	ptr := func(n int) *int { return &n }

	tests := []struct {
		name         string
		args         subagentArgs
		wantOverride int // want MaxRunTokensOverride; 0 = unset (inherit)
	}{
		{
			name:         "below floor is raised to MinSubagentRunTokens",
			args:         subagentArgs{Prompt: "p", MaxRunTokens: ptr(6_000)},
			wantOverride: MinSubagentRunTokens,
		},
		{
			name:         "value of 1 is raised to MinSubagentRunTokens",
			args:         subagentArgs{Prompt: "p", MaxRunTokens: ptr(1)},
			wantOverride: MinSubagentRunTokens,
		},
		{
			name:         "exact floor value passes through unchanged",
			args:         subagentArgs{Prompt: "p", MaxRunTokens: ptr(MinSubagentRunTokens)},
			wantOverride: MinSubagentRunTokens,
		},
		{
			name:         "above floor passes through unchanged",
			args:         subagentArgs{Prompt: "p", MaxRunTokens: ptr(MinSubagentRunTokens + 10_000)},
			wantOverride: MinSubagentRunTokens + 10_000,
		},
		{
			name:         "zero is unset (inherit) — not raised to floor",
			args:         subagentArgs{Prompt: "p", MaxRunTokens: ptr(0)},
			wantOverride: 0,
		},
		{
			name:         "nil is unset (inherit) — not raised to floor",
			args:         subagentArgs{Prompt: "p"},
			wantOverride: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, _, _ := buildSubagentRunRequest(tc.args, false, resumePosture{}, "")
			if req.MaxRunTokensOverride != tc.wantOverride {
				t.Fatalf("buildSubagentRunRequest(%+v) MaxRunTokensOverride = %d, want %d",
					tc.args, req.MaxRunTokensOverride, tc.wantOverride)
			}
		})
	}
}

// TestSubmitResultSpecCarriesRetryAffordance pins the retry affordance in the
// SubmitResult description (Execute's own correction path tells the model to "call
// SubmitResult again"; the description must agree, not contradict it with "exactly
// once").
func TestSubmitResultSpecCarriesRetryAffordance(t *testing.T) {
	desc := newSubmitResultTool(json.RawMessage(`{"type":"object"}`)).Spec().Description
	if !strings.Contains(desc, "call SubmitResult again") {
		t.Fatalf("SubmitResult description must carry the retry affordance %q; got:\n%s", "call SubmitResult again", desc)
	}
}

// TestDriveChildStructuredPlainTextExhaustsToCleanTerminal is the ADVERSARIAL exhaustion
// case the e2e tests do NOT cover (QA MUST #1 + #2): an output_schema IS set but the
// child returns PLAIN TEXT on every attempt and NEVER calls SubmitResult, so
// submit.valid() stays false across the bounded correction re-drives. It drives the child
// DIRECTLY (this is package agent) so it can assert the STRONG terminal guarantee (QA #2):
// driveChild returns StopStructuredOutput, AND the underlying child session ends COMPLETED
// (a CLEAN terminal — each plain-text drive ends StopEndTurn → completed) and is
// Reopen-recoverable. A regression to StopError / a failed session fails this test.
func TestDriveChildStructuredPlainTextExhaustsToCleanTerminal(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	// The child emits ONLY plain text — never a SubmitResult call — across every attempt
	// (script more turns than the 1+defaultStructuredOutputRetries attempts can consume).
	var turns []mockllm.Turn
	for i := 0; i < 1+defaultStructuredOutputRetries+2; i++ {
		turns = append(turns, mockllm.TextTurn("here is a plain text answer, ignoring SubmitResult"))
	}
	engine := NewEngine(Deps{
		LLM:     mockllm.New(turns...),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "child-model",
	})

	ws := memfs.NewWorkspace("/ws")
	childID := session.SessionID("subagent-c1")
	child := session.New(childID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ws.Root(), Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	submit := newSubmitResultTool(schema)
	call := session.NewToolCall("c1", subagentToolName, nil)

	_, stop, _, _, _ := driveChild(context.Background(), engine, child, testEnvironment(ws, nil),
		"profile someone", RunRequest{ExtraTools: []tool.Tool{submit}},
		nil, call, childID, childPosture{}, submit, schema)

	if stop != session.StopStructuredOutput {
		t.Fatalf("driveChild stop = %q, want %q (plain-text-only exhaustion)", stop, session.StopStructuredOutput)
	}
	if submit.valid() {
		t.Fatal("submit.valid() must stay false (the child never called SubmitResult)")
	}
	// The child must have been re-driven the full bounded number of times (a plain-text
	// turn ends each attempt, then a Reopen+correction re-drives — never an infinite loop).
	if got := engine.deps.LLM.(*mockllm.Provider).Calls(); got != 1+defaultStructuredOutputRetries {
		t.Fatalf("child made %d model calls, want %d (initial + bounded corrections)", got, 1+defaultStructuredOutputRetries)
	}
	// STRONG terminal guarantee: the child session is a CLEAN COMPLETED terminal (never
	// failed), so it is Reopen-recoverable — exactly like StopBudget. A regression to
	// StopError/failed would make the session non-recoverable and fail here.
	if child.State != session.StateCompleted {
		t.Fatalf("child session state = %q, want completed (clean terminal, not failed)", child.State)
	}
	if err := child.Reopen(); err != nil {
		t.Fatalf("Reopen after structured-output exhaustion: %v (the terminal must stay recoverable)", err)
	}
}

// TestDriveChildStructuredRetryPreservesMaxRunTokensOverride pins the RunRequest
// copy in driveChild's structured-output retry loop. Each plain-text attempt spends
// 150 tokens without calling SubmitResult. The per-call 250-token ceiling must survive
// the correction re-drives, so the third attempt stops at its first boundary after two
// model calls. Reconstructing the retry request from scratch drops the override, consumes
// the third scripted turn, and ends StopStructuredOutput instead.
func TestDriveChildStructuredRetryPreservesMaxRunTokensOverride(t *testing.T) {
	const budget = 250
	attempt := func(text string) mockllm.Turn {
		return mockllm.ChunksTurn(
			mockllm.TextChunk(text),
			mockllm.UsageChunk(session.Usage{InputTokens: 90, OutputTokens: 60}),
			mockllm.DoneChunk(session.StopEndTurn),
		)
	}
	llm := mockllm.New(attempt("miss one"), attempt("miss two"), attempt("must not run"))
	engine := NewEngine(Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "child-model",
	})
	ws := memfs.NewWorkspace("/ws")
	childID := session.SessionID("subagent-budget-copy")
	child := session.New(childID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ws.Root(), Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	schema := json.RawMessage(`{"type":"object","required":["answer"]}`)
	submit := newSubmitResultTool(schema)
	call := session.NewToolCall("c-budget-copy", subagentToolName, nil)

	_, stop, _, usage, _ := driveChild(context.Background(), engine, child, testEnvironment(ws, nil),
		"return structured output",
		RunRequest{MaxRunTokensOverride: budget, ExtraTools: []tool.Tool{submit}},
		nil, call, childID, childPosture{}, submit, schema)

	if stop != session.StopBudget {
		t.Fatalf("driveChild stop = %q, want %q (retry must preserve the per-call ceiling)", stop, session.StopBudget)
	}
	if got := llm.Calls(); got != 2 {
		t.Fatalf("child made %d model calls, want 2 (third retry must stop at the preserved budget)", got)
	}
	if got := usage.TotalTokens(); got != 300 {
		t.Fatalf("driveChild usage = %d, want 300 accumulated across two attempts", got)
	}
}

// TestSalvageEmptyStopPreservesMaxRunTokensOverride pins the same request-copy
// discipline on the free-text salvage path. The working turn reaches MaxTurns after
// spending above the per-call ceiling. Because non-budget stops deliberately preserve
// session usage, the salvage request must retain that ceiling and stop before consuming
// its scripted summary. Rebuilding the salvage request without the override runs it.
func TestSalvageEmptyStopPreservesMaxRunTokensOverride(t *testing.T) {
	const budget = 250
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(session.NewToolCall("k1", "Read", json.RawMessage(`{}`))),
			mockllm.UsageChunk(session.Usage{InputTokens: 180, OutputTokens: 120}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("SALVAGE MUST NOT RUN"),
	)
	readTool := &fakeOverlayTool{name: "Read", schema: json.RawMessage(`{"type":"object"}`)}
	catalog := tool.NewCatalog()
	catalog.MustRegister(readTool)
	engine := NewEngine(Deps{
		LLM:     llm,
		Catalog: catalog,
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "child-model",
	})
	ws := memfs.NewWorkspace("/ws")
	childID := session.SessionID("subagent-salvage-budget-copy")
	child := session.New(childID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ws.Root(), Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Now())
	call := session.NewToolCall("c-salvage-copy", subagentToolName, nil)

	final, stop, _, usage, _ := driveChild(context.Background(), engine, child, testEnvironment(ws, nil),
		"investigate", RunRequest{MaxRunTokensOverride: budget},
		nil, call, childID, childPosture{}, nil, nil)

	if stop != session.StopMaxTurns {
		t.Fatalf("driveChild stop = %q, want %q", stop, session.StopMaxTurns)
	}
	if got := llm.Calls(); got != 1 {
		t.Fatalf("child made %d model calls, want 1 (salvage must retain the spent per-call ceiling)", got)
	}
	if strings.Contains(final, "SALVAGE MUST NOT RUN") {
		t.Fatalf("salvage ran after dropping the per-call override: %q", final)
	}
	if got := usage.TotalTokens(); got != 300 {
		t.Fatalf("driveChild usage = %d, want 300 from the working turn only", got)
	}
}

// TestSubmitResultOverlayWinsAndIsAdvertised is the focused RunRequest overlay test (QA
// SHOULD #6): a run-scoped ExtraTool whose name COLLIDES with a catalog tool of the same
// name must (a) win via lookupTool (overlay-first resolution) and (b) be advertised by
// buildRequest with the OVERLAY's spec, so the advertised set and dispatch resolution
// never disagree. It exercises both seams directly (package agent).
func TestSubmitResultOverlayWinsAndIsAdvertised(t *testing.T) {
	// A catalog tool named SubmitResult with a DISTINCT (decoy) schema.
	decoy := &fakeOverlayTool{name: submitResultToolName, schema: json.RawMessage(`{"type":"object","title":"DECOY"}`)}
	cat := tool.NewCatalog()
	cat.MustRegister(decoy)
	engine := NewEngine(Deps{
		LLM:     mockllm.New(),
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "m",
	})

	overlaySchema := json.RawMessage(`{"type":"object","title":"OVERLAY"}`)
	submit := newSubmitResultTool(overlaySchema)
	r := &Run{req: RunRequest{ExtraTools: []tool.Tool{submit}}, diag: engine.deps.Diagnostics}

	// (a) lookupTool resolves the OVERLAY, not the catalog decoy.
	got, ok := engine.lookupTool(r, submitResultToolName)
	if !ok {
		t.Fatal("lookupTool must resolve the overlay tool")
	}
	if _, isSubmit := got.(*submitResultTool); !isSubmit {
		t.Fatalf("lookupTool returned %T, want the overlay *submitResultTool (overlay-first)", got)
	}

	// (b) buildRequest advertises the OVERLAY's spec for the colliding name, exactly once.
	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	req := engine.buildRequest(context.Background(), r, sess, memEnv("/ws"))
	count, sawOverlay := 0, false
	for _, spec := range req.Tools {
		if spec.Name == submitResultToolName {
			count++
			if strings.Contains(string(spec.Schema), "OVERLAY") {
				sawOverlay = true
			}
		}
	}
	if count != 1 {
		t.Fatalf("SubmitResult advertised %d times, want exactly 1 (overlay replaces the catalog spec)", count)
	}
	if !sawOverlay {
		t.Fatal("buildRequest must advertise the OVERLAY schema, not the catalog decoy")
	}
}

func TestExtraToolAuthorityExemptionDefaultsToRestricted(t *testing.T) {
	extra := &fakeOverlayTool{name: "RunControl", schema: json.RawMessage(`{"type":"object"}`)}
	req := RunRequest{ExtraTools: []tool.Tool{extra}}
	if req.extraToolAuthorityExempt(extra.Spec().Name) {
		t.Fatal("an extra tool must require delegated authority by default")
	}
	req.extraToolOptions = map[string]extraToolOptions{
		extra.Spec().Name: {AuthorityExempt: true},
	}
	if !req.extraToolAuthorityExempt(extra.Spec().Name) {
		t.Fatal("runtime-provided authority exemption was not applied")
	}
}

// fakeOverlayTool is a trivial read-only tool with a controllable name + schema, used to
// stand in as a catalog tool whose name collides with a run-scoped ExtraTool.
type fakeOverlayTool struct {
	name   string
	schema json.RawMessage
}

func (f *fakeOverlayTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: f.name, Description: "decoy", Schema: f.schema}
}
func (*fakeOverlayTool) ReadOnly() bool { return true }
func (*fakeOverlayTool) Execute(_ context.Context, c session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(c.ID, "decoy"), nil
}
