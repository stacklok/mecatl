package agent

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func brokerReconcileFixture(t *testing.T) (*session.Session, *Engine, *brokerFenceTestTool, session.ToolCall, session.BrokerAttempt) {
	t.Helper()
	ref := session.BrokerSessionRef(base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	raw := make([]byte, 32)
	raw[0] = 1
	cat := session.BrokerCatalogueRef(base64.RawURLEncoding.EncodeToString(raw))
	s := session.New("broker-uncertainty", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "fixture", Revision: "1"}, session.Limits{}, time.Now())
	if err := s.BindAuthority(session.Authority{Provenance: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AdoptBrokerCatalogue(ref, cat, session.BrokerConnectionRef(cat), time.Now().Add(time.Hour), []string{"remote"}); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("same", "remote", []byte(`{}`))
	if err := s.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatal(err)
	}
	attempt, err := s.PrepareBrokerInvocation(ref, cat, call, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(Deps{Store: memstore.New()})
	return s, e, &brokerFenceTestTool{ref: ref, cat: cat}, call, attempt
}

type brokerContinuationAbortTool struct {
	*brokerFenceTestTool
	attempt       session.BrokerAttempt
	authorization session.ExternalAuthorization
	aborts        int
	t             *testing.T
}

func (b *brokerContinuationAbortTool) RequestAuthorization(context.Context, session.ToolCall) (session.ExternalAuthorization, bool, error) {
	return b.authorization, true, nil
}

func (b *brokerContinuationAbortTool) AbortAuthorization(ctx context.Context, authorization session.ExternalAuthorization) error {
	attempt, ok := tool.BrokerInvocationFromContext(ctx)
	if !ok || attempt != b.attempt || authorization != b.authorization {
		b.t.Error("cancellation did not target exact parked occurrence")
	}
	if ctx.Err() != nil {
		b.t.Error("cancellation inherited cancelled caller context")
	}
	b.aborts++
	return nil
}

func TestBrokerContinuationPlanRejectionCancelsOnlyUndispatchedOccurrence(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		t.Run(map[bool]string{false: "prepared", true: "uncertain"}[dispatched], func(t *testing.T) {
			sess, engine, base, call, attempt := brokerReconcileFixture(t)
			authorization := session.ExternalAuthorization{ID: session.NewBrokerAttempt().ID, Binding: session.AuthorizationBinding(base.ref), ExpiresAt: time.Now().Add(time.Hour)}
			remote := &brokerContinuationAbortTool{brokerFenceTestTool: base, attempt: attempt, authorization: authorization, t: t}
			engine.deps.Catalog = tool.NewCatalog()
			if err := engine.deps.Catalog.Register(remote); err != nil {
				t.Fatal(err)
			}
			if err := sess.PauseForAuthorization(session.PendingAuthorization{Authorization: authorization, Call: call}); err != nil {
				t.Fatal(err)
			}
			pending, err := sess.ClaimAuthorization()
			if err != nil {
				t.Fatal(err)
			}
			sess.Mode = session.ModePlan
			if dispatched {
				if err := sess.DispatchBrokerInvocation(attempt); err != nil {
					t.Fatal(err)
				}
			}
			resolution, err := session.NewAuthorizationResolution(session.AuthorizationGranted)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := engine.PrepareAuthorizationContinuation(t.Context(), sess, MemEnv("/broker"), pending, resolution)
			if err != nil {
				t.Fatal(err)
			}
			run, _ := prepared.Start()
			for range run.Events() {
			}
			wantAborts := 1
			if dispatched {
				wantAborts = 0
			}
			if remote.aborts != wantAborts || base.calls != 0 {
				t.Fatalf("aborts=%d want=%d executions=%d", remote.aborts, wantAborts, base.calls)
			}
		})
	}
}

func TestBrokerHostWiring_ContextCannotReattachRestoredAttempt(t *testing.T) {
	s, e, remote, call, attempt := brokerReconcileFixture(t)
	ctx := tool.WithBrokerInvocation(t.Context(), attempt)
	if _, err := e.prepareBrokerAttempt(ctx, s, call, remote); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchBrokerInvocation(attempt); err != nil {
		t.Fatal(err)
	}
	before, _ := s.BrokerAccess()
	if err := s.RestoreBrokerAccess(before); err != nil {
		t.Fatal(err)
	}
	if _, err := e.prepareBrokerAttempt(ctx, s, call, remote); err == nil {
		t.Fatal("restored context authorized resend")
	}
	if _, err := e.prepareBrokerAttempt(t.Context(), s, call, remote); err == nil {
		t.Fatal("new context authorized resend")
	}
	if remote.calls != 0 {
		t.Fatal("restored context executed a call")
	}
}

func TestBrokerHostWiring_SettlementSaveFailureKeepsFence(t *testing.T) {
	s, e, remote, call, attempt := brokerReconcileFixture(t)
	if err := s.DispatchBrokerInvocation(attempt); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordBrokerInvocationResult(attempt, call.ID); err != nil {
		t.Fatal(err)
	}
	e.deps.Store = &brokerFenceFailStore{memstore.New()}
	if err := e.recordBrokerResults(t.Context(), s, []session.ToolResult{session.NewToolResult(call.ID, "ok")}); err == nil {
		t.Fatal("save error lost")
	}
	a, _ := s.BrokerAccess()
	if a.Current == nil || a.Current.Attempt != attempt {
		t.Fatal("failed save released fence")
	}
	if _, err := s.PrepareBrokerInvocation(remote.ref, remote.cat, call, time.Now()); err == nil {
		t.Fatal("same-ID history released unsaved occurrence")
	}
}
