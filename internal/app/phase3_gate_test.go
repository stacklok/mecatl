package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// TestPhase3ReconstructFromStoreAndLog is the cloud-native Phase 3 GATE: it drives
// a session that takes THREE distinct verdicts (deny / allow-once / allow-always)
// AND crosses a compaction boundary, persists to a REAL on-disk jsonlstore via the
// full composition (app.Build + the HTTP SSE relay, which Appends every event to
// the durable EventLog), then RECONSTRUCTS the user-rich timeline from
// SessionStore.Load + EventLog.Read ALONE (a fresh jsonlstore over the same dir,
// no live Service). It asserts the reconstruction contains:
//
//	(a) the PRE-compaction turns, recovered from EvCompactionArchive (the loaded
//	    session SNAPSHOT no longer holds them — compaction replaced them);
//	(b) all three EvApproval events with the correct verdicts;
//	(c) live reasoning/progress events (reasoning.delta / message.delta / turn.end).
//
// WHAT THIS GATE PROVES is the END-TO-END pipeline: the archive + verdict events
// reach the DURABLE LOG and survive a from-store+log-alone reconstruction. It is NOT
// the oracle for the archive's CAPTURE ORDERING — under the tiny test window the loop
// fires SEVERAL compactions, and a capture-AFTER-ReplaceHistory mutation survives here
// (a later compaction's archive still carries an earlier dropped call), so this gate
// does not kill that mutation. The capture-before/after kill is deferred to the
// loop-level oracle TestCompactionEmitsNonDestructiveArchive (engine/agent), which
// forces a SINGLE deterministic compaction and asserts the archived slice EQUALS the
// pre-mutation history.
//
// MUTATION-KILL (compaction archive reaches the log): drop the EvCompactionArchive
// emit in maybeCompact and (a) fails — the pre-compaction turns are unrecoverable from
// the snapshot+log. MUTATION-KILL (verdict events reach the log): drop an authorize
// EvApproval emit and (b) fails — a verdict is missing from the log.
func TestPhase3ReconstructFromStoreAndLog(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()

	write := func(id, path string) session.ToolCall {
		return session.NewToolCall(session.ToolCallID(id), "Write",
			json.RawMessage(`{"path":"`+path+`","content":"body of `+path+`"}`))
	}

	cfg := Config{
		Workspace:           workspace,
		NoSoul:              true,
		StoreDir:            storeDir,
		MemoryDir:           t.TempDir(),
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
		liveModelHTTPClient: offlineHTTPClient(),
		// Tiny window so maybeCompact fires after the first couple of turns of history
		// (offline test seam) — the pre-compaction turns then land in an archive event.
		ContextWindowOverride: 40,
	}
	// The first three Writes take the three distinct verdicts; MANY more Writes
	// follow (allow-once) so the trailing history grows past the HeuristicCompactor's
	// kept tail AND past the user-turn back-snap's lookback bound (maxUserSnapLookback)
	// — there is no recent USER turn near the early writes, so the back-snap cannot
	// anchor the verbatim tail on them and the EARLY writes fall into the dropped head,
	// making them recoverable ONLY from the archive, never the compacted snapshot.
	turns := []mockllm.Turn{
		mockllm.ToolCallTurn(write("w1", "a.txt")), // -> DENY
		mockllm.ToolCallTurn(write("w2", "b.txt")), // -> ALLOW_ONCE
		mockllm.ToolCallTurn(write("w3", "c.txt")), // -> ALLOW_ALWAYS
	}
	// w4..w18 (15 more turns = 30 trailing messages, well past the back-snap's
	// 24-message lookback) so w1/w2/w3 are unreachable by the back-snap and drop.
	for i := 4; i <= 18; i++ {
		id := "w" + strconv.Itoa(i)
		turns = append(turns, mockllm.ToolCallTurn(write(id, id+".txt"))) // -> ALLOW_ONCE (tail)
	}
	turns = append(turns, mockllm.ReasoningTurn("reviewing the writes", "all set"))
	cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(turns...)
	}
	built, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	sess, err := built.Service.CreateSession(ctx, workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		built.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	srv := httptest.NewServer(server.NewHTTPHandler(built.Service))

	// The first THREE asks take the three DISTINCT verdicts (the gate's assertion);
	// later tail-write asks all take allow-once just to keep the run flowing.
	verdicts := []string{
		session.VerdictStringDeny,
		session.VerdictStringAllowOnce,
		session.VerdictStringAllowAlways,
	}
	verdictFor := func(i int) string {
		if i < len(verdicts) {
			return verdicts[i]
		}
		return session.VerdictStringAllowOnce
	}
	asks := 0
	promptOverHTTP(t, srv.URL, string(sess.ID), "do the writes", func(ev sseEvent) {
		if ev.Type != "permission.ask" {
			return
		}
		body, _ := json.Marshal(map[string]any{"ask_id": ev.Ask.AskID, "verdict": verdictFor(asks)})
		ar, aerr := http.Post(srv.URL+"/v1/sessions/"+string(sess.ID)+"/approve",
			"application/json", strings.NewReader(string(body)))
		if aerr != nil {
			t.Errorf("POST approve #%d: %v", asks+1, aerr)
		} else {
			ar.Body.Close()
		}
		asks++
	})
	srv.Close()
	built.Close() // tear the live Service down: the reconstruction reads ONLY store+log.

	if asks < len(verdicts) {
		t.Fatalf("expected at least %d asks (the three distinct verdicts), saw %d", len(verdicts), asks)
	}

	// RECONSTRUCT from a FRESH jsonlstore over the same dir — no live Service.
	rstore, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("reopen jsonlstore: %v", err)
	}
	loaded, err := rstore.Load(ctx, sess.ID)
	if err != nil {
		t.Fatalf("SessionStore.Load: %v", err)
	}
	var logged []session.Event
	for ev, rerr := range rstore.Read(ctx, sess.ID) {
		if rerr != nil {
			t.Fatalf("EventLog.Read: %v", rerr)
		}
		logged = append(logged, ev)
	}
	if len(logged) == 0 {
		t.Fatal("the durable log is empty")
	}

	// (a) PRE-compaction turns are recoverable from the archive but NOT from the
	// loaded snapshot. The early Write tool calls (w1/w2/w3) must appear in some
	// archive's Replaced span; at least one must be ABSENT from the compacted snapshot
	// (proving the archive is the non-destructive record the snapshot lost).
	var archives int
	archivedCalls := map[session.ToolCallID]struct{}{}
	for _, ev := range logged {
		if ev.Type == session.EvCompactionArchive && ev.CompactionArchive != nil {
			archives++
			for _, m := range ev.CompactionArchive.Replaced {
				for _, c := range m.ToolCalls {
					archivedCalls[c.ID] = struct{}{}
				}
			}
		}
	}
	if archives == 0 {
		t.Fatal("no EvCompactionArchive in the log: compaction never fired, the gate does not exercise (a)")
	}
	snapshotCalls := map[session.ToolCallID]struct{}{}
	for _, m := range loaded.Conversation.Messages {
		for _, c := range m.ToolCalls {
			snapshotCalls[c.ID] = struct{}{}
		}
	}
	recovered := false
	for id := range archivedCalls {
		if _, inSnap := snapshotCalls[id]; !inSnap {
			recovered = true // a pre-compaction turn the snapshot lost, recovered from the archive
			break
		}
	}
	if !recovered {
		t.Fatalf("no pre-compaction tool call was recoverable from the archive yet absent from the snapshot; "+
			"archived=%v snapshot=%v", callIDKeys(archivedCalls), callIDKeys(snapshotCalls))
	}

	// (b) The three distinct verdicts are present in the log, in order, as the FIRST
	// three EvApproval events (the later tail-write verdicts are all allow-once).
	var gotVerdicts []string
	for _, ev := range logged {
		if ev.Type == session.EvApproval && ev.Approval != nil {
			gotVerdicts = append(gotVerdicts, ev.Approval.Verdict)
		}
	}
	if len(gotVerdicts) < len(verdicts) {
		t.Fatalf("expected at least %d EvApproval verdicts in the log, got %d: %v", len(verdicts), len(gotVerdicts), gotVerdicts)
	}
	for i, want := range verdicts {
		if gotVerdicts[i] != want {
			t.Fatalf("verdict #%d = %q, want %q (full: %v)", i+1, gotVerdicts[i], want, gotVerdicts)
		}
	}

	// (c) Live reasoning/progress events survive in the log.
	kinds := map[session.EventType]bool{}
	for _, ev := range logged {
		kinds[ev.Type] = true
	}
	for _, want := range []session.EventType{session.EvReasoningDelta, session.EvMessageDelta, session.EvTurnEnd} {
		if !kinds[want] {
			t.Fatalf("the reconstructed timeline is missing live event %q (kinds: %v)", want, kindNames(kinds))
		}
	}

	// (d) WHAT THE USER ASKED survives in the log (ADR 0038 / EvUserPrompt): the durable
	// log now records the user prompt text, so a from-store+log reconstruction can show
	// what the user requested — the gap ADR 0027 row 11 left open. Before EvUserPrompt
	// the relay never re-emitted the prompt, so the log could not show it.
	foundPrompt := false
	for _, ev := range logged {
		if ev.Type == session.EvUserPrompt && ev.UserPrompt != nil && ev.UserPrompt.Text == "do the writes" {
			foundPrompt = true
			break
		}
	}
	if !foundPrompt {
		t.Fatalf("the reconstructed timeline does not contain the user's prompt text (EvUserPrompt); "+
			"kinds: %v", kindNames(kinds))
	}
}

