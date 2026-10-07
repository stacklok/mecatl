package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type brokerReconcileTool struct {
	brokerFenceTestTool
	status                     tool.BrokerAttemptStatus
	inspectErr                 error
	inspects, acknowledgements int
}

func (t *brokerReconcileTool) InspectBrokerAttempt(context.Context, session.BrokerAttempt) (tool.BrokerAttemptStatus, error) {
	t.inspects++
	return t.status, t.inspectErr
}
func (t *brokerReconcileTool) AcknowledgeBrokerAttempt(_ context.Context, attempt session.BrokerAttempt) (tool.BrokerAttemptStatus, error) {
	t.acknowledgements++
	return tool.BrokerAttemptStatus{Attempt: attempt, Phase: "terminal", Disposition: session.BrokerAttemptNotDispatched}, nil
}

func brokerReconcileFixture(t *testing.T) (*session.Session, *Engine, *brokerReconcileTool, session.ToolCall, session.BrokerAttempt) {
	t.Helper()
	ref := session.BrokerSessionRef(base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	raw := make([]byte, 32)
	raw[0] = 1
	cat := session.BrokerCatalogueRef(base64.RawURLEncoding.EncodeToString(raw))
	s := session.New("broker-reconciliation", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "fixture", Revision: "1"}, session.Limits{}, time.Now())
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
	if err := e.saveRequired(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	remote := &brokerReconcileTool{brokerFenceTestTool: brokerFenceTestTool{ref: ref, cat: cat}}
	return s, e, remote, call, attempt
}

func TestBrokerHostWiring_ReconciliationEvidence(t *testing.T) {
	for _, phase := range []string{"not_admitted", "reserved", "parked", "dispatched", "completed", "unknown", "wrong-attempt", "generic-error"} {
		t.Run(phase, func(t *testing.T) {
			s, e, remote, call, attempt := brokerReconcileFixture(t)
			remote.status = tool.BrokerAttemptStatus{Attempt: attempt, Phase: phase}
			switch phase {
			case "completed", "unknown":
				remote.status.Phase = "terminal"
				remote.status.Disposition = session.BrokerAttemptDisposition(phase)
			case "wrong-attempt":
				remote.status = tool.BrokerAttemptStatus{Attempt: session.BrokerAttempt{Sequence: 2}, Phase: "not_admitted"}
			case "generic-error":
				remote.inspectErr = errors.New("FailedPrecondition")
			}
			before, _ := s.BrokerAccess()
			if err := s.RestoreBrokerAccess(before); err != nil {
				t.Fatal(err)
			}
			_, err := e.prepareBrokerAttempt(t.Context(), s, call, remote)
			allowed := phase == "not_admitted" || phase == "reserved" || phase == "parked"
			if (err == nil) != allowed {
				t.Fatalf("prepare error=%v allowed=%v", err, allowed)
			}
			if remote.inspects != 1 || remote.calls != 0 {
				t.Fatal("restore was not passive inspection first")
			}
			a, _ := s.BrokerAccess()
			if !allowed {
				if *a.Current != *before.Current || remote.acknowledgements != 0 {
					t.Fatal("uncertainty released or acknowledged")
				}
				return
			}
			want := uint64(2)
			if phase == "not_admitted" {
				want = 1
			}
			if a.Current.Attempt.Sequence != want || a.AdmittedSequence != want-1 {
				t.Fatalf("sequence gap: %+v", a)
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
	before, _ := s.BrokerAccess()
	if err := s.RestoreBrokerAccess(before); err != nil {
		t.Fatal(err)
	}
	if _, err := e.prepareBrokerAttempt(ctx, s, call, remote); err == nil {
		t.Fatal("attempt framing bypassed restored-host reconciliation")
	}
	if remote.calls != 0 {
		t.Fatal("restored context executed a call")
	}
}

func TestBrokerHostWiring_SettlementSaveFailureKeepsFence(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonadmission", true: "completion"}[completed], func(t *testing.T) {
			s, e, remote, call, attempt := brokerReconcileFixture(t)
			e.deps.Store = &brokerFenceFailStore{memstore.New()}
			if completed {
				if err := s.DispatchBrokerInvocation(attempt); err != nil {
					t.Fatal(err)
				}
				if err := s.RecordBrokerInvocationResult(attempt, call.ID); err != nil {
					t.Fatal(err)
				}
				if err := e.recordBrokerResults(t.Context(), s, []session.ToolResult{session.NewToolResult(call.ID, "ok")}); err == nil {
					t.Fatal("save error lost")
				}
			} else {
				remote.status = tool.BrokerAttemptStatus{Attempt: attempt, Phase: "not_admitted"}
				if err := e.reconcileBrokerAttempt(t.Context(), s, remote, attempt); err == nil {
					t.Fatal("save error lost")
				}
			}
			a, _ := s.BrokerAccess()
			if a.Current == nil || a.Current.Attempt != attempt || a.Current.Phase == "terminal" {
				t.Fatal("failed save released fence")
			}
			if _, err := s.PrepareBrokerInvocation(remote.ref, remote.cat, call, time.Now()); err == nil {
				t.Fatal("same-ID history released unsaved occurrence")
			}
		})
	}
}
