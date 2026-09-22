package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// collectParallel filters the parent run's emitted events down to the parallel.* family,
// preserving order. Used by every observability assert below.
func collectParallel(evs []session.Event) []*session.ParallelPayload {
	var out []*session.ParallelPayload
	for _, ev := range evs {
		switch ev.Type {
		case session.EvParallelStart, session.EvParallelBranch, session.EvParallelEnd:
			out = append(out, ev.Parallel)
		}
	}
	return out
}

// TestParallelEmitsObservabilityStreamAll is the model-facing e2e for join=all: drive a
// REAL Parallel call over a mockllm child engine + memfs forks, drain the PARENT run, and
// assert the redacted parallel.* events bracket the run and every branch — exactly one
// parallel.start (join+count), one branch_start + ≥0 branch_tool + one branch_end PER
// branch (keyed by index), and one parallel.end with Winner=-1 (join=all has no winner).
func TestParallelEmitsObservabilityStreamAll(t *testing.T) {
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "child read something"), nil
		}}
	childEngine := childEngineWith(&branchProvider{summary: "branch summary X"}, catalogWith(t, childRead))

	mf := &memForker{}
	parallel := agent.NewParallelTool(childEngine, mf)
	parentCat := catalogWith(t, parallel)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Parallel",
			`{"tasks":["explore A","explore B","explore C"],"shared":"common"}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)
	ps := collectParallel(evs)
	if len(ps) == 0 {
		t.Fatalf("no parallel.* events emitted; got %v", typesOf(evs))
	}

	// Exactly one start, attributed to the Parallel call, with join + count.
	var starts, ends int
	for _, ev := range evs {
		switch ev.Type {
		case session.EvParallelStart:
			starts++
			if ev.Parallel.ParentCallID != "p1" {
				t.Fatalf("start parentCallID=%q want p1", ev.Parallel.ParentCallID)
			}
			if ev.Parallel.Join != "all" || ev.Parallel.BranchCount != 3 {
				t.Fatalf("start join=%q count=%d want all/3", ev.Parallel.Join, ev.Parallel.BranchCount)
			}
		case session.EvParallelEnd:
			ends++
			if ev.Parallel.Winner != -1 {
				t.Fatalf("join=all winner=%d want -1", ev.Parallel.Winner)
			}
			if ev.Parallel.BranchCount != 3 {
				t.Fatalf("end count=%d want 3", ev.Parallel.BranchCount)
			}
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("starts=%d ends=%d want 1/1", starts, ends)
	}

	// Per branch: exactly one branch_start + one branch_end.
	bstart := map[int]int{}
	bend := map[int]int{}
	for _, p := range ps {
		switch p.Kind {
		case session.ParallelBranchStart:
			bstart[p.BranchIndex]++
			if p.BranchLabel == "" {
				t.Fatalf("branch_start idx=%d missing label", p.BranchIndex)
			}
		case session.ParallelBranchEnd:
			bend[p.BranchIndex]++
		}
	}
	for i := 0; i < 3; i++ {
		if bstart[i] != 1 || bend[i] != 1 {
			t.Fatalf("branch %d: starts=%d ends=%d want 1/1", i, bstart[i], bend[i])
		}
	}
}

// TestParallelEmitsWinnerJudge drives join=judge where the WINNING branch is NOT index 0
// and asserts parallel.end.Winner carries the REAL branch index (off-by-one guard).
func TestParallelEmitsWinnerJudge(t *testing.T) {
	childEngine := childEngineWith(&routingBranchProvider{summaries: map[string]string{
		"alpha": "alpha result",
		"beta":  "WINNINGRESULT beta",
		"gamma": "gamma result",
	}}, tool.NewCatalog())
	lf := newLabeledForker()
	judge := &fakeJudge{pick: "WINNINGRESULT", rationale: "beta is best"}
	parallel := agent.NewParallelTool(childEngine, lf, agent.WithParallelConcurrency(1), agent.WithParallelJudge(judge))
	parentCat := catalogWith(t, parallel)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Parallel",
			`{"tasks":["do alpha","do beta","do gamma"],"join":"judge","criteria":"pick beta"}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	var end *session.ParallelPayload
	for _, ev := range evs {
		if ev.Type == session.EvParallelEnd {
			end = ev.Parallel
		}
	}
	if end == nil {
		t.Fatalf("no parallel.end emitted; got %v", typesOf(evs))
	}
	if end.Join != "judge" {
		t.Fatalf("end join=%q want judge", end.Join)
	}
	if end.Winner != 1 {
		t.Fatalf("winner=%d want 1 (branch beta, the non-zero index)", end.Winner)
	}
}

