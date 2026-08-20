package agent_test

// This test lives in package agent_test (not caller_identity_owner_test.go's
// internal package agent) so it can directly reuse toolCall/newEngine/newSession
// (loop_test.go), catalogWith (loop_test.go), childEngineWith (subagent_test.go),
// allowAll (loop_test.go), and drainObserving/resultByCallID (childcancel_test.go/
// background_test.go) — none of the assertions below need unexported access, and
// package agent_test is where those existing helpers already live.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// resumeOwnerAlice and resumeOwnerBob are the two principals
// TestCallerSeparation_Scenario3_SubagentResumeIsOwnerChecked drives: Bob must
// never be able to resume a subagent Alice owns.
var (
	resumeOwnerAlice = &session.Principal{Issuer: "https://idp.example/alice-realm", Subject: "alice", GrantType: session.GrantTypeUser, Name: "Alice"}
	resumeOwnerBob   = &session.Principal{Issuer: "https://idp.example/bob-realm", Subject: "bob", GrantType: session.GrantTypeUser, Name: "Bob"}
)

// resumeSnapshot is the pre-attack fingerprint the test re-checks after Bob's
// denied resume attempt, so the assertion catches a mutation even when the
// ToolResult alone would look like a clean refusal.
type resumeSnapshot struct {
	messages int
	owner    *session.Principal
	state    session.State
}

func snapshotResumeSession(t *testing.T, store port.SessionStore, id session.SessionID) resumeSnapshot {
	t.Helper()
	sess, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("load %q: %v", id, err)
	}
	var owner *session.Principal
	if sess.Owner != nil {
		cp := *sess.Owner
		owner = &cp
	}
	return resumeSnapshot{messages: len(sess.Conversation.Messages), owner: owner, state: sess.State}
}

// sameResumeSnapshot compares by VALUE (never by pointer — store.Load hands
// back a freshly deserialized *Principal each call even for the same owner).
func sameResumeSnapshot(a, b resumeSnapshot) bool {
	if a.messages != b.messages || a.state != b.state {
		return false
	}
	if (a.owner == nil) != (b.owner == nil) {
		return false
	}
	return a.owner == nil || *a.owner == *b.owner
}

func conversationContains(sess session.Session, marker string) bool {
	for _, m := range sess.Conversation.Messages {
		if strings.Contains(m.Text, marker) {
			return true
		}
	}
	return false
}

