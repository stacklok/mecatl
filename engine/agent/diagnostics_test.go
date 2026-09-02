package agent_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// --- capturing Diagnostics double -------------------------------------------

// diagRecord is one captured Diagnostics line: the level, message, and the
// flattened key/value attributes (bound-via-With keys merged with per-call args).
type diagRecord struct {
	level Level
	msg   string
	attrs map[string]any
}

// Level mirrors port.Level for readable assertions without re-importing the alias.
type Level = port.Level

// capturingDiag is a concurrency-safe port.Diagnostics that records every line,
// carrying the With-bound attributes onto each record so a test can assert the
// run-scoped correlation keys ("session", "agent"). With returns a CHILD that
// shares the same backing record slice (so all lines land in one place) but
// carries its own bound attributes — exactly the per-run binding shape.
type capturingDiag struct {
	mu      *sync.Mutex
	records *[]diagRecord
	bound   map[string]any
}

func newCapturingDiag() *capturingDiag {
	return &capturingDiag{
		mu:      &sync.Mutex{},
		records: &[]diagRecord{},
		bound:   map[string]any{},
	}
}

func (c *capturingDiag) Log(_ context.Context, level port.Level, msg string, args ...any) {
	attrs := make(map[string]any, len(c.bound)+len(args)/2)
	for k, v := range c.bound {
		attrs[k] = v
	}
	for i := 0; i+1 < len(args); i += 2 {
		if k, ok := args[i].(string); ok {
			attrs[k] = args[i+1]
		}
	}
	c.mu.Lock()
	*c.records = append(*c.records, diagRecord{level: level, msg: msg, attrs: attrs})
	c.mu.Unlock()
}

func (c *capturingDiag) With(args ...any) port.Diagnostics {
	bound := make(map[string]any, len(c.bound)+len(args)/2)
	for k, v := range c.bound {
		bound[k] = v
	}
	for i := 0; i+1 < len(args); i += 2 {
		if k, ok := args[i].(string); ok {
			bound[k] = args[i+1]
		}
	}
	return &capturingDiag{mu: c.mu, records: c.records, bound: bound}
}

// snapshot returns a copy of the captured records (safe to read after the run).
func (c *capturingDiag) snapshot() []diagRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]diagRecord, len(*c.records))
	copy(out, *c.records)
	return out
}

// --- a denying policy double ------------------------------------------------

// denyPolicy is a port.PermissionPolicy that denies every call with a fixed
// reason, so a test can drive the policy-deny diagnostics emitter deterministically
// without depending on a specific rule shape.
type denyPolicy struct{ reason string }

func (p denyPolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) governance.PermissionDecision {
	return governance.PermissionDecision{Effect: governance.Deny, Reason: p.reason}
}
func (denyPolicy) Learn(session.SessionID, session.ToolCall) {}

// failingCompactor always errors, so a test can drive the compaction-failure
// diagnostics emitter and confirm the run still completes uncompacted.
type failingCompactor struct{ err error }

func (f failingCompactor) Compact(context.Context, *session.Conversation) ([]session.Message, string, error) {
	return nil, "", f.err
}

// --- helpers ----------------------------------------------------------------