// TestParallelEmitsWinnerFirst drives join=first and asserts parallel.end carries a real
// winner index belonging to the succeeding set.
func TestParallelEmitsWinnerFirst(t *testing.T) {
	childEngine := childEngineWith(&routingBranchProvider{summaries: map[string]string{
		"one": "one done", "two": "two done",
	}}, tool.NewCatalog())
	lf := newLabeledForker()
	parallel := agent.NewParallelTool(childEngine, lf, agent.WithParallelConcurrency(2))
	parentCat := catalogWith(t, parallel)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Parallel",
			`{"tasks":["do one","do two"],"join":"first"}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	var end *session.ParallelPayload
	for _, ev := range evs {
		if ev.Type == session.EvParallelEnd {
			end = ev.Parallel
		}
	}
	if end == nil {
		t.Fatalf("no parallel.end emitted; got %v", typesOf(evs))
	}
	if end.Join != "first" {
		t.Fatalf("end join=%q want first", end.Join)
	}
	// The first-success winner is decided by completion ORDER, which is genuinely
	// non-deterministic here (both branches succeed). Assert membership in {0,1}.
	if end.Winner != 0 && end.Winner != 1 {
		t.Fatalf("join=first winner=%d want a succeeding branch index (0 or 1)", end.Winner)
	}
}

// TestParallelBranchErrorRepresented is the adversarial case: one branch's child fails
// (fork failure) and another succeeds. Assert the failed branch's branch_end carries
// Failed=true, every branch still emits a coherent branch_end (no missing event, no
// hang), and the run still ends with exactly one parallel.end.
func TestParallelBranchErrorRepresented(t *testing.T) {
	childEngine := childEngineWith(&branchProvider{summary: "ok branch summary"}, tool.NewCatalog())
	mf := &memForker{failOnLabel: "branch-1"} // first branch's fork fails
	parallel := agent.NewParallelTool(childEngine, mf)
	parentCat := catalogWith(t, parallel)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Parallel", `{"tasks":["fail me","run ok"]}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)
	ps := collectParallel(evs)

	ends := map[int]*session.ParallelPayload{}
	var runEnds int
	for _, p := range ps {
		if p.Kind == session.ParallelBranchEnd {
			ends[p.BranchIndex] = p
		}
	}
	for _, ev := range evs {
		if ev.Type == session.EvParallelEnd {
			runEnds++
		}
	}
	if runEnds != 1 {
		t.Fatalf("runEnds=%d want exactly 1 parallel.end", runEnds)
	}
	if len(ends) != 2 {
		t.Fatalf("got branch_end for %d branches want 2 (every branch represented)", len(ends))
	}
	if ends[0] == nil || !ends[0].Failed {
		t.Fatalf("branch 0 (fork-failed) should have Failed=true: %+v", ends[0])
	}
	if ends[1] == nil || ends[1].Failed {
		t.Fatalf("branch 1 (ok) should have Failed=false: %+v", ends[1])
	}
}

// canaryProvider is a stateless child LLM whose tool RESULT and message TEXT both carry a
// secret canary string, exercising the no-content-leak guarantee: a branch produces
// content that MUST NOT appear in any parallel.* event.
type canaryProvider struct {
	canary string
}

func (*canaryProvider) Capabilities() port.ProviderCapabilities { return port.ProviderCapabilities{} }