// callIDKeys projects a tool-call-id set to a slice for a failure message.
func callIDKeys(set map[session.ToolCallID]struct{}) []session.ToolCallID {
	out := make([]session.ToolCallID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}

// kindNames projects an event-kind set to a slice for a failure message.
func kindNames(set map[session.EventType]bool) []session.EventType {
	out := make([]session.EventType, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// childLeakSentinel is a secret-SHAPED stand-in for a Subagent child's tool arg
// (gauntlet #7): an innocuous literal that must NEVER surface VERBATIM in any durable
// log event — the no-leak mutation-verify asserts on the ABSENCE of its full form
// (ADR 0079: the delegation projection forwards a clampPreview-BOUNDED preview, so
// only a clamped head may cross). It is longer than the clampPreview cap (200 runes)
// so verbatim carriage is impossible by construction.
var childLeakSentinel = "SENTINEL_phase3_child_arg_must_not_leak_7b2e" + strings.Repeat("_pad", 120) + "_TAIL"

// childLeakSentinelTail is the part of the sentinel that clamping MUST remove.
const childLeakSentinelTail = "_TAIL"

// TestPhase3LogNoChildLeak is the Phase 3 GATE's no-leak mutation-verify: a
// Subagent delegation's child makes a tool call whose args carry a secret-shaped
// sentinel. The delegation events the relay records (subagent.*) are BOUNDED
// previews (ADR 0079), so the sentinel's TAIL must NOT appear in ANY durable-log
// event's serialized body. The
// log inherits the stream's redaction; it adds none of its own — and the
// compaction archive carries only the PARENT's conversation, never child content.
//
// MUTATION-INTENT: if a future change forwarded raw child args on a delegation
// event (or folded child content into the parent conversation the archive carries),
// the serialized log would contain the full sentinel (tail included) and this fails.
func TestPhase3LogNoChildLeak(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()

	cfg := Config{
		Workspace:           workspace,
		NoSoul:              true,
		StoreDir:            storeDir,
		MemoryDir:           t.TempDir(),
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
		liveModelHTTPClient: offlineHTTPClient(),
	}
	// ONE shared mockllm serves both parent and child (composition wires one provider).
	// The Subagent tool drives the child SYNCHRONOUSLY when the parent issues the call,
	// so the turns interleave in this exact order: parent issues Subagent, the child
	// reads (carrying the sentinel arg) then ends, then the parent ends.
	cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(
			// parent turn 1: delegate.
			mockllm.ToolCallTurn(session.NewToolCall("p1", "Subagent",
				json.RawMessage(`{"prompt":"investigate"}`))),
			// child turn 1: a read-only Read whose path carries the sentinel (auto-runs,
			// no ask). child turn 2: end.
			mockllm.ToolCallTurn(session.NewToolCall("k1", "Read",
				json.RawMessage(`{"path":"`+childLeakSentinel+`"}`))),
			mockllm.TextTurn("child done"),
			// parent turn 2: end.
			mockllm.TextTurn("parent done"),
		)
	}
	built, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	sess, err := built.Service.CreateSession(ctx, workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		built.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	srv := httptest.NewServer(server.NewHTTPHandler(built.Service))

	sawSubagent := false
	promptOverHTTP(t, srv.URL, string(sess.ID), "delegate it", func(ev sseEvent) {
		if strings.HasPrefix(ev.Type, "subagent.") {
			sawSubagent = true
		}
	})
	srv.Close()
	built.Close()

	if !sawSubagent {
		t.Fatal("no subagent.* event relayed: the delegation did not run")
	}

	rstore, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("reopen jsonlstore: %v", err)
	}
	var logged []session.Event
	for ev, rerr := range rstore.Read(ctx, sess.ID) {
		if rerr != nil {
			t.Fatalf("EventLog.Read: %v", rerr)
		}
		logged = append(logged, ev)
	}
	if len(logged) == 0 {
		t.Fatal("the durable log is empty")
	}
	for _, ev := range logged {
		blob, merr := json.Marshal(ev)
		if merr != nil {
			t.Fatalf("marshal logged event: %v", merr)
		}
		if strings.Contains(string(blob), childLeakSentinelTail) {
			t.Fatalf("REDACTION LEAK: child arg sentinel surfaced UNBOUNDED in a logged %s event: %s", ev.Type, blob)
		}
	}

	// CHILD-ISOLATION for EvUserPrompt (ADR 0038): the child's OWN prompt is the
	// delegated goal ("investigate"). It is emitted on the CHILD run's stream (drained
	// inside the Subagent tool, like every child event) and must NEVER reach the PARENT
	// log. So the parent log's EvUserPrompt events carry only the parent's input
	// ("delegate it"), never the child goal. This pins that a child's user-prompt
	// emission cannot leak onto the parent run's durable log.
	for _, ev := range logged {
		if ev.Type == session.EvUserPrompt && ev.UserPrompt != nil {
			if strings.Contains(ev.UserPrompt.Text, "investigate") {
				t.Fatalf("CHILD-ISOLATION LEAK: the child's delegated goal surfaced as a parent EvUserPrompt: %q", ev.UserPrompt.Text)
			}
		}
	}
}