// attrString returns the value of key k on a record as a string, or "" if absent.
func attrString(r diagRecord, k string) (string, bool) {
	v, ok := r.attrs[k]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// findMsg returns the first record whose message contains substr, or false.
func findMsg(records []diagRecord, substr string) (diagRecord, bool) {
	for _, r := range records {
		if strings.Contains(r.msg, substr) {
			return r, true
		}
	}
	return diagRecord{}, false
}

// --- tests ------------------------------------------------------------------

// TestRunDiagnosticsCarrySessionKey pins the per-run correlation binding: every
// diagnostics line a MAIN-engine run emits carries the "session" key equal to the
// session id, and (Role=="") carries NO "agent" key.
func TestRunDiagnosticsCarrySessionKey(t *testing.T) {
	diag := newCapturingDiag()
	// A denied tool call guarantees at least one emitted line (the policy-deny INFO).
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("c1", "Edit", `{"path":"a.go"}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("done"),
	)
	e := agent.NewEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, &fakeTool{name: "Edit", readOnly: false, exec: okExec}),
		Policy:      denyPolicy{reason: "no edits in tests"},
		Model:       "m",
		Diagnostics: diag,
	})
	sess := session.New("sess-A", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "edit a.go"}))

	records := diag.snapshot()
	if len(records) == 0 {
		t.Fatal("no diagnostics lines emitted; expected at least the policy-deny line")
	}
	for _, r := range records {
		got, ok := attrString(r, "session")
		if !ok || got != "sess-A" {
			t.Fatalf("line %q: session key = %q (present=%v), want %q", r.msg, got, ok, "sess-A")
		}
		if _, ok := r.attrs["agent"]; ok {
			t.Fatalf("line %q: main-engine run carries an 'agent' key %v, want none (Role==\"\")", r.msg, r.attrs["agent"])
		}
	}
}

// TestChildRunDiagnosticsCarryAgentRole pins that a child engine (Role set) tags
// every emitted line with both "session" and "agent"=role.
func TestChildRunDiagnosticsCarryAgentRole(t *testing.T) {
	diag := newCapturingDiag()
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("c1", "Edit", `{"path":"a.go"}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("done"),
	)
	e := agent.NewEngine(agent.Deps{
		LLM:         llm,
		Catalog:     catalogWith(t, &fakeTool{name: "Edit", readOnly: false, exec: okExec}),
		Policy:      denyPolicy{reason: "no edits"},
		Model:       "m",
		Diagnostics: diag,
		Role:        "member:explorer",
	})
	sess := session.New("sess-child", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "edit a.go"}))

	records := diag.snapshot()
	if len(records) == 0 {
		t.Fatal("no diagnostics lines emitted; expected at least the policy-deny line")
	}
	for _, r := range records {
		if got, _ := attrString(r, "session"); got != "sess-child" {
			t.Fatalf("line %q: session key = %q, want %q", r.msg, got, "sess-child")
		}
		if got, ok := attrString(r, "agent"); !ok || got != "member:explorer" {
			t.Fatalf("line %q: agent key = %q (present=%v), want %q", r.msg, got, ok, "member:explorer")
		}
	}
}

// denyThenDoneProvider is a STATELESS port.LLMProvider: it inspects the request
// history and yields a tool-call turn when there is no prior tool result, else a
// "done" text turn. Being stateless (no shared cursor), it is deterministic under
// ANY interleaving of concurrent runs on a SHARED engine — exactly what the
// cross-tag test needs. Each run then deterministically does: turn 0 (tool call →
// policy deny → deny result fed back) → turn 1 (history ends with a tool result →
// "done"), producing exactly one policy-deny diagnostic line per run.
type denyThenDoneProvider struct{}