func (p *canaryProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	hasResult := false
	for _, m := range req.Messages {
		if m.ToolResult != nil {
			hasResult = true
			break
		}
	}
	var chunks []port.Chunk
	if hasResult {
		chunks = []port.Chunk{
			{Kind: port.ChunkText, Text: "branch said " + p.canary},
			{Kind: port.ChunkUsage, Usage: &session.Usage{}},
			{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		}
	} else {
		// args carry the canary too — the tool.call args must never be forwarded.
		call := toolCall("k", "Read", `{"path":"`+p.canary+`"}`)
		chunks = []port.Chunk{
			{Kind: port.ChunkToolCall, ToolCall: &call},
			{Kind: port.ChunkUsage, Usage: &session.Usage{}},
			{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		}
	}
	return func(yield func(port.Chunk, error) bool) {
		for _, c := range chunks {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if !yield(c, nil) {
				return
			}
		}
	}, nil
}

// TestParallelNoContentLeakBehavioral is the gauntlet-#7 behavioral guard (ADR 0079
// shape): a branch whose child tool args, tool result, and message text all contain a
// sentinel canary longer than the clampPreview cap and laced with control bytes. Assert
// the canary NEVER appears VERBATIM in ANY string field of ANY emitted parallel.* event —
// only its clamped, control-byte-scrubbed prefix may cross, plus the redacted metadata
// (tool names, labels, fork paths).
func TestParallelNoContentLeakBehavioral(t *testing.T) {
	// The head is short enough to survive clamping intact; the tail pushes every
	// content body past the 200-rune cap, so verbatim carriage is what the test
	// disproves.
	canaryHead := "TOPSECRETCANARY-" + strings.Repeat("h", 220)
	canaryTail := "-TAIL-" + strings.Repeat("z", 600)
	canary := canaryHead + canaryTail + "\x1b[7m"
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "result body "+canary), nil
		}}
	childEngine := childEngineWith(&canaryProvider{canary: canary}, catalogWith(t, childRead))
	mf := &memForker{}
	parallel := agent.NewParallelTool(childEngine, mf)
	parentCat := catalogWith(t, parallel)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Parallel", `{"tasks":["task one","task two"]}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	saw := false
	for _, p := range collectParallel(evs) {
		saw = true
		// Scan EVERY string-kinded field by REFLECTION (not a hand-maintained list),
		// so a future payload field is covered the moment it exists — the behavioral
		// sentinel can never go stale against the structural allow-list in
		// TestParallelPayloadHasNoContentFields.
		rv := reflect.ValueOf(*p)
		for i := 0; i < rv.NumField(); i++ {
			f := rv.Field(i)
			if f.Kind() != reflect.String {
				continue
			}
			s := f.String()
			if strings.Contains(s, canaryTail) {
				t.Fatalf("canary leaked UNBOUNDED into parallel.* field %s: %q", rv.Type().Field(i).Name, s)
			}
			for _, c := range s {
				if c < 0x20 || (c >= 0x7f && c <= 0x9f) {
					t.Fatalf("control byte leaked into parallel.* field %s: %q", rv.Type().Field(i).Name, s)
				}
			}
		}
	}
	if !saw {
		t.Fatal("no parallel.* events emitted; the leak guard did not exercise")
	}
	// A branch_tool DID fire carrying a CLAMPED preview: the tool name crossed, the
	// canary head survives only inside a clamped Detail, and the tail never does.
	var sawTool, sawClampedPreview bool
	for _, p := range collectParallel(evs) {
		if p.Kind == session.ParallelBranchTool && p.ToolName == "Read" {
			sawTool = true
		}
		if p.Kind == session.ParallelBranchTool && strings.Contains(p.Detail, "TOPSECRETCANARY-hhh") && !strings.Contains(p.Detail, canaryTail) {
			sawClampedPreview = true
		}
	}
	if !sawTool {
		t.Fatal("expected a branch_tool(Read) event to confirm metadata forwarding")
	}
	if !sawClampedPreview {
		t.Fatal("expected a branch_tool event with a clamped canary-head preview (ADR 0079)")
	}
}

