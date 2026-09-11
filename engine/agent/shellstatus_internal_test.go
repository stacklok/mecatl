package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// shellStatusCall builds a ShellStatus tool call with the given JSON args.
func shellStatusCall(id string, args map[string]any) session.ToolCall {
	raw, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return session.ToolCall{ID: session.ToolCallID(id), Name: shellStatusToolName, Args: raw}
}

// execShellStatus drives ShellStatus through the childCapableTool seam against reg.
func execShellStatus(t *testing.T, reg *childRunRegistry, id string, args map[string]any) session.ToolResult {
	t.Helper()
	res, err := NewShellStatusTool().(childCapableTool).ExecuteWithParent(
		context.Background(), shellStatusCall(id, args), shellEnv, nil, parentCaps{children: reg})
	if err != nil {
		t.Fatalf("ExecuteWithParent err = %v", err)
	}
	return res
}

// TestShellStatusCapsLessError pins the honest no-registry error on the plain
// Execute path (and on ExecuteWithParent with a nil registry).
func TestShellStatusCapsLessError(t *testing.T) {
	res, err := NewShellStatusTool().Execute(context.Background(), shellStatusCall("s1", nil), shellEnv)
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "no live registry is available on this run") {
		t.Fatalf("res = %+v, want the no-registry error", res)
	}
	res, err = NewShellStatusTool().(childCapableTool).ExecuteWithParent(
		context.Background(), shellStatusCall("s2", nil), shellEnv, nil, parentCaps{})
	if err != nil {
		t.Fatalf("ExecuteWithParent err = %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "no live registry is available on this run") {
		t.Fatalf("caps-less ExecuteWithParent res = %+v, want the no-registry error", res)
	}
}

// TestShellStatusRosterShellOnly pins the disjoint projection: the ShellStatus
// roster lists ONLY bash-cmd entries (a delegation child is invisible to it),
// renders ids + state + stop labels, and NEVER leaks the command text (the
// model-authored goal stays out of the bulk roster).
func TestShellStatusRosterShellOnly(t *testing.T) {
	reg := newChildRunRegistry()
	noCancel := func() {}

	// A delegation child and a still-running bash job.
	reg.register("subagent-a", childFamilySubagent, "explore things", noCancel, true)
	reg.register("bashcmd-live", childFamilyShellCmd, "make serve", noCancel, true)
	reg.markRunning("bashcmd-live")
	// A done bash job with an uncollected result.
	done := session.NewToolResult("d", "tail body\n[exit code: 0]")
	reg.register("bashcmd-done", childFamilyShellCmd, "npm run build", noCancel, true)
	reg.markDoneResult("bashcmd-done", session.StopEndTurn, &done)

	// Empty projection for a registry holding ONLY delegation children.
	onlySub := newChildRunRegistry()
	onlySub.register("subagent-a", childFamilySubagent, "g", noCancel, true)
	if res := execShellStatus(t, onlySub, "s1", nil); res.IsError ||
		!strings.Contains(res.Content, "No background commands have been started in this run.") {
		t.Fatalf("delegation-only registry must render the empty roster, got %+v", res)
	}

	res := execShellStatus(t, reg, "s2", nil)
	if res.IsError {
		t.Fatalf("roster is an error: %q", res.Content)
	}
	if !strings.Contains(res.Content, "Background commands of this run (2):") {
		t.Fatalf("roster header mismatch: %q", res.Content)
	}
	if !strings.Contains(res.Content, "- bashcmd-live running") {
		t.Fatalf("roster lacks the live job: %q", res.Content)
	}
	if !strings.Contains(res.Content, "- bashcmd-done done (end_turn) — result ready; collect it with job_id") {
		t.Fatalf("roster lacks the done job: %q", res.Content)
	}
	if strings.Contains(res.Content, "subagent-a") {
		t.Fatalf("roster must exclude the delegation child: %q", res.Content)
	}
	for _, cmd := range []string{"make serve", "npm run build", "explore things"} {
		if strings.Contains(res.Content, cmd) {
			t.Fatalf("roster must never carry command/goal text (%q): %q", cmd, res.Content)
		}
	}
}