// runResumeAttempt drives ONE Subagent resume call through a real parent run —
// so the background:true call site (which requires a live child registry from
// caps, only present via a real dispatched run) is exercised identically to the
// foreground one — and returns the Subagent tool's own ToolResult. When
// waitForCompletion is set (the background positive-control cases), the
// scripted parent ALSO calls SubagentStatus{wait_ms} before its final turn, so
// the child is guaranteed persisted at StateCompleted by the time this returns
// (mirroring TestBackgroundComposesWithResume's pattern).
func runResumeAttempt(t *testing.T, task tool.Tool, childID session.SessionID, background bool, caller *session.Principal, waitForCompletion bool) session.ToolResult {
	t.Helper()
	args := `{"resume":"` + string(childID) + `","prompt":"continue"`
	if background {
		args += `,"background":true`
	}
	args += `}`

	turns := []mockllm.Turn{mockllm.ToolCallTurn(toolCall("rc", "Subagent", args))}
	cat := catalogWith(t, task)
	if waitForCompletion {
		turns = append(turns, mockllm.ToolCallTurn(toolCall("wait", "SubagentStatus",
			`{"agent_id":"`+string(childID)+`","wait_ms":30000}`)))
		cat.MustRegister(agent.NewSubagentStatusTool())
	}
	turns = append(turns, mockllm.TextTurn("parent done"))

	e := newEngine(agent.Deps{LLM: mockllm.New(turns...), Catalog: cat})
	ctx := context.Background()
	if caller != nil {
		ctx = session.WithPrincipal(ctx, caller)
	}
	r := e.Run(ctx, newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
	evs := drainObserving(t, r, nil)
	res := resultByCallID(evs)[session.ToolCallID("rc")]
	if res == nil {
		t.Fatalf("no Subagent result on the parent stream: %v", evs)
	}
	return *res
}

// TestCallerSeparation_Scenario3_SubagentResumeIsOwnerChecked extends AC3.3: a
// model given another caller's subagent handle cannot resume it through the
// Subagent tool's `resume: <agentId>` argument, in both the foreground and
// background:true call sites of engine/agent/subagent.go's resolveResumeSession.
// Bob's attempt is absence-shaped (identical wording to an unknown id, no
// leaked content) AND leaves Alice's persisted session byte-identical
// afterward (same message count, Owner, state) — proving no mutation, not
// merely that an error was returned. A same-owner resume still succeeds and
// replays the owner's own history; an ownerless ctx still resumes too (the
// pre-existing compatibility path for a non-OIDC deployment).
func TestCallerSeparation_Scenario3_SubagentResumeIsOwnerChecked(t *testing.T) {
	for _, background := range []bool{false, true} {
		name := "foreground"
		if background {
			name = "background"
		}
		t.Run(name, func(t *testing.T) {
			store := memstore.New()
			childLLM := mockllm.New(
				mockllm.TextTurn("ALICE SECRET"),      // fresh seed run
				mockllm.TextTurn("resumed by Alice"),  // Alice's own resume
				mockllm.TextTurn("resumed ownerless"), // ownerless-ctx resume
			)
			task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t)), agent.WithSubagentStore(store))

			// Seed Alice's owned child: a real parent run through the
			// dispatcher is the ONLY thing that stamps caps.owner onto a
			// freshly-minted child — a direct task.Execute call always
			// threads a zero parentCaps (see Execute's doc-comment).
			seedParentLLM := mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"remember: ALICE SECRET"}`))),
				mockllm.TextTurn("parent done"),
			)
			seedParent := agent.NewEngine(agent.Deps{LLM: seedParentLLM, Catalog: catalogWith(t, task), Policy: allowAll(), Model: "parent-model"})
			seedSess := session.New("owner-parent", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
			if err := seedSess.RestoreLabels(resumeOwnerAlice, session.Authority{}); err != nil {
				t.Fatalf("RestoreLabels: %v", err)
			}
			r := seedParent.Run(context.Background(), seedSess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"})
			drainObserving(t, r, nil)

			childID := session.SessionID("subagent-owner-parent-p1")
			before := snapshotResumeSession(t, store, childID)
			if before.owner == nil || before.owner.Subject != "alice" {
				t.Fatalf("seeded child owner = %+v, want alice", before.owner)
			}

			// 1+2: Bob's resume attempt is absence-shaped: no marker, no hint
			// the id exists under another owner.
			bobRes := runResumeAttempt(t, task, childID, background, resumeOwnerBob, false)
			if !bobRes.IsError {
				t.Fatalf("Bob's resume of Alice's child succeeded: %+v", bobRes)
			}
			if !strings.Contains(bobRes.Content, "no subagent found for resume id") {
				t.Fatalf("Bob's resume error = %q, want the standard not-found wording", bobRes.Content)
			}
			if strings.Contains(bobRes.Content, "ALICE SECRET") || strings.Contains(strings.ToLower(bobRes.Content), "alice") {
				t.Fatalf("Bob's resume error leaked owner content: %q", bobRes.Content)
			}

			// 3: Alice's persisted session is untouched by Bob's denied
			// attempt — this is the assertion that catches the resume-start
			// persist (issue #38); a check placed too late would pass step 2
			// and fail here.
			after := snapshotResumeSession(t, store, childID)
			if !sameResumeSnapshot(before, after) {
				t.Fatalf("Bob's denied resume mutated the foreign session: before=%+v after=%+v", before, after)
			}

			// 4: positive control — Alice resumes her own child and sees her
			// own history (proves the check isn't a hard-coded deny).
			aliceRes := runResumeAttempt(t, task, childID, background, resumeOwnerAlice, background)
			if aliceRes.IsError {
				t.Fatalf("Alice's own resume was denied: %+v", aliceRes)
			}
			grown := loadResumeSession(t, store, childID)
			if !conversationContains(grown, "ALICE SECRET") {
				t.Fatalf("Alice's resumed child lost her own history")
			}
			if len(grown.Conversation.Messages) <= before.messages {
				t.Fatalf("Alice's resume did not grow the child's history: before=%d after=%d", before.messages, len(grown.Conversation.Messages))
			}

			// 5: ownerless control — no principal on ctx still resumes (the
			// pre-existing compatibility path for a non-OIDC deployment).
			noOwnerRes := runResumeAttempt(t, task, childID, background, nil, background)
			if noOwnerRes.IsError {
				t.Fatalf("ownerless-ctx resume was denied: %+v", noOwnerRes)
			}
			grown2 := loadResumeSession(t, store, childID)
			if !conversationContains(grown2, "ALICE SECRET") {
				t.Fatalf("ownerless resume lost the owner's history")
			}
		})
	}
}

func TestSubagentResumeRequiresPrincipalWhenOwnershipEnforced(t *testing.T) {
	store := memstore.New()
	childID := session.SessionID("subagent-alice")
	child := session.New(childID, session.ModeDefault, "/ws", session.Limits{}, time.Now())
	if err := child.RestoreLabels(resumeOwnerAlice, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	if err := store.Save(context.Background(), child); err != nil {
		t.Fatalf("Save: %v", err)
	}
	task := agent.NewSubagentTool(childEngineWith(mockllm.New(), catalogWith(t)),
		agent.WithSubagentStore(store),
		agent.WithSubagentOwnershipEnforced(true),
	)

	for _, caller := range []*session.Principal{nil, resumeOwnerBob} {
		res := runResumeAttempt(t, task, childID, false, caller, false)
		if !res.IsError || !strings.Contains(res.Content, "no subagent found for resume id") {
			t.Fatalf("enforced resume for caller %#v = %+v, want absence-shaped denial", caller, res)
		}
	}
}

// TestCallerSeparation_SubagentResumeHidesForeignInFlightState ensures the
// in-flight guard remains observable to the owner but not to a foreign caller.
func TestCallerSeparation_SubagentResumeHidesForeignInFlightState(t *testing.T) {
	store := memstore.New()
	childID := session.SessionID("subagent-alice")
	seed := session.New(childID, session.ModeDefault, "/ws", session.Limits{}, time.Now())
	if err := seed.RestoreLabels(resumeOwnerAlice, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	if err := seed.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := seed.RecordAssistant(session.NewAssistantMessage("seed", "", nil)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := seed.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := store.Save(context.Background(), seed); err != nil {
		t.Fatalf("Save: %v", err)
	}

	block := &signalThenBlockTool{entered: make(chan struct{}, 1)}
	task := agent.NewSubagentTool(
		childEngineWith(mockllm.New(mockllm.ToolCallTurn(toolCall("k", "Block", `{}`))), catalogWith(t, block)),
		agent.WithSubagentStore(store),
		agent.WithSubagentOwnershipEnforced(true),
	)
	ctx, cancel := context.WithCancel(session.WithPrincipal(context.Background(), resumeOwnerAlice))
	defer cancel()
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = task.Execute(ctx, session.NewToolCall("a1", "Subagent", json.RawMessage(`{"resume":"subagent-alice","prompt":"continue"}`)), agent.MemEnv("/ws"))
	}()
	select {
	case <-block.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("owner resume never entered the blocking child")
	}

	foreign, err := task.Execute(session.WithPrincipal(context.Background(), resumeOwnerBob), session.NewToolCall("a2", "Subagent", json.RawMessage(`{"resume":"subagent-alice","prompt":"continue"}`)), agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("foreign resume transport error: %v", err)
	}
	unknown, err := task.Execute(session.WithPrincipal(context.Background(), resumeOwnerBob), session.NewToolCall("a3", "Subagent", json.RawMessage(`{"resume":"subagent-missing","prompt":"continue"}`)), agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("unknown resume transport error: %v", err)
	}
	for _, res := range []session.ToolResult{foreign, unknown} {
		if !res.IsError || !strings.Contains(res.Content, "no subagent found for resume id") || strings.Contains(res.Content, "already running") {
			t.Fatalf("foreign/unknown resume exposed liveness: %q", res.Content)
		}
	}

	owner, err := task.Execute(session.WithPrincipal(context.Background(), resumeOwnerAlice), session.NewToolCall("a4", "Subagent", json.RawMessage(`{"resume":"subagent-alice","prompt":"continue"}`)), agent.MemEnv("/ws"))
	if err != nil {
		t.Fatalf("owner concurrent resume transport error: %v", err)
	}
	if !owner.IsError || !strings.Contains(owner.Content, "already running") {
		t.Fatalf("owner concurrent resume = %q, want already-running guard", owner.Content)
	}
	cancel()
	<-firstDone
}

func loadResumeSession(t *testing.T, store port.SessionStore, id session.SessionID) session.Session {
	t.Helper()
	sess, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("load %q: %v", id, err)
	}
	return *sess
}