// TestParallelPayloadHasNoContentFields is the gauntlet-#7 STRUCTURAL guard: assert the
// ParallelPayload struct's field set is exactly the documented metadata + bounded-preview
// allow-list (ADR 0079). The preview fields (Text/Detail/InnerKind) are content-shaped but
// are fed ONLY through clampPreview (control-byte scrub + rune cap) at the single
// drainChildObserved chokepoint, and are client-only — never the parent's Conversation.
// Raw-content fields (Args/Content/Summary/FailReason) remain BANNED. This trips if a
// future change adds an unreviewed field.
func TestParallelPayloadHasNoContentFields(t *testing.T) {
	allowed := map[string]bool{
		"ParentCallID": true, "Kind": true, "Join": true, "BranchCount": true,
		"BranchIndex": true, "BranchLabel": true, "Goal": true, "ToolName": true,
		"IsError": true, "ToolCount": true, "Failed": true,
		"Stop": true, "Usage": true, "DurationMs": true, "Winner": true,
		// ChildID is the branch's child SESSION id ("parallel-<callID>-<i>") — a
		// HARNESS-derived addressing handle (the CancelChild target, D16), never
		// branch content: it is composed of the id prefix + the parent call id + the
		// branch index, none of which a branch authors.
		"ChildID":          true,
		"ChildIncarnation": true,
		// RoutedCategory / RoutedModel are the OPT-IN model router's classification for
		// this branch (ADR 0034): a CATEGORY label (operator-authored taxonomy name) and a
		// concrete MODEL id — bare metadata, never the branch prompt, summary, or the
		// classifier's reasoning. Mirrors SubagentPayload's identically-justified routed
		// fields; gauntlet-#7 safe (no branch content crosses).
		"RoutedCategory": true, "RoutedModel": true,
		// RoutingReason (issue #397) is the bare-metadata REASON the branch was not
		// routed: a session.RoutingReason* gate const or the classifier's bounded
		// missReason, EMPTY on a routed hit — never the branch prompt or the
		// classifier's reasoning. Same gauntlet-#7 footing as RoutedCategory/Model.
		"RoutingReason": true,
		// RoutingDecision (ADR 0350) is sanitized bounded classifier metadata only.
		"RoutingDecision": true,
		// Model (issue #112 / ADR 0035) is the concrete MODEL id this branch ACTUALLY ran
		// on, regardless of how it was chosen — bare metadata, never branch content.
		"Model": true,
		// Text / Detail / InnerKind (ADR 0079) are the BOUNDED PREVIEW fields: Text
		// carries a clamped child message/result-text preview, Detail a clamped
		// tool-call-args or tool-result-body preview, InnerKind the inner event kind
		// the preview came from. Both content fields are fed ONLY through clampPreview
		// (control-byte scrub + maxTeamPreview rune cap) at the single
		// drainChildObserved chokepoint (re-tagged by branchTool), are client-only,
		// and never enter the parent's Conversation — the behavioral canary suite
		// (TestParallelNoContentLeakBehavioral) proves the raw body never crosses.
		"Text": true, "Detail": true, "InnerKind": true,
	}
	rt := reflect.TypeOf(session.ParallelPayload{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !allowed[f.Name] {
			t.Fatalf("ParallelPayload grew an unexpected field %q (%s): a new field MUST be reviewed "+
				"against gauntlet #7 — branch content may cross only as a clampPreview-bounded, "+
				"client-only preview (ADR 0079). If it is legitimate redacted metadata or a bounded "+
				"preview, add it to the allow-list with a justification.", f.Name, f.Type)
		}
	}
	// Spot-check: RAW content fields remain banned — only the clampPreview-fed preview
	// fields (Text/Detail) may carry branch-derived text.
	for _, banned := range []string{"Args", "Content", "Summary", "FailReason"} {
		if _, ok := rt.FieldByName(banned); ok {
			t.Fatalf("ParallelPayload must not carry a raw content field %q (gauntlet #7)", banned)
		}
	}
}

// TestParallelBranchIDIsNotBranchContent is the gauntlet-#7 guard for the issue-#30
// "branch id:" discoverability line: the branch id surfaced in the result text is
// prefix+callID+index ONLY — it must NOT carry any branch summary/output. A canary string
// baked into the branch's summary must NEVER appear in the surfaced branch id; it may
// appear ONLY in the deliberately-pulled InspectSubagent ToolResult (the parent's explicit
// choice), proving the id stays a pure addressing handle while the content crosses solely
// through the PULL channel.
func TestParallelBranchIDIsNotBranchContent(t *testing.T) {
	const canary = "BRANCHCANARY42"
	store := memstore.New()
	childRead := &fakeTool{name: "Read", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "child read"), nil
		}}
	// The branch's SUMMARY carries the canary.
	childEngine := childEngineWith(&branchProvider{summary: "branch summary " + canary}, catalogWith(t, childRead))
	par := agent.NewParallelTool(childEngine, &memForker{}, agent.WithParallelStore(store))

	res, err := par.Execute(context.Background(),
		session.NewToolCall("c1", "Parallel", json.RawMessage(`{"tasks":["explore"]}`)),
		agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("Parallel.Execute: %v", err)
	}

	// Find the surfaced branch id line and assert it carries NO canary.
	wantID := "parallel-c1-0"
	if !strings.Contains(res.Content, "branch id: "+wantID) {
		t.Fatalf("result missing the branch id line %q:\n%s", wantID, res.Content)
	}
	for _, line := range strings.Split(res.Content, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "branch id: ") && strings.Contains(l, canary) {
			t.Fatalf("branch id line leaked branch content (canary): %q", l)
		}
	}

	// The canary DOES cross only through the deliberate PULL: InspectSubagent on that id.
	inspect := agent.NewInspectSubagentTool(store)
	ires, err := inspect.Execute(context.Background(),
		session.NewToolCall("i1", "InspectSubagent", json.RawMessage(`{"agent_id":"`+wantID+`"}`)),
		agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("InspectSubagent.Execute: %v", err)
	}
	if ires.IsError || !strings.Contains(ires.Content, canary) {
		t.Fatalf("the deliberately-pulled transcript must carry the canary, got %+v", ires)
	}
}