// TestShellStatusLiveJobDetail pins the per-job LIVE view: state + the command
// (the deliberate single-job inspection) + the CURRENT tail snapshot — a peek
// that does NOT mark the stored result delivered (a later collect still
// delivers exactly once).
func TestShellStatusLiveJobDetail(t *testing.T) {
	reg := newChildRunRegistry()
	noCancel := func() {}
	reg.register("bashcmd-x", childFamilyShellCmd, "make serve", noCancel, true)
	reg.markRunning("bashcmd-x")
	tail := newTailBuffer(1024)
	reg.attachOutputTail("bashcmd-x", tail)
	if _, err := io.WriteString(tail, "listening on :8080\nserving /ws\n"); err != nil {
		t.Fatalf("tail write: %v", err)
	}

	res := execShellStatus(t, reg, "s1", map[string]any{"job_id": "bashcmd-x"})
	if res.IsError {
		t.Fatalf("live detail is an error: %q", res.Content)
	}
	for _, want := range []string{
		"background command bashcmd-x is running.",
		"command: make serve",
		"--- output so far (tail) ---",
		"listening on :8080",
		"serving /ws",
		"Wait for it with wait_ms",
	} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("live detail lacks %q: %q", want, res.Content)
		}
	}

	// The peek is NOT a delivery: finishing the job leaves its stored result
	// collectible exactly once.
	done := session.NewToolResult("bashcmd-x", "full tail\n[exit code: 0]")
	reg.markDoneResult("bashcmd-x", session.StopEndTurn, &done)
	if _, _, outcome := reg.collect("bashcmd-x"); outcome != collectOK {
		t.Fatalf("after a live peek the result must still deliver, got %v", outcome)
	}
}

// TestShellStatusLiveJobDetailNoOutputYet covers the live job whose tail is
// still empty (and the never-attached tail): an honest "(no output yet)".
func TestShellStatusLiveJobDetailNoOutputYet(t *testing.T) {
	reg := newChildRunRegistry()
	noCancel := func() {}
	reg.register("bashcmd-quiet", childFamilyShellCmd, "sleep 5", noCancel, true)
	reg.markRunning("bashcmd-quiet")
	reg.attachOutputTail("bashcmd-quiet", newTailBuffer(64))
	reg.register("bashcmd-notail", childFamilyShellCmd, "sleep 6", noCancel, true)
	reg.markRunning("bashcmd-notail")

	for _, id := range []string{"bashcmd-quiet", "bashcmd-notail"} {
		res := execShellStatus(t, reg, "s-"+id, map[string]any{"job_id": id})
		if res.IsError || !strings.Contains(res.Content, "(no output yet)") {
			t.Fatalf("%s detail = %+v, want the no-output-yet view", id, res)
		}
	}
}

// TestShellStatusDoneCollectExactlyOnce pins the terminal collection: the first
// job_id read of a done job delivers the stored result body (the error bit
// rides along), the second reports already-delivered, and a delegation id
// routed to ShellStatus is the SAME miss as an unknown id (disjoint doors).
func TestShellStatusDoneCollectExactlyOnce(t *testing.T) {
	reg := newChildRunRegistry()
	noCancel := func() {}

	ok := session.NewToolResult("bashcmd-ok", "build ok\n[exit code: 0]")
	reg.register("bashcmd-ok", childFamilyShellCmd, "make build", noCancel, true)
	reg.markDoneResult("bashcmd-ok", session.StopEndTurn, &ok)

	failed := session.NewToolResult("bashcmd-bad", "boom\n[exit code: 2]")
	failed.IsError = true
	reg.register("bashcmd-bad", childFamilyShellCmd, "make deploy", noCancel, true)
	reg.markDoneResult("bashcmd-bad", session.StopError, &failed)

	// First collect delivers the body verbatim.
	res := execShellStatus(t, reg, "s1", map[string]any{"job_id": "bashcmd-ok"})
	if res.IsError || res.Content != "build ok\n[exit code: 0]" {
		t.Fatalf("first collect = %+v, want the stored body", res)
	}
	// Second collect reports the delivery.
	res = execShellStatus(t, reg, "s2", map[string]any{"job_id": "bashcmd-ok"})
	if res.IsError || !strings.Contains(res.Content, "background command bashcmd-ok (end_turn): result already delivered.") {
		t.Fatalf("second collect = %+v, want already-delivered", res)
	}
	// A stored error body surfaces as an error tool result, still exactly once.
	res = execShellStatus(t, reg, "s3", map[string]any{"job_id": "bashcmd-bad"})
	if !res.IsError || res.Content != "boom\n[exit code: 2]" {
		t.Fatalf("failed-job collect = %+v, want the stored error body", res)
	}
	res = execShellStatus(t, reg, "s4", map[string]any{"job_id": "bashcmd-bad"})
	if res.IsError || !strings.Contains(res.Content, "result already delivered") {
		t.Fatalf("failed-job second collect = %+v, want already-delivered", res)
	}

	// Disjoint projection: a delegation id is the same miss as an unknown id.
	reg.register("subagent-a", childFamilySubagent, "g", noCancel, true)
	reg.markDone("subagent-a", session.StopEndTurn)
	res = execShellStatus(t, reg, "s5", map[string]any{"job_id": "subagent-a"})
	if !res.IsError || !strings.Contains(res.Content, `no background command "subagent-a" in this run`) {
		t.Fatalf("delegation id through ShellStatus = %+v, want the unknown-job error", res)
	}
}

