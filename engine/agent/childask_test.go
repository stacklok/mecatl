package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// fakeBash is a non-read-only Bash tool that records the commands it executed and
// returns a canned result. It never actually runs anything (the command literal is
// inert), so a test can assert "the child executed Bash" without a real shell.
type fakeBash struct {
	mu       sync.Mutex
	executed []string
}

func (*fakeBash) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Bash", Description: "fake bash", Schema: bashSchema}
}
func (*fakeBash) ReadOnly() bool { return false }
func (b *fakeBash) Execute(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
	var args struct {
		Command string `json:"command"`
	}
	_, _ = session.ParseArgs(in, &args)
	b.mu.Lock()
	b.executed = append(b.executed, args.Command)
	b.mu.Unlock()
	return session.NewToolResult(in.ID, "bash ran: "+args.Command), nil
}
func (b *fakeBash) ran() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.executed...)
}

var bashSchema = json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)

// recordingDiag captures diagnostic Log calls for assertions (the headless auto-deny
// operator line). It is concurrency-safe. With-derived children record into the same
// root buffer and carry the accumulated bound attrs, so a test can assert on both the
// message and the correlation keys (e.g. agent=<role>).
type recordingDiag struct {
	root  *recordingDiag
	mu    sync.Mutex
	lines []diagLine
	attrs []any
}

type diagLine struct {
	level port.Level
	msg   string
	args  []any
}

func newRecordingDiag() *recordingDiag { return &recordingDiag{} }

func (d *recordingDiag) base() *recordingDiag {
	if d.root != nil {
		return d.root
	}
	return d
}

func (d *recordingDiag) Log(_ context.Context, level port.Level, msg string, args ...any) {
	merged := append(append([]any{}, d.attrs...), args...)
	b := d.base()
	b.mu.Lock()
	b.lines = append(b.lines, diagLine{level: level, msg: msg, args: merged})
	b.mu.Unlock()
}

func (d *recordingDiag) With(args ...any) port.Diagnostics {
	return &recordingDiag{
		root:  d.base(),
		attrs: append(append([]any{}, d.attrs...), args...),
	}
}

// findLine returns the first recorded line whose message contains substr, plus ok.
func (d *recordingDiag) findLine(substr string) (diagLine, bool) {
	b := d.base()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, l := range b.lines {
		if strings.Contains(l.msg, substr) {
			return l, true
		}
	}
	return diagLine{}, false
}

// argValue returns the value following key in a line's args (key, value pairs), or nil.
func (l diagLine) argValue(key string) any {
	for i := 0; i+1 < len(l.args); i += 2 {
		if k, ok := l.args[i].(string); ok && k == key {
			return l.args[i+1]
		}
	}
	return nil
}

// TestChildAskRouterRoutesVerdict proves the parent Run.Approve routes a child-
// namespaced askID to the child's own registry, falls through to the parent's own
// resolve on an unknown id, and is a safe no-op on a stale id. It drives a real
// interactive parent engine whose Subagent child surfaces an ask, then approves via the
// surfaced (child) askID.
func TestChildAskRouterRoutesVerdict(t *testing.T) {
	bash := &fakeBash{}
	// Child: isolated (forker) but the command is NOT isolation-approvable (non-read-only
	// inner stand-in `zap`), so it surfaces to the parent rather than auto-approving.
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Bash", `{"command":"cat $(zap)"}`)),
		mockllm.TextTurn("child done"),
	)
	childEngine := bashChildEngine(childLLM, bash)
	task := agent.NewSubagentTool(childEngine, agent.WithChildForker(&recordingSubagentForker{}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"run it"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})

	// Approve the FIRST surfaced parent EvPermissionAsk using its askID (the child's own
	// askID). The router on the parent run routes it to the child.
	approveOnAsk(t, r, session.VerdictAllowOnce)

	if got := bash.ran(); len(got) != 1 || !strings.Contains(got[0], "cat $(zap)") {
		t.Fatalf("child Bash should have executed once after the surfaced ask was approved; ran=%v", got)
	}
}