// TestParallelBranchCancelledBeforeStartRepresented covers the cancelledBeforeStart path:
// a branch whose ctx is already cancelled when it tries to acquire a worker slot never
// runs, but MUST still be represented on the stream with exactly one branch_start + one
// branch_end (Failed=true / StopCancelled) — the "every branch represented, no missing
// event" guarantee. Distinct from the fork-failure path (a branch that DID run): here the
// branch never even forks. We force it with concurrency:1 + a child that blocks until the
// PARENT ctx is cancelled, so the queued branches hit the `<-ctx.Done()` arm of the
// worker-slot select before they ever acquire a slot.
func TestParallelBranchCancelledBeforeStartRepresented(t *testing.T) {
	releasedFirst := make(chan struct{})
	// The first branch to run blocks on the parent ctx; while it blocks (concurrency 1),
	// every later branch waits on the semaphore. Cancelling the parent ctx then trips the
	// queued branches' `<-ctx.Done()` slot-acquire arm => cancelledBeforeStart.
	blockingChild := &blockUntilCancelProvider{started: releasedFirst}
	childEngine := childEngineWith(blockingChild, tool.NewCatalog())
	mf := &memForker{}
	parallel := agent.NewParallelTool(childEngine, mf, agent.WithParallelConcurrency(1))
	parentCat := catalogWith(t, parallel)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Parallel", `{"tasks":["block A","queued B","queued C"]}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})

	ctx, cancel := context.WithCancel(context.Background())
	r := e.Run(ctx, newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})

	// Cancel once the first branch is in flight, so the queued branches are cancelled at
	// the slot-acquire select (never run, never fork).
	go func() {
		<-releasedFirst
		cancel()
	}()
	evs := drain(r)
	ps := collectParallel(evs)

	starts := map[int]int{}
	ends := map[int]*session.ParallelPayload{}
	for _, p := range ps {
		switch p.Kind {
		case session.ParallelBranchStart:
			starts[p.BranchIndex]++
		case session.ParallelBranchEnd:
			ends[p.BranchIndex] = p
		}
	}
	// At least one of the queued branches (index 1 or 2) must have been cancelled before
	// start — and EVERY branch index must still carry exactly one start + one end.
	cancelledBeforeStart := 0
	for i := 0; i < 3; i++ {
		if starts[i] != 1 {
			t.Fatalf("branch %d: branch_start count = %d, want exactly 1 (every branch represented)", i, starts[i])
		}
		end := ends[i]
		if end == nil {
			t.Fatalf("branch %d: missing branch_end (no missing event)", i)
		}
		// A cancelled-before-start branch is Failed + StopCancelled with zero
		// usage/duration/toolcount.
		if end.Failed && end.Stop == session.StopCancelled &&
			end.ToolCount == 0 && end.Usage == (session.Usage{}) && end.DurationMs == 0 {
			cancelledBeforeStart++
		}
		// Even a never-started branch carries its deterministic D16 child id on both
		// bracketing events (cancelledBeforeStart now sets childID: be.branchChildID(i)),
		// so a client can still address it for cancel/inspect.
		wantID := fmt.Sprintf("parallel-s1-p1-%d", i)
		if end.ChildID != wantID {
			t.Errorf("branch %d: branch_end ChildID = %q, want %q (deterministic even when never started)", i, end.ChildID, wantID)
		}
	}
	if cancelledBeforeStart == 0 {
		t.Fatalf("expected ≥1 branch cancelled BEFORE start (Failed/StopCancelled/no fork); got none: %+v", ends)
	}
	// Exactly one parallel.end still fires.
	var runEnds int
	for _, ev := range evs {
		if ev.Type == session.EvParallelEnd {
			runEnds++
		}
	}
	if runEnds != 1 {
		t.Fatalf("runEnds=%d want exactly 1 parallel.end even on cancellation", runEnds)
	}
}

