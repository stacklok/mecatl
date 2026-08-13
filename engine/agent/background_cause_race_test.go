package agent

// Internal-package tests for issue #332: persisting the terminal failure cause
// on the child snapshot so it survives independent of the parent's subagent.end
// emit race. The event-drop race itself (drainChildren's abortEmits closing
// emitAbort so a parked end-emit gives up) is pinned by the existing drain tests
// (TestDrainTwoPhaseJoinsEmitParkedChild etc.); these tests prove the SNAPSHOT is
// the durable cause source — the load-bearing fix.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestBackgroundSubagentCauseSurvivesPostSealEmitRace is the issue-#332
// mutation-kill for the BACKGROUND path: a background child whose provider errors
// (StopError + cause) persists the cause on its snapshot via RecordLastError
// BEFORE persistChild, so the cause survives on the snapshot independent of the
// parent's subagent.end emit. The parent sequences on the child's registry doneCh
// (via AwaitAllDone) so the child has genuinely finished driveChild (StopError →
// RecordLastError → persistChild → emit → markDone) BEFORE the parent ends — no
// ctx-cancel race. The event-drop race the issue describes (the end-emit losing
// the race with the run-end seal) is pinned by the existing drain tests
// (TestDrainTwoPhaseJoinsEmitParkedChild etc.); this test proves the SNAPSHOT is
// the durable cause source regardless of whether the event was delivered.
// Removing the RecordLastError call fails (b) while (a) still passes.
func TestBackgroundSubagentCauseSurvivesPostSealEmitRace(t *testing.T) {
	const causeText = "upstream 503: model overloaded"
	store := memstore.New()

	// The child errors on its first (only) turn — a mid-stream break after a first
	// chunk is the terminal, never-retried shape.
	childLLM := mockllm.New(mockllm.ErrorTurn(errors.New(causeText), mockllm.TextChunk("thinking")))
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	childEngine := NewEngine(Deps{LLM: childLLM, Catalog: tool.NewCatalog(), Policy: allow, Model: "child-model"})
	task := NewSubagentTool(childEngine, WithSubagentStore(store))

	holder := &noticeRunHolder{ready: make(chan struct{})}
	await := &awaitChildrenDoneTool{holder: holder, ids: []string{"subagent-bg-cause-race-p1"}}

	mkCall := func(id, args string) session.ToolCall {
		return session.NewToolCall(session.ToolCallID(id), "Subagent", json.RawMessage(args))
	}
	// The parent starts the background child, then (in the SAME turn) parks on
	// AwaitAllDone until the child's doneCh closes (the child finished driveChild
	// → RecordLastError → persistChild → emit → markDone). Only then does the
	// parent take its clean-end turn, so the child is already StateFailed +
	// persisted before drainChildren runs — no ctx-cancel race.
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(
			mkCall("p1", `{"prompt":"investigate","background":true}`),
			session.NewToolCall("pw", "AwaitAllDone", json.RawMessage(`{}`)),
		),
		mockllm.TextTurn("parent done"),
	)
	cat := tool.NewCatalog()
	cat.MustRegister(task)
	cat.MustRegister(await)
	e := NewEngine(Deps{LLM: parentLLM, Catalog: cat, Policy: allow, Model: "parent-model"})

	sess := session.New("bg-cause-race", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	r := e.Run(context.Background(), sess, memEnv("/ws"), RunRequest{Text: "go"})
	holder.run = r
	close(holder.ready)

	var evs []session.Event
	deadline := time.After(15 * time.Second)
	for {
		stop := false
		select {
		case ev, ok := <-r.Events():
			if !ok {
				stop = true
			} else {
				evs = append(evs, ev)
			}
		case <-deadline:
			r.Cancel()
			t.Fatalf("run did not terminate (possible wedge)")
		}
		if stop {
			break
		}
	}

	// (a) The child's subagent.end was emitted (the child errored on its own
	// before the parent ended) and carries the cause. In the real race this
	// event would be dropped by the run-end seal's abortEmits — that drop is
	// pinned by TestDrainTwoPhaseJoinsEmitParkedChild; here we verify the child
	// genuinely errored so the snapshot path is exercised.
	var endCause string
	var ends int
	for _, ev := range evs {
		if ev.Type == session.EvSubagentEnd && ev.Subagent != nil {
			ends++
			endCause = ev.Subagent.Cause
		}
	}
	if ends != 1 {
		t.Fatalf("want exactly 1 EvSubagentEnd (the child errored before the parent ended), got %d", ends)
	}
	if !strings.Contains(endCause, causeText) {
		t.Fatalf("the subagent.end event must carry the cause %q, got %q", causeText, endCause)
	}

	// (b) The child snapshot carries the cause — the mutation-kill. Removing the
	// RecordLastError call in driveBackground empties this while (a) still passes
	// (the event is independent of the snapshot field).
	saved, err := store.Load(context.Background(), "subagent-bg-cause-race-p1")
	if err != nil || saved == nil {
		t.Fatalf("background child must be persisted: %v", err)
	}
	if saved.State != session.StateFailed {
		t.Fatalf("persisted child state = %q, want failed (the child errored before the parent ended)", saved.State)
	}
	if !strings.Contains(saved.LastError(), causeText) {
		t.Fatalf("the child snapshot's LastError must carry the cause %q, got %q (issue #332: the snapshot is the durable cause source)", causeText, saved.LastError())
	}
}

