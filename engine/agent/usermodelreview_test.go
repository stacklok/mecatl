package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// nilReviewStore is the minimal port.SessionStore stand-in for the nil-args
// guard below. The full reviewer behavior (R10: load-but-never-reopen, the fact
// landing in the real user-model store) is integration-tested next to the
// memory adapter in internal/adapter/memory.
type nilReviewStore struct{}

func (nilReviewStore) Load(context.Context, session.SessionID) (*session.Session, error) {
	return nil, nil
}

func (nilReviewStore) Save(context.Context, *session.Session) error { return nil }

// TestNewUserModelReviewerNilArgsPanic guards the composition-root contract.
func TestNewUserModelReviewerNilArgsPanic(t *testing.T) {
	t.Run("nil store", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("nil store should panic")
			}
		}()
		_ = agent.NewUserModelReviewer(nil, newEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog()}))
	})
	t.Run("nil engine", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("nil engine should panic")
			}
		}()
		_ = agent.NewUserModelReviewer(nilReviewStore{}, nil)
	})
	t.Run("observer nil engine", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("nil engine should panic")
			}
		}()
		_ = agent.NewUserModelObserver(nil)
	})
}

func TestUserModelObserverNeedsNoSessionStore(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	reviewer := agent.NewUserModelObserver(newEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog()}))
	if err := reviewer.Observe(context.Background(), learning.NewTrajectory(
		"completed", "/workspace", session.StopEndTurn, session.Usage{},
		[]session.Message{session.NewUserMessage("I prefer concise answers")},
	)); err != nil {
		t.Fatalf("Observe without SessionStore: %v", err)
	}
	if llm.Calls() != 1 {
		t.Fatalf("reviewer calls = %d, want 1", llm.Calls())
	}
	if err := reviewer.Review(context.Background(), "completed"); err == nil || !strings.Contains(err.Error(), "requires a SessionStore") {
		t.Fatalf("legacy Review error = %v, want documented SessionStore requirement", err)
	}
}
