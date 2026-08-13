package agent

// Internal-package I3b tests: the TWO-children batched completion notice (one
// harness message for both — needs the registry's doneChs as a deterministic
// "both children terminal" anchor, which only the internal package can reach)
// and the noticeFinishedBackground unit semantics.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// noticeRunHolder hands the parent *Run (created only when the test calls
// e.Run) to a tool that needs its child registry, race-free.
type noticeRunHolder struct {
	ready chan struct{}
	run   *Run
}

// awaitChildrenDoneTool parks until every listed child id has a CLOSED doneCh
// in the parent run's registry — the deterministic "all background children are
// terminal in the registry" anchor a scripted parent turn sequences on, so the
// NEXT boundary's notice batches them all. Registration of each id may lag this
// tool's start (the children spawn concurrently), so it polls doneChFor until
// the entry exists, then joins it; ctx-bounded throughout.
type awaitChildrenDoneTool struct {
	holder *noticeRunHolder
	ids    []string
}

func (*awaitChildrenDoneTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "AwaitAllDone", Description: "parks until the listed children are terminal",
		Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*awaitChildrenDoneTool) ReadOnly() bool { return true }
func (a *awaitChildrenDoneTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	select {
	case <-a.holder.ready:
	case <-ctx.Done():
		return session.NewToolResult(in.ID, "cancelled"), nil
	}
	for _, id := range a.ids {
		var ch <-chan struct{}
		for {
			var ok bool
			if ch, ok = a.holder.run.children.doneChFor(id); ok {
				break
			}
			select {
			case <-ctx.Done():
				return session.NewToolResult(in.ID, "cancelled"), nil
			case <-time.After(time.Millisecond):
			}
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return session.NewToolResult(in.ID, "cancelled"), nil
		}
	}
	return session.NewToolResult(in.ID, "all children terminal"), nil
}

// TestBackgroundNoticeBatchesTwoFinishedChildren drives the REAL loop with two
// background children that are BOTH terminal before the next boundary: the
// boundary injects exactly ONE notice message naming both ids (id-sorted) with
// their stop labels — never one message per child. The results stay uncollected
// through the run's end, pinning the documented disposition: the run-end drain
// has nothing to cancel (they are done) and the uncollected results simply die
// with the registry while the persisted children remain inspectable.
func TestBackgroundNoticeBatchesTwoFinishedChildren(t *testing.T) {
	childLLM := mockllm.New(
		mockllm.TextTurn("child one findings"),
		mockllm.TextTurn("child two findings"),
	)
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	childEngine := NewEngine(Deps{LLM: childLLM, Catalog: tool.NewCatalog(), Policy: allow, Model: "child-model"})
	task := NewSubagentTool(childEngine)

	holder := &noticeRunHolder{ready: make(chan struct{})}
	await := &awaitChildrenDoneTool{holder: holder, ids: []string{"subagent-notice-batch-p1", "subagent-notice-batch-p2"}}

	mkCall := func(id, args string) session.ToolCall {
		return session.NewToolCall(session.ToolCallID(id), "Subagent", json.RawMessage(args))
	}
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(
			mkCall("p1", `{"prompt":"first","background":true}`),
			mkCall("p2", `{"prompt":"second","background":true}`),
			session.NewToolCall("pw", "AwaitAllDone", json.RawMessage(`{}`)),
		),
		mockllm.TextTurn("parent done"),
	)
	cat := tool.NewCatalog()
	cat.MustRegister(task)
	cat.MustRegister(NewSubagentStatusTool())
	cat.MustRegister(await)
	e := NewEngine(Deps{LLM: parentLLM, Catalog: cat, Policy: allow, Model: "parent-model"})

	sess := session.New("notice-batch", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	r := e.Run(context.Background(), sess, memEnv("/ws"), RunRequest{Text: "go"})
	holder.run = r
	close(holder.ready)

	deadline := time.After(10 * time.Second)
	for {
		stop := false
		select {
		case _, ok := <-r.Events():
			if !ok {
				stop = true
			}
		case <-deadline:
			r.Cancel()
			t.Fatalf("run did not terminate (possible wedge)")
		}
		if stop {
			break
		}
	}

	want := "[harness note: 2 background subagent(s) finished: subagent-notice-batch-p1 (end_turn), subagent-notice-batch-p2 (end_turn). " +
		"Collect each result with SubagentStatus before relying on it.]"
	var notices []string
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && len(m.Text) > 0 && m.Text[0] == '[' {
			notices = append(notices, m.Text)
		}
	}
	if len(notices) != 1 || notices[0] != want {
		t.Fatalf("expected exactly ONE batched notice %q, got %v", want, notices)
	}
	if sess.State != session.StateCompleted {
		t.Fatalf("run must complete cleanly (done children never block the end), got %q", sess.State)
	}
	// The never-collected results are still sitting in the (now sealed) registry —
	// collectible up to the run's end, simply dropped with it.
	for _, id := range []string{"subagent-notice-batch-p1", "subagent-notice-batch-p2"} {
		if res, _, outcome := r.children.collect(id); outcome != collectOK || res == nil {
			t.Fatalf("child %s result must have remained uncollected (still stored), got outcome %v", id, outcome)
		}
	}
}