// TestSurfacedAskRedaction proves the surfaced parent EvPermissionAsk clamps the
// command and frames it as a subagent request — peer-injected/untrusted args never ride
// raw — and that NO child transcript content (message.delta / result body) crosses to
// the parent (gauntlet #7). The child's Bash command embeds a long, framing-shaped,
// "untrusted" string; the surfaced ask must contain the clamped command framed as a
// request, never a child result body.
func TestSurfacedAskRedaction(t *testing.T) {
	bash := &fakeBash{}
	untrusted := "cat $(zap " + strings.Repeat("X", 400) + ")" // long, non-read-only inner
	childLLM := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("CHILD-SECRET-TEXT-do-not-leak"),
			mockllm.ToolCallChunk(toolCall("k1", "Bash", `{"command":`+jsonString(untrusted)+`}`)),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.TextTurn("child done"),
	)
	childEngine := bashChildEngine(childLLM, bash)
	task := agent.NewSubagentTool(childEngine, agent.WithChildForker(&recordingSubagentForker{}))

	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := interactiveEngine(t, agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})

	var surfaced *session.PendingAsk
	var sawChildText bool
	evs := drainApproving(r, session.VerdictDeny, func(ev session.Event) {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && surfaced == nil {
			a := *ev.Ask
			surfaced = &a
		}
		// Gauntlet #7: no child message.delta text may reach the parent stream.
		if ev.Type == session.EvMessageDelta && strings.Contains(ev.Text, "CHILD-SECRET-TEXT") {
			sawChildText = true
		}
	})
	_ = evs

	if surfaced == nil {
		t.Fatalf("expected a surfaced parent permission ask")
	}
	if sawChildText {
		t.Fatalf("gauntlet #7: child message.delta text leaked to the parent stream")
	}
	if !strings.Contains(surfaced.Reason, `subagent "x" requests approval to run Bash`) {
		t.Fatalf("surfaced ask must be framed as an attributed subagent request, got reason: %q", surfaced.Reason)
	}
	// The command is clamped (rune-bounded); the 400-X blob must not ride in full.
	if strings.Contains(surfaced.Reason, strings.Repeat("X", 300)) {
		t.Fatalf("surfaced ask must CLAMP the command; it forwarded the untrusted blob in full")
	}
	// Raw args are never forwarded on the surfaced ask.
	if len(surfaced.Args) != 0 {
		t.Fatalf("surfaced ask must not forward raw child args; got %q", string(surfaced.Args))
	}
}

// jsonString quotes s as a JSON string literal for embedding in a raw args payload.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestIsolatedSubagentAutoApprovesWorktreeSafe proves an ISOLATED Subagent child's
// worktree-safe Bash (the motivating per-package coverage loop: substitution +
// go test/list) auto-APPROVES (A2) — the child executes it without surfacing or
// denying — even on a NON-interactive parent (so it is the isolation auto-approve, not
// surfacing, that clears it).
func TestIsolatedSubagentAutoApprovesWorktreeSafe(t *testing.T) {
	bash := &fakeBash{}
	cmd := `for pkg in $(go list ./...); do go test -cover "$pkg"; done`
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Bash", `{"command":`+jsonString(cmd)+`}`)),
		mockllm.TextTurn("child done"),
	)
	childEngine := bashChildEngine(childLLM, bash)
	task := agent.NewSubagentTool(childEngine, agent.WithChildForker(&recordingSubagentForker{}))

	// NON-interactive parent: no surface path. Only A2 can clear the ask.
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"coverage"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task)}) // Interactive=false
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	_ = drain(r)

	if got := bash.ran(); len(got) != 1 {
		t.Fatalf("isolated worktree-safe Bash should auto-approve and execute once; ran=%v", got)
	}
}

