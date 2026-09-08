package agent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestInvariant_session_environment_identity_required(t *testing.T) {
	t.Run("aggregate construction rejects an invalid ref", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("session.New accepted an invalid EnvironmentRef")
			}
		}()
		_ = session.New("invalid", session.ModeDefault, session.EnvironmentRef{}, session.Limits{}, time.Unix(0, 0))
	})

	for _, entry := range []struct {
		name string
		run  func(*agent.Engine, *session.Session, tool.Environment) *agent.Run
	}{
		{name: "run", run: func(e *agent.Engine, s *session.Session, env tool.Environment) *agent.Run {
			return e.Run(context.Background(), s, env, agent.RunRequest{Text: "hello"})
		}},
		{name: "retry failed step", run: func(e *agent.Engine, s *session.Session, env tool.Environment) *agent.Run {
			return e.RetryFailedStep(context.Background(), s, env)
		}},
		{name: "resume approval", run: func(e *agent.Engine, s *session.Session, env tool.Environment) *agent.Run {
			return e.ResumeApproval(context.Background(), s, env, "ask", session.VerdictAllowOnce)
		}},
	} {
		t.Run(entry.name+" rejects mismatch before activity", func(t *testing.T) {
			llm := mockllm.New(mockllm.TextTurn("provider ran"))
			eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog()})
			ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "placement", Revision: "v1"}
			sess := session.New("environment-entry-all", session.ModeDefault, ref, session.Limits{}, time.Unix(0, 0))
			env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "other", Revision: "v1"}, memfs.NewWorkspace("/private/other"), memledger.New(), nil)

			var cause string
			for ev := range entry.run(eng, sess, env).Events() {
				if ev.Result != nil {
					cause = ev.Result.Error
				}
			}
			if llm.Calls() != 0 {
				t.Fatalf("provider calls = %d, want 0", llm.Calls())
			}
			if !strings.Contains(cause, "environment identity mismatch") {
				t.Fatalf("terminal cause = %q, want identity mismatch", cause)
			}
		})
	}

	t.Run("run requires an exact valid live environment before provider access", func(t *testing.T) {
		validRef := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "placement", Revision: "v1"}
		tests := []struct {
			name       string
			sessionRef session.EnvironmentRef
			envRef     session.EnvironmentRef
			reject     bool
		}{
			{name: "exact match", sessionRef: validRef, envRef: validRef},
			{name: "different kind", sessionRef: validRef, envRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "placement", Revision: "v1"}, reject: true},
			{name: "different id", sessionRef: validRef, envRef: session.EnvironmentRef{Kind: session.EnvKindMem, ID: "other", Revision: "v1"}, reject: true},
			{name: "different revision", sessionRef: validRef, envRef: session.EnvironmentRef{Kind: session.EnvKindMem, ID: "placement", Revision: "v2"}, reject: true},
			{name: "invalid session ref", sessionRef: session.EnvironmentRef{}, envRef: validRef, reject: true},
			{name: "invalid live ref", sessionRef: validRef, envRef: session.EnvironmentRef{}, reject: true},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				llm := mockllm.New(mockllm.TextTurn("provider ran"))
				eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog()})
				sess := session.New("environment-entry", session.ModeDefault, validRef, session.Limits{}, time.Unix(0, 0))
				// Session restoration validates this field, but direct Engine callers can
				// hold and mutate the aggregate. Run entry must still fail closed.
				sess.EnvironmentRef = tc.sessionRef
				env := tool.MustEnvironment(tc.envRef, memfs.NewWorkspace("/private/placement"), memledger.New(), nil)

				run := eng.Run(context.Background(), sess, env, agent.RunRequest{Text: "hello"})
				var cause string
				for ev := range run.Events() {
					if ev.Result != nil {
						cause = ev.Result.Error
					}
				}

				if tc.reject {
					if llm.Calls() != 0 {
						t.Fatalf("provider calls = %d, want 0", llm.Calls())
					}
					if !strings.Contains(cause, "environment identity mismatch") {
						t.Fatalf("terminal cause = %q, want identity mismatch", cause)
					}
					return
				}
				if llm.Calls() != 1 {
					t.Fatalf("provider calls = %d, want 1", llm.Calls())
				}
				if cause != "" {
					t.Fatalf("terminal cause = %q, want success", cause)
				}
			})
		}
	})
}