// TestNoticeFinishedBackgroundSemantics is the registry unit for the A2
// bookkeeping: only DONE+BACKGROUND+!noticed entries are candidates; delivered
// candidates are marked noticed but NOT returned; nothing is ever re-noticed;
// the returned slice is id-sorted; and a noticed result remains collectible.
func TestNoticeFinishedBackgroundSemantics(t *testing.T) {
	g := newChildRunRegistry()
	noCancel := func() {}

	// Foreground done child: never noticed (its result returned inline).
	g.register("subagent-fg", childFamilySubagent, "g", noCancel, false)
	g.markDone("subagent-fg", session.StopEndTurn)
	// Running background child: not yet a candidate.
	g.register("subagent-running", childFamilySubagent, "g", noCancel, true)
	g.markRunning("subagent-running")
	// Two finished background children, registered out of id order.
	resB := session.NewToolResult("b", "body B")
	g.register("subagent-b", childFamilySubagent, "g", noCancel, true)
	g.markDoneResult("subagent-b", session.StopBudget, &resB)
	resA := session.NewToolResult("a", "body A")
	g.register("subagent-a", childFamilySubagent, "g", noCancel, true)
	g.markDoneResult("subagent-a", session.StopEndTurn, &resA)
	// A finished background child ALREADY collected: noticed silently, not listed.
	resC := session.NewToolResult("c", "body C")
	g.register("subagent-c", childFamilySubagent, "g", noCancel, true)
	g.markDoneResult("subagent-c", session.StopEndTurn, &resC)
	if _, _, outcome := g.collect("subagent-c"); outcome != collectOK {
		t.Fatalf("setup: collect c failed: %v", outcome)
	}

	first := g.noticeFinishedBackground()
	if len(first) != 2 || first[0].id != "subagent-a" || first[1].id != "subagent-b" {
		t.Fatalf("want id-sorted [subagent-a subagent-b], got %+v", first)
	}
	if first[0].stop != session.StopEndTurn || first[1].stop != session.StopBudget {
		t.Fatalf("statuses must carry each child's stop, got %+v", first)
	}
	if got := backgroundNoticeText(first); got !=
		"[harness note: 2 background subagent(s) finished: subagent-a (end_turn), subagent-b (budget). "+
			"Collect each result with SubagentStatus before relying on it.]" {
		t.Fatalf("notice text mismatch: %q", got)
	}

	// FAMILY-AWARE rendering (bash-cmd jobs): a finished bash job joins the
	// notice under its own clause naming BashStatus; the subagent clause keeps
	// its byte-stable wording; a bash-only notice has no subagent clause at all.
	mixed := append(append([]childStatus(nil), first...),
		childStatus{id: "bashcmd-b1", family: childFamilyBashCmd, background: true, state: childDone, stop: session.StopEndTurn})
	if got := backgroundNoticeText(mixed); got !=
		"[harness note: 2 background subagent(s) finished: subagent-a (end_turn), subagent-b (budget). "+
			"Collect each result with SubagentStatus before relying on it.]"+
			"[harness note: 1 background command(s) finished: bashcmd-b1 (end_turn). "+
			"Collect each output with BashStatus before relying on it.]" {
		t.Fatalf("mixed-family notice mismatch: %q", got)
	}
	bashOnly := []childStatus{{id: "bashcmd-b2", family: childFamilyBashCmd, background: true, state: childDone, stop: session.StopError}}
	if got := backgroundNoticeText(bashOnly); got !=
		"[harness note: 1 background command(s) finished: bashcmd-b2 (error). "+
			"Collect each output with BashStatus before relying on it.]" {
		t.Fatalf("bash-only notice mismatch: %q", got)
	}

	// Never re-noticed — including the silently-noticed delivered child.
	if second := g.noticeFinishedBackground(); len(second) != 0 {
		t.Fatalf("a second scan must return nothing, got %+v", second)
	}

	// noticed ≠ delivered: the noticed results are still collectible exactly once.
	if res, _, outcome := g.collect("subagent-a"); outcome != collectOK || res == nil || res.Content != "body A" {
		t.Fatalf("noticed result must remain collectible, got %v (%+v)", outcome, res)
	}
	if _, _, outcome := g.collect("subagent-a"); outcome != collectAlready {
		t.Fatalf("second collect must report already-delivered, got %v", outcome)
	}

	// The running child becomes a candidate only at its terminal.
	resR := session.NewToolResult("r", "body R")
	g.markDoneResult("subagent-running", session.StopCancelled, &resR)
	if third := g.noticeFinishedBackground(); len(third) != 1 || third[0].id != "subagent-running" || third[0].stop != session.StopCancelled {
		t.Fatalf("the late terminal must be noticed on the next scan, got %+v", third)
	}
}