// TestBackgroundSubagentCauseRecoveredViaChildSnapshot was removed: the snapshot
// round-trip is covered by TestBackgroundSubagentCauseSurvivesPostSealEmitRace over
// memstore (a SessionStore) plus the sessnap round-trip tests
// (TestSnapshotRoundTripsLastErrorFailed etc.); the restart-over-jsonlstore
// event-log shape is the external subagent_cause_eventlog_test.go contract.

// TestSnapshotAndEventCauseAgreement pins the agent side of the byte-for-byte
// agreement the session-local snapshot normaliser claims (issue #332): the
// snapshot's Session.LastError and the subagent.end event's SubagentPayload.Cause
// must carry the SAME persisted cause, so the two normalisers (the session-local
// normaliseSnapshotError, mirrored because session cannot import engine/agent, and
// this package's subagentCausePayload) must agree byte-for-byte. The companion
// session-package test (TestSnapshotCauseMirrorsEventCauseContract) pins the same
// expectations from the other side; a drift in either algorithm or clamp constant
// breaks exactly one of the two, tripping CI.
func TestSnapshotAndEventCauseAgreement(t *testing.T) {
	if maxSubagentCausePreview != 400 {
		t.Fatalf("maxSubagentCausePreview = %d, must stay 400 to agree with engine/session's maxSnapshotErrorRunes", maxSubagentCausePreview)
	}
	// Each expectation is the shared contract output; the session side pins the
	// identical table.
	for _, tc := range []struct{ in, want string }{
		{"upstream 503: model overloaded", "upstream 503: model overloaded"},
		{"BOOM-\n  upstream detail\n\tdetail", "BOOM- upstream detail detail"},
		{"  leading and trailing  ", "leading and trailing"},
		{"", ""},
		{strings.Repeat("z", 5000), strings.Repeat("z", 400) + "…"},
	} {
		if got := subagentCausePayload(tc.in); got != tc.want {
			t.Errorf("subagentCausePayload(%q) = %q, want the shared snapshot/event output %q", tc.in, got, tc.want)
		}
	}
}

// TestForegroundSubagentCauseAlsoPersistedOnSnapshot is the #319 foreground
// failure fixture's snapshot half: a foreground child that dies on a provider
// error persists the cause on its snapshot too (belt-and-suspenders — the
// foreground emit is synchronous, so the event AND the snapshot both carry it).
func TestForegroundSubagentCauseAlsoPersistedOnSnapshot(t *testing.T) {
	const causeText = "upstream 503: model overloaded"
	store := memstore.New()

	// One turn: it streams a first chunk then breaks — the child never records
	// assistant text (RecordAssistant is never reached on the error path).
	childLLM := mockllm.New(mockllm.ErrorTurn(errors.New(causeText), mockllm.TextChunk("thinking")))
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	childEngine := NewEngine(Deps{LLM: childLLM, Catalog: tool.NewCatalog(), Policy: allow, Model: "child-model"})
	task := NewSubagentTool(childEngine, WithSubagentStore(store))

	mkCall := func(id, args string) session.ToolCall {
		return session.NewToolCall(session.ToolCallID(id), "Subagent", json.RawMessage(args))
	}
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(mkCall("p1", `{"prompt":"investigate"}`)),
		mockllm.TextTurn("parent done"),
	)
	cat := tool.NewCatalog()
	cat.MustRegister(task)
	e := NewEngine(Deps{LLM: parentLLM, Catalog: cat, Policy: allow, Model: "parent-model"})

	sess := session.New("fg-cause-snap", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	r := e.Run(context.Background(), sess, memEnv("/ws"), RunRequest{Text: "go"})

	var endCause string
	deadline := time.After(15 * time.Second)
	for {
		stop := false
		select {
		case ev, ok := <-r.Events():
			if !ok {
				stop = true
			} else if ev.Type == session.EvSubagentEnd && ev.Subagent != nil {
				endCause = ev.Subagent.Cause
			}
		case <-deadline:
			r.Cancel()
			t.Fatalf("run did not terminate (possible wedge)")
		}
		if stop {
			break
		}
	}

	// (a) The event carries the cause (the #319 contract, unchanged).
	if !strings.Contains(endCause, causeText) {
		t.Fatalf("the subagent.end event must carry the cause %q, got %q", causeText, endCause)
	}

	// (b) The loaded child snapshot ALSO carries the cause (issue #332 — the
	// snapshot is the single durable cause source regardless of path).
	saved, err := store.Load(context.Background(), "subagent-fg-cause-snap-p1")
	if err != nil || saved == nil {
		t.Fatalf("foreground child must be persisted: %v", err)
	}
	if saved.State != session.StateFailed {
		t.Fatalf("persisted child state = %q, want failed", saved.State)
	}
	if !strings.Contains(saved.LastError(), causeText) {
		t.Fatalf("the foreground child snapshot's LastError must carry the cause %q, got %q (issue #332)", causeText, saved.LastError())
	}
}
