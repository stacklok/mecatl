package agent_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func abandonedContinuation(t *testing.T, source string) *session.Session {
	t.Helper()
	if strings.HasPrefix(source, "events-") {
		control := `{"Type":"permission.ask","Ask":{"AskID":"obsolete-ask","Tool":"Read","Call":"duplicate"}}`
		if source == "events-auth" {
			control = `{"Type":"authorization.required","Authorization":{"AuthorizationID":"obsolete-auth","Call":"duplicate","Status":"pending"}}`
		}
		var events []session.Event
		for i, raw := range []string{
			`{"Type":"tool.call","ToolCall":{"ID":"duplicate","Name":"Read","Args":{"path":"a"}}}`,
			`{"Type":"tool.call","ToolCall":{"ID":"duplicate","Name":"Read","Args":{"path":"b"}}}`,
			control,
		} {
			e, err := eventcodec.Unmarshal([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, e.WithLegacySource("agent-regression", fmt.Sprint(i)))
		}
		s, err := eventsource.Fold(eventsource.SessionMeta{ID: "abandoned-events", Mode: session.ModeDefault, EnvironmentRef: agent.MemEnv("/ws").Ref(), CreatedAt: time.Unix(0, 0)}, func(yield func(session.Event, error) bool) {
			for _, e := range events {
				if !yield(e, nil) {
					return
				}
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	fixture := source
	switch source {
	case "snapshot-auth":
		fixture = "legacy-authorizing"
	case "snapshot-ask":
		fixture = "legacy-ambiguous-pending"
	}
	data, err := os.ReadFile("../adapter/sessnap/testdata/" + fixture + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if source == "snapshot-auth" {
		data = bytes.ReplaceAll(data, []byte("provider-auth-call"), []byte("provider-call-reused"))
	}
	s, err := sessnap.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAbandonedContinuationControlsAreInert(t *testing.T) {
	for _, source := range []string{"snapshot-ask", "snapshot-auth", "candidate-held", "candidate-held-event", "events-ask", "events-auth"} {
		t.Run(source, func(t *testing.T) {
			store := &saveCountingStore{inner: memstore.New()}
			rules := permstore.New()
			instructions := &countingAssembler{msg: "new work instructions"}
			provider := mockllm.New(mockllm.TextTurn("unreachable"))
			executed := 0
			read := &fakeTool{name: "Read", exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				executed++
				return session.NewToolResult(c.ID, "unreachable"), nil
			}}
			e := newEngine(agent.Deps{LLM: provider, Catalog: catalogWith(t, read), Policy: permpolicy.NewPolicy(nil, rules), Store: store, Instructions: instructions})
			s := abandonedContinuation(t, source)
			if s.State != session.StateIdle || store.saves.Load() != 0 || provider.Calls() != 0 || executed != 0 || instructions.called != 0 || len(rules.Rules(s.ID)) != 0 {
				t.Fatal("loading performed work or did not abandon continuation")
			}
			mem := agent.MemEnv("/ws")
			env := tool.MustEnvironment(s.EnvironmentRef, mem.Workspace(), mem.ReadLedger(), nil)
			resolution, _ := session.NewAuthorizationResolution(session.AuthorizationGranted)
			stale := session.PendingAuthorization{Authorization: session.ExternalAuthorization{ID: "obsolete-auth", Binding: "old-binding", ExpiresAt: time.Unix(100, 0)}, Call: session.NewToolCall("provider-call-reused", "Read", []byte(`{"path":"a"}`))}
			for cycle := range 3 {
				before, err := sessnap.Marshal(s)
				if err != nil {
					t.Fatal(err)
				}
				for _, suppliedEnv := range []tool.Environment{env, {}} {
					for _, askID := range []string{"ask-ambiguous", "obsolete-ask"} {
						if lastResult(t, resumeEvents(e.ResumeApproval(t.Context(), s, suppliedEnv, askID, session.VerdictAllowAlways))).Stop != session.StopError {
							t.Fatal("stale approval accepted")
						}
					}
					if lastResult(t, resumeEvents(e.RetryFailedStep(t.Context(), s, suppliedEnv))).Stop != session.StopError {
						t.Fatal("unprepared retry accepted")
					}
					for _, prepare := range []func() (*agent.PreparedRun, error){
						func() (*agent.PreparedRun, error) {
							return e.PrepareAuthorizationContinuation(t.Context(), s, suppliedEnv, stale, resolution)
						},
						func() (*agent.PreparedRun, error) {
							return e.PrepareAfterAuthorization(t.Context(), s, suppliedEnv, stale.Authorization, stale.Call.ID, nil, resolution)
						},
					} {
						prepared, err := prepare()
						if prepared != nil {
							prepared.Abort()
						}
						if err == nil || prepared != nil {
							t.Error("stale authorization prepared a run")
						}
					}
				}
				if _, err := s.ClaimAuthorization(); err == nil {
					t.Error("abandoned authorization claimed")
				}
				if err := s.RestoreAuthorizationClaim(stale); err == nil {
					t.Error("stale authorization restored")
				}
				if err := s.PrepareFailedStepRetry(); err == nil {
					t.Error("abandoned work acquired retry intent")
				}
				after, err := sessnap.Marshal(s)
				if err != nil || !bytes.Equal(before, after) || s.State != session.StateIdle {
					t.Error("stale controls mutated normalized session")
				}
				if provider.Calls() != 0 || executed != 0 || instructions.called != 0 || len(rules.Rules(s.ID)) != 0 || store.saves.Load() != int32(cycle) {
					t.Errorf("stale controls caused effects: model=%d tools=%d preparation=%d grants=%d saves=%d", provider.Calls(), executed, instructions.called, len(rules.Rules(s.ID)), store.saves.Load())
				}
				if cycle < 2 {
					if err := store.Save(t.Context(), s); err != nil {
						t.Fatal(err)
					}
					s, err = store.Load(t.Context(), s.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestAbandonedContinuationSeparatePromptRunsNormally(t *testing.T) {
	for _, source := range []string{"snapshot-ask", "snapshot-auth", "candidate-held", "candidate-held-event", "events-ask", "events-auth"} {
		t.Run(source, func(t *testing.T) {
			s := abandonedContinuation(t, source)
			store := memstore.New()
			for range 2 {
				if err := store.Save(t.Context(), s); err != nil {
					t.Fatal(err)
				}
				var err error
				s, err = store.Load(t.Context(), s.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := port.ProjectMessages(s.Conversation.Messages); err != nil {
				t.Fatal(err)
			}
			var request port.LLMRequest
			provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { request = req })}, mockllm.TextTurn("new work completed"))
			instructions := &countingAssembler{msg: "new work instructions"}
			e := newEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), Store: store, Instructions: instructions})
			mem := agent.MemEnv("/ws")
			env := tool.MustEnvironment(s.EnvironmentRef, mem.Workspace(), mem.ReadLedger(), nil)
			result := lastResult(t, resumeEvents(e.Run(t.Context(), s, env, agent.RunRequest{Text: "separate new prompt"})))
			if result.Stop != session.StopEndTurn || provider.Calls() != 1 || instructions.called != 1 || s.State != session.StateCompleted {
				t.Fatalf("new prompt: result=%+v model=%d preparation=%d state=%s", result, provider.Calls(), instructions.called, s.State)
			}
			var notice, prompt bool
			for _, m := range request.Messages {
				notice = notice || strings.Contains(m.Text, "abandoned during upgrade")
				prompt = prompt || m.Text == "separate new prompt"
			}
			if !notice || !prompt {
				t.Fatal("model did not receive normalized history and separate prompt")
			}
		})
	}
}
