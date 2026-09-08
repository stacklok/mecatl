package memory_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/memory"
)

// fakeReviewStore is a port.SessionStore that hands back a single scripted
// transcript session and RECORDS every Load id and Save call, so a test can prove
// R10: the reviewer reads the user session (Load) but NEVER reopens/re-runs it
// (the returned session's State is never driven away from completed, and the store
// is never asked to Save the user session by the reviewer).
type fakeReviewStore struct {
	mu      sync.Mutex
	sess    *session.Session
	loadIDs []session.SessionID
	saveIDs []session.SessionID
}

func (f *fakeReviewStore) Load(_ context.Context, id session.SessionID) (*session.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadIDs = append(f.loadIDs, id)
	return f.sess, nil
}

func (f *fakeReviewStore) Save(_ context.Context, s *session.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saveIDs = append(f.saveIDs, s.ID)
	return nil
}

// completedSessionWithTranscript builds a terminal (completed) session carrying a
// short operator/agent transcript, mirroring a just-finished run the reviewer
// re-loads.
func completedSessionWithTranscript(id session.SessionID) *session.Session {
	s := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/proj", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 3}, time.Now())
	s.Conversation.Append(session.Message{Role: session.RoleUser, Text: "I prefer terse answers and I work in Go."})
	s.Conversation.Append(session.Message{Role: session.RoleAssistant, Text: "Understood — terse it is."})
	s.State = session.StateCompleted
	return s
}

// TestUserModelReviewerWritesFactAndNeverReopens is the Phase-2b unit proof:
//   - the reviewer loads a scripted transcript via the fake store,
//   - runs a mockllm child that emits ONE RememberUser call,
//   - the fact lands in the (real) user-model store, AND
//   - R10: the user's terminal session is never reopened/re-run (its State stays
//     completed, and the store is never asked to Save it by the reviewer).
func TestUserModelReviewerWritesFactAndNeverReopens(t *testing.T) {
	// Real caller-partitioned user-model store in a temp dir + the RememberUser tool over it.
	baseStore, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	store := memory.NewCallerStore(baseStore, false)
	var rememberUser tool.Tool
	for _, tl := range memory.NewUserModelTools(store) {
		if tl.Spec().Name == memory.RememberUserToolName {
			rememberUser = tl
		}
	}
	if rememberUser == nil {
		t.Fatal("RememberUser tool not found")
	}

	// A child engine scoped to ONLY RememberUser, driven by a mockllm that emits one
	// RememberUser tool call then a closing text turn.
	cat := tool.NewCatalog()
	cat.MustRegister(rememberUser)
	args, _ := json.Marshal(map[string]any{
		"key": "comm-style", "value": "Prefers terse answers; works in Go.",
		"description": "communication style",
	})
	llm := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("c1", memory.RememberUserToolName, args)),
		mockllm.TextTurn("done"),
	)
	childEngine := newEngine(agent.Deps{LLM: llm, Catalog: cat})

	userSessID := session.SessionID("user-session-1")
	owner := &session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
	userSess := completedSessionWithTranscript(userSessID)
	if err := userSess.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	fakeStore := &fakeReviewStore{sess: userSess}

	reviewer := agent.NewUserModelReviewer(fakeStore, childEngine)
	if err := reviewer.Review(context.Background(), string(userSessID)); err != nil {
		t.Fatalf("Review: %v", err)
	}

	// (a) The fact landed in Alice's user-model namespace under the enforced "user/" prefix.
	aliceCtx := session.WithPrincipal(context.Background(), owner)
	got, ok, err := store.Recall(aliceCtx, "user/comm-style")
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if !ok {
		t.Fatalf("reviewer did not write the extracted fact to the user-model store")
	}
	if got.Value == "" {
		t.Errorf("stored fact has empty value")
	}

	// (b) R10: the user's terminal session was READ (loaded) but never reopened or
	// re-run — its State is still completed, and the reviewer never Saved it.
	if !fakeStore.sess.State.IsTerminal() {
		t.Errorf("R10 violated: the user session was driven out of its terminal state (now %q)", fakeStore.sess.State)
	}
	if fakeStore.sess.State != session.StateCompleted {
		t.Errorf("R10 violated: user session state changed from completed to %q", fakeStore.sess.State)
	}
	for _, id := range fakeStore.saveIDs {
		if id == userSessID {
			t.Errorf("R10 violated: the reviewer Saved (re-ran) the user session %q", userSessID)
		}
	}
	// The reviewer must have loaded exactly the user session (a read), never a child.
	if len(fakeStore.loadIDs) != 1 || fakeStore.loadIDs[0] != userSessID {
		t.Errorf("expected exactly one Load of the user session, got %v", fakeStore.loadIDs)
	}
}

// TestUserModelReviewerEmptyTranscriptIsNoOp proves a session with no minable text
// is a clean no-op (no child run, no write, no error).
func TestUserModelReviewerEmptyTranscriptIsNoOp(t *testing.T) {
	store, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	cat := tool.NewCatalog()
	for _, tl := range memory.NewUserModelTools(store) {
		if tl.Spec().Name == memory.RememberUserToolName {
			cat.MustRegister(tl)
		}
	}
	// A mockllm that WOULD write if run — proving the no-op short-circuits before any
	// child run by asserting the store stays empty.
	args, _ := json.Marshal(map[string]any{"key": "x", "value": "should not be written"})
	llm := mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("c1", memory.RememberUserToolName, args)))
	childEngine := newEngine(agent.Deps{LLM: llm, Catalog: cat})

	id := session.SessionID("empty-session")
	empty := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/proj", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 3}, time.Now())
	empty.State = session.StateCompleted // no messages
	fakeStore := &fakeReviewStore{sess: empty}

	reviewer := agent.NewUserModelReviewer(fakeStore, childEngine)
	if err := reviewer.Review(context.Background(), string(id)); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if entries, _ := store.List(context.Background(), ""); len(entries) != 0 {
		t.Errorf("empty transcript should be a no-op, but the store has %d entries", len(entries))
	}
}

// TestNewUserModelReviewerNilArgsPanic (the composition-root contract guard)
// stays in engine/agent — it needs no real store and the reviewer is engine code.