func (denyThenDoneProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (denyThenDoneProvider) Stream(_ context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	var sawToolResult bool
	for _, m := range req.Messages {
		if m.Role == session.RoleTool {
			sawToolResult = true
		}
	}
	var chunks []port.Chunk
	if sawToolResult {
		chunks = []port.Chunk{mockllm.TextChunk("done"), mockllm.DoneChunk(session.StopEndTurn)}
	} else {
		chunks = []port.Chunk{
			mockllm.ToolCallChunk(toolCall("c1", "Edit", `{"path":"a.go"}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		}
	}
	return func(yield func(port.Chunk, error) bool) {
		for _, c := range chunks {
			if !yield(c, nil) {
				return
			}
		}
	}, nil
}

// TestRunDiagnosticsNoCrossTag pins the shared-engine cross-tag property: ONE
// *agent.Engine is reused across two runs with different session ids, and no
// captured line ever carries the OTHER run's session id. This is the test that
// catches a regression to construction-time binding — a single shared engine bound
// once at construction could only ever stamp ONE session id, so a second run on it
// would mis-tag (or panic). The per-run With binding in Engine.Run is what makes
// this hold.
//
// It runs the reuse BOTH sequentially (the minimum that catches construction-time
// binding) AND concurrently (two goroutines joined on a WaitGroup, no polling) —
// the capturing double is mutex-safe, so the concurrent leg is sound under -race.
func TestRunDiagnosticsNoCrossTag(t *testing.T) {
	cat := catalogWith(t, &fakeTool{name: "Edit", readOnly: false, exec: okExec})

	// runLeg builds ONE shared engine and drives two runs (sess-X, sess-Y) on it
	// against ONE shared capturing sink, optionally concurrently, then asserts no
	// line is cross-tagged. Each leg gets a fresh engine+sink so the two legs don't
	// pollute each other's assertions.
	runLeg := func(t *testing.T, concurrent bool) {
		t.Helper()
		diag := newCapturingDiag()
		eng := agent.NewEngine(agent.Deps{
			LLM:         denyThenDoneProvider{},
			Catalog:     cat,
			Policy:      denyPolicy{reason: "no edits"},
			Model:       "m",
			Diagnostics: diag,
		})
		sessX := session.New("sess-X", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
		sessY := session.New("sess-Y", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))

		drive := func(s *session.Session) {
			drain(eng.Run(context.Background(), s, agent.MemEnv("/ws"), agent.RunRequest{Text: "edit a.go"}))
		}
		if concurrent {
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); drive(sessX) }()
			go func() { defer wg.Done(); drive(sessY) }()
			wg.Wait()
		} else {
			drive(sessX)
			drive(sessY)
		}

		var sawX, sawY bool
		for _, r := range diag.snapshot() {
			id, ok := attrString(r, "session")
			if !ok {
				t.Fatalf("line %q has no session key", r.msg)
			}
			switch id {
			case "sess-X":
				sawX = true
			case "sess-Y":
				sawY = true
			default:
				t.Fatalf("line %q carries unexpected session key %q (cross-tag bleed)", r.msg, id)
			}
		}
		if !sawX || !sawY {
			t.Fatalf("expected a policy-deny line for BOTH sessions; sawX=%v sawY=%v", sawX, sawY)
		}
	}

	t.Run("sequential-reuse", func(t *testing.T) { runLeg(t, false) })
	t.Run("concurrent-reuse", func(t *testing.T) { runLeg(t, true) })
}

// TestCompactionFailureEmitsWarn drives a failing compactor and asserts a WARN line
// with the error key fires AND the run still completes (uncompacted), proving the
// emitter surfaces degraded mode without changing the best-effort behaviour.
func TestCompactionFailureEmitsWarn(t *testing.T) {
	diag := newCapturingDiag()
	boom := errors.New("compactor boom")
	llm := mockllm.New(mockllm.TextTurn("done"))
	e := agent.NewEngine(agent.Deps{
		LLM:             llm,
		Catalog:         catalogWith(t),
		Policy:          allowAll(),
		Model:           "m",
		Compactor:       failingCompactor{err: boom},
		ContextWindow:   func() int { return 10 }, // tiny: a long prompt blows past 80% of it
		CompactionRatio: 0.8,
		Diagnostics:     diag,
	})
	sess := session.New("sess-compact", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	bigPrompt := strings.Repeat("word ", 200) // far over the threshold
	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: bigPrompt}))

	// The run still completes cleanly (uncompacted): no compaction event, clean stop.
	if containsType(evs, session.EvCompaction) {
		t.Fatal("compaction event emitted despite a failing compactor; expected none (uncompacted run)")
	}
	res := lastResult(t, evs)
	if res.Stop != session.StopEndTurn {
		t.Fatalf("stop = %q, want end_turn (failed compaction must not abort the run)", res.Stop)
	}

	rec, ok := findMsg(diag.snapshot(), "compaction failed")
	if !ok {
		t.Fatalf("no compaction-failure WARN line; records=%v", diag.snapshot())
	}
	if rec.level != port.LevelWarn {
		t.Fatalf("compaction-failure level = %v, want LevelWarn", rec.level)
	}
	if errv, ok := rec.attrs["error"]; !ok || errv != boom {
		t.Fatalf("compaction-failure line missing error=%v; attrs=%v", boom, rec.attrs)
	}
	if got, _ := attrString(rec, "session"); got != "sess-compact" {
		t.Fatalf("compaction-failure line session = %q, want %q", got, "sess-compact")
	}
}

// failingSaveStore is a port.SessionStore whose Save always fails, counting the
// attempts so a test can assert the WARN is sticky rather than per-save.
type failingSaveStore struct {
	err      error
	attempts int
}

func (s *failingSaveStore) Save(context.Context, *session.Session) error {
	s.attempts++
	return s.err
}

func (*failingSaveStore) Load(_ context.Context, id session.SessionID) (*session.Session, error) {
	return nil, fmt.Errorf("%w: %q", port.ErrSessionNotFound, id)
}

// TestSaveFailureEmitsOneCorrelatedWarn pins the diagnostic that makes a
// persistence failure visible at all. Engine.save used to discard its error, so a
// session the store could not persist — a child whose provider-supplied id the
// store could not name, for instance — vanished with NO line anywhere, and resume
// / InspectSubagent / its event log all silently stopped working.
//
// Two properties, both load-bearing: the line carries the session correlation (a
// child's failure must be attributable), and it fires exactly ONCE per run even
// though save is called on every turn — a broken store is broken for every save,
// and an unconditional log would bury the run in near-identical lines.
func TestSaveFailureEmitsOneCorrelatedWarn(t *testing.T) {
	diag := newCapturingDiag()
	boom := errors.New("store boom")
	store := &failingSaveStore{err: boom}
	// A tool call then a text turn, so the run saves more than once (dispatch
	// persists after the tool result, and the terminal path persists again) —
	// otherwise "sticky" is indistinguishable from "logged once because it only
	// happened once".
	noop := &fakeTool{name: "Noop", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "ok"), nil
		}}
	e := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(
			mockllm.ToolCallTurn(toolCall("c1", "Noop", `{}`)),
			mockllm.TextTurn("done"),
		),
		Catalog:     catalogWith(t, noop),
		Policy:      allowAll(),
		Model:       "m",
		Store:       store,
		Diagnostics: diag,
	})
	sess := session.New("sess-save-fail", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	evs := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	// A failed persist must not abort an otherwise-fine run.
	if res := lastResult(t, evs); res.Stop != session.StopEndTurn {
		t.Fatalf("stop = %q, want end_turn (a failed persist must not abort the run)", res.Stop)
	}
	if store.attempts < 2 {
		t.Fatalf("store.Save attempted %d times; the test needs multiple saves to prove stickiness", store.attempts)
	}

	var warns []diagRecord
	for _, rec := range diag.snapshot() {
		if strings.Contains(rec.msg, "session persistence failed") {
			warns = append(warns, rec)
		}
	}
	if len(warns) != 1 {
		t.Fatalf("got %d persistence WARN lines across %d Save attempts, want exactly 1 (sticky per run)",
			len(warns), store.attempts)
	}
	if warns[0].level != port.LevelWarn {
		t.Fatalf("persistence line level = %v, want LevelWarn", warns[0].level)
	}
	if errv, ok := warns[0].attrs["error"]; !ok || errv != boom {
		t.Fatalf("persistence line missing error=%v; attrs=%v", boom, warns[0].attrs)
	}
	if got, _ := attrString(warns[0], "session"); got != "sess-save-fail" {
		t.Fatalf("persistence line session = %q, want %q", got, "sess-save-fail")
	}
}

// TestPolicyDenyEmitsInfo drives a denied tool call and asserts an INFO line with
// tool+reason fires; an ALLOWED call (separate run, allow-all policy) emits NO
// diagnostic — proving allow/ask are not double-logged onto the operator channel.
func TestPolicyDenyEmitsInfo(t *testing.T) {
	// Denied run.
	denyDiag := newCapturingDiag()
	denyLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("c1", "Edit", `{"path":"a.go"}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("done"),
	)
	e := agent.NewEngine(agent.Deps{
		LLM:         denyLLM,
		Catalog:     catalogWith(t, &fakeTool{name: "Edit", readOnly: false, exec: okExec}),
		Policy:      denyPolicy{reason: "edits are off in tests"},
		Model:       "m",
		Diagnostics: denyDiag,
	})
	sess := session.New("sess-deny", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "edit a.go"}))

	rec, ok := findMsg(denyDiag.snapshot(), "denied by policy")
	if !ok {
		t.Fatalf("no policy-deny INFO line; records=%v", denyDiag.snapshot())
	}
	if rec.level != port.LevelInfo {
		t.Fatalf("policy-deny level = %v, want LevelInfo", rec.level)
	}
	if got, _ := attrString(rec, "tool"); got != "Edit" {
		t.Fatalf("policy-deny tool = %q, want %q", got, "Edit")
	}
	if got, _ := attrString(rec, "reason"); got != "edits are off in tests" {
		t.Fatalf("policy-deny reason = %q, want %q", got, "edits are off in tests")
	}
	// The deny also carries the turn index (the call is denied on the first turn) so
	// an operator can line it up against the event stream.
	turn, ok := rec.attrs["turn"]
	if !ok {
		t.Fatalf("policy-deny line missing 'turn' key; attrs=%v", rec.attrs)
	}
	if turn != 0 {
		t.Fatalf("policy-deny turn = %v, want 0 (denied on the first turn)", turn)
	}

	// Allowed run: an allow-all policy on the same tool must NOT emit any diagnostic.
	allowDiag := newCapturingDiag()
	allowLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.ToolCallChunk(toolCall("c1", "Edit", `{"path":"a.go"}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("done"),
	)
	e2 := agent.NewEngine(agent.Deps{
		LLM:         allowLLM,
		Catalog:     catalogWith(t, &fakeTool{name: "Edit", readOnly: false, exec: okExec}),
		Policy:      allowAll(),
		Model:       "m",
		Diagnostics: allowDiag,
	})
	sess2 := session.New("sess-allow", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	drain(e2.Run(context.Background(), sess2, agent.MemEnv("/ws"), agent.RunRequest{Text: "edit a.go"}))

	if len(allowDiag.snapshot()) != 0 {
		t.Fatalf("allow-all run emitted %d diagnostics lines, want 0 (allow/ask must not double-log)", len(allowDiag.snapshot()))
	}
}
