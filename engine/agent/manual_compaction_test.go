package agent_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

type manualCompactor struct {
	out     []session.Message
	summary string
	err     error
	calls   int
}

func (c *manualCompactor) Compact(_ context.Context, _ *session.Conversation) ([]session.Message, string, session.AuxiliaryUsage, error) {
	c.calls++
	return c.out, c.summary, session.AuxiliaryUsage{}, c.err
}

type textLengthCounter struct{}

func (textLengthCounter) Count(text string) int { return len(text) }
func (textLengthCounter) CountMessages(msgs []session.Message) int {
	total := len(msgs)
	for _, msg := range msgs {
		total += len(msg.Text)
	}
	return total
}

func manualSession(t *testing.T) *session.Session {
	t.Helper()
	sess := session.New("manual", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	if err := sess.SeedHistory([]session.Message{
		session.NewUserMessage("a long original user instruction"),
		session.NewAssistantMessage("a long original assistant response", "", nil),
	}); err != nil {
		t.Fatalf("SeedHistory: %v", err)
	}
	return sess
}

func TestEngineCompactSessionCandidateContract(t *testing.T) {
	boom := errors.New("compact failed")
	orphan := session.NewToolMessage(session.NewToolResult("missing", "bad"))
	tests := []struct {
		name       string
		out        []session.Message
		err        error
		wantErr    error
		wantChange bool
	}{
		{"reducing", []session.Message{session.NewUserMessage("short")}, nil, nil, true},
		{"empty", nil, nil, nil, false},
		{"identical", manualSession(t).Conversation.Messages, nil, nil, false},
		{"non-reducing", []session.Message{session.NewUserMessage("this candidate is deliberately much longer than the entire original persisted history and cannot reduce it")}, nil, nil, false},
		{"invalid pairing", []session.Message{orphan}, nil, agent.ErrCompactionWouldOrphan, false},
		{"compactor error", nil, boom, boom, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sess := manualSession(t)
			before := session.CloneMessages(sess.Conversation.Messages)
			compactor := &manualCompactor{out: tc.out, summary: "summary", err: tc.err}
			eng := agent.NewEngine(agent.Deps{Compactor: compactor, TokenCounter: textLengthCounter{}})

			got, err := eng.CompactSession(context.Background(), sess)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if got.Changed != tc.wantChange {
				t.Fatalf("Changed = %t, want %t", got.Changed, tc.wantChange)
			}
			if compactor.calls != 1 {
				t.Fatalf("compactor calls = %d, want 1", compactor.calls)
			}
			if !tc.wantChange {
				if !reflect.DeepEqual(sess.Conversation.Messages, before) {
					t.Fatal("no-op/error mutated history")
				}
				return
			}
			if !reflect.DeepEqual(got.Archive, before) || got.Summary != "summary" {
				t.Fatalf("result archive/summary = %#v, %q", got.Archive, got.Summary)
			}
			if !reflect.DeepEqual(sess.Conversation.Messages, tc.out) {
				t.Fatalf("history = %#v, want candidate %#v", sess.Conversation.Messages, tc.out)
			}
		})
	}
}

type mutatingManualCompactor struct {
	err error
}

func (c mutatingManualCompactor) Compact(_ context.Context, conv *session.Conversation) ([]session.Message, string, session.AuxiliaryUsage, error) {
	conv.Messages[0].Text = "mutated"
	conv.Messages[0].Parts[0].Data[0] = 99
	if c.err == nil {
		return []session.Message{session.NewUserMessage(strings.Repeat("non-reducing", 100))}, "", session.AuxiliaryUsage{}, nil
	}
	return conv.Messages, "", session.AuxiliaryUsage{}, c.err
}

func TestEngineCompactSessionIsolatesMutatingCompactor(t *testing.T) {
	for _, compactErr := range []error{errors.New("failed after mutation"), nil} {
		sess := manualSession(t)
		part := session.Content{Kind: session.MediaImage, Data: []byte{1, 2, 3}}
		messages := session.CloneMessages(sess.Conversation.Messages)
		messages[0].Parts = []session.Content{part}
		if err := sess.ReplaceHistoryAtBoundary(messages); err != nil {
			t.Fatalf("ReplaceHistoryAtBoundary: %v", err)
		}
		before := append([]byte(nil), sess.Conversation.Messages[0].Parts[0].Data...)
		eng := agent.NewEngine(agent.Deps{Compactor: mutatingManualCompactor{err: compactErr}, TokenCounter: textLengthCounter{}})
		result, err := eng.CompactSession(context.Background(), sess)
		if compactErr != nil && !errors.Is(err, compactErr) {
			t.Fatalf("error = %v, want %v", err, compactErr)
		}
		if compactErr == nil && err != nil {
			t.Fatalf("non-reducing mutation: %v", err)
		}
		if result.Changed || sess.Conversation.Messages[0].Text == "mutated" || !reflect.DeepEqual(sess.Conversation.Messages[0].Parts[0].Data, before) {
			t.Fatalf("mutating compactor changed aggregate: result=%+v history=%+v", result, sess.Conversation.Messages)
		}
	}
}

func TestEngineCompactSessionRejectsActiveStatesBeforeCompactor(t *testing.T) {
	for _, awaiting := range []bool{false, true} {
		sess := manualSession(t)
		if err := sess.BeginTurn(); err != nil {
			t.Fatalf("BeginTurn: %v", err)
		}
		if awaiting {
			if err := sess.PauseForApproval(session.PendingAsk{AskID: "ask"}); err != nil {
				t.Fatalf("PauseForApproval: %v", err)
			}
		}
		compactor := &manualCompactor{out: []session.Message{session.NewUserMessage("short")}}
		eng := agent.NewEngine(agent.Deps{Compactor: compactor, TokenCounter: textLengthCounter{}})
		if _, err := eng.CompactSession(context.Background(), sess); !errors.Is(err, session.ErrIllegalTransition) {
			t.Fatalf("awaiting=%t error = %v, want ErrIllegalTransition", awaiting, err)
		}
		if compactor.calls != 0 {
			t.Fatalf("awaiting=%t compactor called %d times", awaiting, compactor.calls)
		}
	}
}