// blockUntilCancelProvider is a stateless child LLM that signals `started` once and then
// blocks until the run ctx is cancelled, yielding a cancelled terminal. It lets a test
// hold the single worker slot (concurrency 1) so later branches are cancelled at the
// slot-acquire select (the cancelledBeforeStart path). `started` is closed exactly once.
type blockUntilCancelProvider struct {
	started chan struct{}
	once    sync.Once
}

func (*blockUntilCancelProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *blockUntilCancelProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.once.Do(func() { close(p.started) })
	return func(yield func(port.Chunk, error) bool) {
		<-ctx.Done()
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopCancelled}, nil)
	}, nil
}

// usageProvider is a stateless child LLM that returns a single text turn carrying a fixed
// NON-ZERO usage, so a test can assert the run-level parallel.end usage is the SUM across
// branches (not zero, not a single branch's usage).
type usageProvider struct {
	usage session.Usage
}

func (*usageProvider) Capabilities() port.ProviderCapabilities { return port.ProviderCapabilities{} }

func (p *usageProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	u := p.usage
	chunks := []port.Chunk{
		{Kind: port.ChunkText, Text: "branch summary"},
		{Kind: port.ChunkUsage, Usage: &u},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}
	return func(yield func(port.Chunk, error) bool) {
		for _, c := range chunks {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if !yield(c, nil) {
				return
			}
		}
	}, nil
}

// TestParallelEndUsageIsBranchSum asserts the run-level parallel.end carries the SUM of
// every branch's usage — guarding sumBranchUsage against a regression to 0 or to a single
// branch's usage (which the mapper round-trip test cannot catch). Three branches each
// report the same known non-zero usage; end.Usage must be 3× it.
func TestParallelEndUsageIsBranchSum(t *testing.T) {
	per := session.Usage{InputTokens: 100, OutputTokens: 30, CacheReadTokens: 10, CacheWriteTokens: 5}
	childEngine := childEngineWith(&usageProvider{usage: per}, tool.NewCatalog())
	mf := &memForker{}
	parallel := agent.NewParallelTool(childEngine, mf)
	parentCat := catalogWith(t, parallel)
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Parallel", `{"tasks":["A","B","C"]}`)),
		mockllm.TextTurn("done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: parentCat})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	var end *session.ParallelPayload
	for _, ev := range evs {
		if ev.Type == session.EvParallelEnd {
			end = ev.Parallel
		}
	}
	if end == nil {
		t.Fatalf("no parallel.end emitted; got %v", typesOf(evs))
	}
	want := session.Usage{}.Add(per).Add(per).Add(per) // 3× the per-branch usage
	if end.Usage != want {
		t.Fatalf("parallel.end usage = %+v, want the 3-branch sum %+v", end.Usage, want)
	}
	// Sanity: the per-branch end events must each carry the single-branch usage (so the
	// sum is genuinely an aggregation, not the same total stamped on every event).
	for _, p := range collectParallel(evs) {
		if p.Kind == session.ParallelBranchEnd && p.Usage != per {
			t.Fatalf("branch_end usage = %+v, want the per-branch %+v", p.Usage, per)
		}
	}
}