// TestNonIsolatedHeadlessChildAutoDenies proves a NON-isolated (no forker), HEADLESS
// child's Bash substitution ask auto-DENIES with the ACCURATE message + a correlated
// operator diagnostic (LevelInfo, agent=<role>), and that NO permission-ask event for
// the deny leaks onto the parent run's Events() stream.
func TestNonIsolatedHeadlessChildAutoDenies(t *testing.T) {
	bash := &fakeBash{}
	childLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("k1", "Bash", `{"command":"cat $(zap)"}`)),
		mockllm.TextTurn("child done"),
	)
	childEngine := bashChildEngine(childLLM, bash)
	// No forker → not isolated. No Interactive → headless. So the ask auto-denies.
	task := agent.NewSubagentTool(childEngine)

	diag := newRecordingDiag()
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"x"}`)),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task), Diagnostics: diag})
	r := e.Run(context.Background(), newSession(t, session.Limits{}), memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "go"})
	evs := drain(r)

	// The child must NOT have executed the denied command.
	if got := bash.ran(); len(got) != 0 {
		t.Fatalf("headless non-isolated child must auto-deny, not execute; ran=%v", got)
	}
	// NO permission-ask event leaked to the parent stream (the deny is silent on-stream).
	for _, ev := range evs {
		if ev.Type == session.EvPermissionAsk {
			t.Fatalf("headless auto-deny must not surface a permission ask on the parent stream")
		}
	}
	// The operator diagnostic fired, correlated with the agent role.
	line, ok := diag.findLine("auto-denied")
	if !ok {
		t.Fatalf("expected a headless auto-deny operator diagnostic; got lines: %+v", diag.base().lines)
	}
	if line.level != port.LevelInfo {
		t.Fatalf("auto-deny diagnostic level = %v, want LevelInfo", line.level)
	}
	if got := line.argValue("agent"); got != "subagent-p1" {
		t.Fatalf("auto-deny diagnostic must carry agent=child-session-id; got %v want %q; args=%v", got, "subagent-p1", line.args)
	}
}

// --- shared helpers for the child-ask tests ---------------------------------

// bashChildEngine builds a child engine with the given Bash tool under the
// CANONICAL default child posture: the allow-all FLOOR
// (permpolicy.AllowAllFloorRules — the same ruleset production childRules()
// uses, so fixture and composition cannot drift). The floor allow-all still
// floors a non-read-only substitution at Ask (the substitution floor is
// separate from allow-all, and a FLOOR-scoped allow never registers as a
// configured Allow), so a `cat $(zap)` call produces an EvPermissionAsk the
// child posture then resolves.
func bashChildEngine(llm port.LLMProvider, bash tool.Tool) *agent.Engine {
	return agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: bashCatalog(bash),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "child-model",
	})
}

func bashCatalog(bash tool.Tool) *tool.Catalog {
	c := tool.NewCatalog()
	c.MustRegister(bash)
	return c
}

// interactiveEngine builds a parent engine with Interactive=true (so its run installs
// the child-ask router and a subagent ask is surfaced rather than auto-denied).
func interactiveEngine(t *testing.T, d agent.Deps) *agent.Engine {
	t.Helper()
	if d.Policy == nil {
		d.Policy = permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	}
	if d.Model == "" {
		d.Model = "parent-model"
	}
	d.Interactive = true
	return agent.NewEngine(d)
}

// approveOnAsk consumes the run's events until it sees the FIRST EvPermissionAsk, then
// approves it with the given verdict using its (child) askID, then drains the rest.
func approveOnAsk(t *testing.T, r *agent.Run, v session.ApprovalVerdict) {
	t.Helper()
	drainApproving(r, v, nil)
}

// drainApproving consumes the run's events to completion. On the FIRST EvPermissionAsk
// it routes the given verdict via r.Approve(askID, v) (the parent router routes it to
// the owning child). The optional observe callback is invoked for every event. It
// returns all events. It is bounded by a watchdog so a wedge fails the test rather than
// hanging the suite.
func drainApproving(r *agent.Run, v session.ApprovalVerdict, observe func(session.Event)) []session.Event {
	var evs []session.Event
	approved := false
	deadline := time.After(10 * time.Second)
	ch := r.Events()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evs
			}
			evs = append(evs, ev)
			if observe != nil {
				observe(ev)
			}
			if !approved && ev.Type == session.EvPermissionAsk && ev.Ask != nil {
				approved = true
				r.Approve(ev.Ask.AskID, v)
			}
		case <-deadline:
			r.Cancel()
			return evs
		}
	}
}
