package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type unconditionalLearningObserver struct{ calls int }

func (o *unconditionalLearningObserver) Observe(context.Context, learning.Trajectory) error {
	o.calls++
	return nil
}

func (o *unconditionalLearningObserver) ObserveWithUsage(context.Context, learning.Trajectory) (session.AuxiliaryUsage, error) {
	o.calls++
	return session.AuxiliaryUsage{}, nil
}

func legacyLearningSession(t *testing.T) *session.Session {
	t.Helper()
	sess, err := sessnap.Unmarshal([]byte(`{"id":"legacy-learning","state":"completed","mode":"default","environment_ref":{"kind":"local","id":"/ws","revision":"in-tree-v1"},"messages":[{"role":"user","text":"remember that I prefer concise output"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestLegacyCompletionNeverReachesCustomLearningObserver(t *testing.T) {
	for _, withUsage := range []bool{false, true} {
		sess := legacyLearningSession(t)
		store := memstore.New()
		ordinary := &recordingObserver{}
		unconditional := &unconditionalLearningObserver{}
		var observer learning.Observer = ordinary
		if withUsage {
			observer = unconditional
		}
		for range 3 {
			if err := sess.Reopen(); err != nil {
				t.Fatal(err)
			}
			runLearningTurn(t, learning.Auto, observer, mockllm.New(mockllm.TextTurn("done")), sess)
			if sess.State != session.StateCompleted || sess.LearningEligible() || ordinary.calls != 0 || unconditional.calls != 0 {
				t.Fatalf("legacy completion state=%s eligibility=%v observer calls=%d/%d", sess.State, sess.LearningEligible(), ordinary.calls, unconditional.calls)
			}
			if err := store.Save(t.Context(), sess); err != nil {
				t.Fatal(err)
			}
			var err error
			sess, err = store.Load(t.Context(), sess.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestExplicitReflectionRejectsIneligibleAndSerializedInputsBeforeProvider(t *testing.T) {
	legacy := legacyLearningSession(t)
	current, _ := admittedInput(t)
	raw, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	var serialized learning.Input
	if err := json.Unmarshal(raw, &serialized); err != nil {
		t.Fatal(err)
	}
	for _, input := range []learning.Input{
		learning.NewInput(learning.NewSessionTrajectory(legacy, "/ws", session.StopEndTurn, session.Usage{}), nil, []learning.Signal{{Kind: learning.SignalHostRequested}}, nil),
		serialized,
	} {
		provider := mockllm.New(mockllm.TextTurn(`{"kind":"abstained","candidates":[]}`))
		reflector, err := agent.NewEvidenceReflector(provider, session.ProviderModelID{ProviderID: "test", ModelID: "model"}, nil, agent.ReflectionLimits{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reflector.RequestTokenEstimate(input); err == nil {
			t.Fatal("ineligible estimation accepted")
		}
		if _, _, err := reflector.Reflect(t.Context(), input); err == nil {
			t.Fatal("ineligible reflection accepted")
		}
		if provider.Calls() != 0 {
			t.Fatal("ineligible reflection called provider")
		}
	}
}

func TestUserModelReviewerExcludesLegacyAndUnprovenTrajectories(t *testing.T) {
	sess := legacyLearningSession(t)
	store := memstore.New()
	if err := store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	provider := mockllm.New(mockllm.TextTurn("must not run"))
	reviewer := agent.NewUserModelReviewer(store, newEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog()}))
	if err := reviewer.Review(t.Context(), string(sess.ID)); err != nil {
		t.Fatal(err)
	}
	for _, tr := range []learning.Trajectory{
		learning.NewSessionTrajectory(sess, "/ws", session.StopEndTurn, session.Usage{}),
		learning.NewTrajectory(sess.ID, "/ws", session.StopEndTurn, session.Usage{}, sess.Conversation.Messages),
	} {
		if err := reviewer.Observe(t.Context(), tr); err != nil {
			t.Fatal(err)
		}
	}
	if provider.Calls() != 0 {
		t.Fatal("ineligible reviewer called provider")
	}
}