// TestShellStatusUnknownID pins the model-addressable miss for a job_id this
// run never started (roster hint included), and for the wait-targeted miss.
func TestShellStatusUnknownID(t *testing.T) {
	reg := newChildRunRegistry()
	res := execShellStatus(t, reg, "s1", map[string]any{"job_id": "bashcmd-nope"})
	if !res.IsError ||
		!strings.Contains(res.Content, `ShellStatus: no background command "bashcmd-nope" in this run`) ||
		!strings.Contains(res.Content, "call ShellStatus with no arguments for the roster") {
		t.Fatalf("res = %+v, want the unknown-job error with the roster hint", res)
	}
	wait := 1000
	res = execShellStatus(t, reg, "s2", map[string]any{"job_id": "bashcmd-nope", "wait_ms": wait})
	if !res.IsError || !strings.Contains(res.Content, "no background command") {
		t.Fatalf("wait on an unknown id must fail before parking, got %+v", res)
	}
}

// TestShellStatusWaitMsParkWake pins the wait_ms discipline: the call parks on
// the job's doneCh and wakes the moment the job lands its terminal (long
// before the cap), then reports the now-collectible state; a wait that
// outlasts its cap just reports whatever is then true.
func TestShellStatusWaitMsParkWake(t *testing.T) {
	r := &fakeStreamingRunner{out: "woke\n", exitCode: 0, run: make(chan struct{}), started: make(chan struct{})}
	reg := newChildRunRegistry()
	caps := parentCaps{children: reg}
	if _, err := NewShellTool().(childCapableTool).ExecuteWithParent(
		context.Background(), shellCall("w1", "block", 0, true), shellEnvRunner(r), nil, caps); err != nil {
		t.Fatalf("start err = %v", err)
	}
	select {
	case <-r.started:
	case <-time.After(5 * time.Second):
		t.Fatal("streaming drive never started")
	}

	type outcome struct{ res session.ToolResult }
	done := make(chan outcome, 1)
	go func() {
		res, err := NewShellStatusTool().(childCapableTool).ExecuteWithParent(
			context.Background(), shellStatusCall("s1", map[string]any{"job_id": "bashcmd-w1", "wait_ms": 30000}), shellEnv, nil, caps)
		if err != nil {
			t.Errorf("wait call err = %v", err)
		}
		done <- outcome{res}
	}()

	// Give the park a beat to settle, then release the job's drive.
	time.Sleep(50 * time.Millisecond)
	select {
	case o := <-done:
		t.Fatalf("wait returned before the job finished: %+v", o.res)
	default:
	}
	close(r.run)

	select {
	case o := <-done:
		if o.res.IsError || o.res.Content != "woke\n[exit code: 0]" {
			t.Fatalf("woke wait = %+v, want the delivered result body", o.res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not wake at the job's terminal")
	}

	// A capped wait on a job that stays live reports the running state at the cap.
	r2 := &fakeStreamingRunner{run: make(chan struct{}), started: make(chan struct{})}
	defer close(r2.run)
	if _, err := NewShellTool().(childCapableTool).ExecuteWithParent(
		context.Background(), shellCall("w2", "block", 0, true), shellEnvRunner(r2), nil, caps); err != nil {
		t.Fatalf("start w2 err = %v", err)
	}
	<-r2.started
	start := time.Now()
	res := execShellStatus(t, reg, "s2", map[string]any{"job_id": "bashcmd-w2", "wait_ms": 60})
	if res.IsError || !strings.Contains(res.Content, "background command bashcmd-w2 is running.") {
		t.Fatalf("capped wait = %+v, want the running report", res)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("wait returned after %v, want roughly the 60ms cap", elapsed)
	}
}

// TestShellStatusWaitAnyJob pins the job-less wait: nothing live → no park; a
// live job → park until the NEXT registry terminal, then the roster renders.
func TestShellStatusWaitAnyJob(t *testing.T) {
	reg := newChildRunRegistry()
	start := time.Now()
	res := execShellStatus(t, reg, "s1", map[string]any{"wait_ms": 5000})
	if time.Since(start) > time.Second {
		t.Fatalf("an any-wait with nothing live must return promptly, took %v", time.Since(start))
	}
	if res.IsError || !strings.Contains(res.Content, "No background commands") {
		t.Fatalf("res = %+v, want the empty roster", res)
	}

	r := &fakeStreamingRunner{out: "later\n", exitCode: 0, run: make(chan struct{}), started: make(chan struct{})}
	caps := parentCaps{children: reg}
	if _, err := NewShellTool().(childCapableTool).ExecuteWithParent(
		context.Background(), shellCall("any1", "block", 0, true), shellEnvRunner(r), nil, caps); err != nil {
		t.Fatalf("start err = %v", err)
	}
	<-r.started
	done := make(chan session.ToolResult, 1)
	go func() {
		res, _ := NewShellStatusTool().(childCapableTool).ExecuteWithParent(
			context.Background(), shellStatusCall("s2", map[string]any{"wait_ms": 30000}), shellEnv, nil, caps)
		done <- res
	}()
	time.Sleep(50 * time.Millisecond)
	close(r.run)
	select {
	case res := <-done:
		if res.IsError || !strings.Contains(res.Content, "- bashcmd-any1 done (end_turn)") {
			t.Fatalf("any-wait wake = %+v, want the done roster", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("any-wait did not wake at the job's terminal")
	}
}

// TestShellStatusCancel pins the cancel verb: it signals the job's per-call
// context (the drive unwinds to StopCancelled), the confirmation is immediate
// (no terminal wait), and unknown / already-done / non-bash ids fail honestly.
func TestShellStatusCancel(t *testing.T) {
	r := &fakeStreamingRunner{run: make(chan struct{}), started: make(chan struct{}), sawCancel: make(chan struct{})}
	reg := newChildRunRegistry()
	caps := parentCaps{children: reg}
	if _, err := NewShellTool().(childCapableTool).ExecuteWithParent(
		context.Background(), shellCall("c1", "sleep 100", 0, true), shellEnvRunner(r), nil, caps); err != nil {
		t.Fatalf("start err = %v", err)
	}
	select {
	case <-r.started:
	case <-time.After(5 * time.Second):
		t.Fatal("streaming drive never started")
	}

	res := execShellStatus(t, reg, "s1", map[string]any{"cancel": "bashcmd-c1"})
	if res.IsError || !strings.Contains(res.Content, "background command bashcmd-c1: cancellation requested.") {
		t.Fatalf("cancel confirmation = %+v", res)
	}
	select {
	case <-r.sawCancel:
	case <-time.After(5 * time.Second):
		t.Fatal("the job's ctx was never cancelled")
	}
	awaitJobDone(t, reg, "bashcmd-c1")

	// The terminal is StopCancelled and the stored result carries the cancelled
	// trailer — collectible once, as usual.
	snap := reg.statusSnapshot()
	if len(snap) != 1 || snap[0].stop != session.StopCancelled {
		t.Fatalf("snapshot = %+v, want the one job at StopCancelled", snap)
	}
	res = execShellStatus(t, reg, "s2", map[string]any{"job_id": "bashcmd-c1"})
	if res.IsError || !strings.Contains(res.Content, "[command was canceled; output above is partial]") {
		t.Fatalf("cancelled collect = %+v, want the cancelled trailer", res)
	}

	// Cancelling a DONE job is an honest nothing-to-cancel, not an error.
	res = execShellStatus(t, reg, "s3", map[string]any{"cancel": "bashcmd-c1"})
	if res.IsError || !strings.Contains(res.Content, "background command bashcmd-c1 is already done; there is nothing to cancel.") {
		t.Fatalf("done cancel = %+v", res)
	}

	// Unknown ids and non-bash ids are the same miss.
	reg.register("subagent-a", childFamilySubagent, "g", func() {}, true)
	for _, id := range []string{"bashcmd-nope", "subagent-a"} {
		res = execShellStatus(t, reg, "s4-"+id, map[string]any{"cancel": id})
		if !res.IsError || !strings.Contains(res.Content, "no background command") {
			t.Fatalf("cancel %q = %+v, want the unknown-job error", id, res)
		}
	}
}

// TestBackgroundPendingNudgeTextFamilies pins the nudge's family partition:
// the subagent clause keeps its byte-stable wording; a bash clause naming
// ShellStatus is appended only for live bash jobs; a bash-only pending set
// renders no subagent clause.
func TestBackgroundPendingNudgeTextFamilies(t *testing.T) {
	if got := backgroundPendingNudgeText([]string{"subagent-p1"}); got !=
		"[harness note: 1 background subagent(s) still running: subagent-p1. "+
			"Collect or wait for them with SubagentStatus, cancel them, or finish — "+
			"anything still running when you finish will be cancelled.]" {
		t.Fatalf("subagent-only nudge mismatch: %q", got)
	}
	if got := backgroundPendingNudgeText([]string{"bashcmd-b1", "subagent-p1"}); got !=
		"[harness note: 1 background subagent(s) still running: subagent-p1. "+
			"Collect or wait for them with SubagentStatus, cancel them, or finish — "+
			"anything still running when you finish will be cancelled.]"+
			"[harness note: 1 background command(s) still running: bashcmd-b1. "+
			"Collect or wait for them with ShellStatus, cancel them, or finish — "+
			"anything still running when you finish will be cancelled.]" {
		t.Fatalf("mixed nudge mismatch: %q", got)
	}
	if got := backgroundPendingNudgeText([]string{"bashcmd-b2"}); got !=
		"[harness note: 1 background command(s) still running: bashcmd-b2. "+
			"Collect or wait for them with ShellStatus, cancel them, or finish — "+
			"anything still running when you finish will be cancelled.]" {
		t.Fatalf("bash-only nudge mismatch: %q", got)
	}
}

// TestLiveBackgroundIDsMatching pins the filtered live-ids walk the nudge
// partitions over: the unfiltered call (the registry's default) lists every
// live background child, the bash-only call only jobs, the delegation-only
// call only subagents.
func TestLiveBackgroundIDsMatching(t *testing.T) {
	reg := newChildRunRegistry()
	noCancel := func() {}
	reg.register("bashcmd-b", childFamilyShellCmd, "g", noCancel, true)
	reg.register("subagent-s", childFamilySubagent, "g", noCancel, true)
	reg.register("subagent-done", childFamilySubagent, "g", noCancel, true)
	reg.markDone("subagent-done", session.StopEndTurn)

	if got := reg.liveBackgroundIDsMatching(nil); len(got) != 2 || got[0] != "bashcmd-b" || got[1] != "subagent-s" {
		t.Fatalf("unfiltered = %v, want [bashcmd-b subagent-s]", got)
	}
	if got := reg.liveBackgroundIDsMatching(delegationFamiliesOnly); len(got) != 1 || got[0] != "subagent-s" {
		t.Fatalf("delegation-only = %v, want [subagent-s]", got)
	}
	if got := reg.liveBackgroundIDsMatching(shellCmdFamiliesOnly); len(got) != 1 || got[0] != "bashcmd-b" {
		t.Fatalf("bash-only = %v, want [bashcmd-b]", got)
	}
}
