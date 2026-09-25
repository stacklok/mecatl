package server

import (
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// TestADR_0362_NilEventLogNeverPublishesFailure pins the durable-append
// precondition at the failure recorder itself, even if a future caller bypasses
// the exact control's admission guard.
func TestADR_0362_NilEventLogNeverPublishesFailure(t *testing.T) {
	id := session.SessionID("plan-without-log")
	observed := make(chan session.Event, 1)
	svc := &Service{
		cfg: Config{Diagnostics: port.NopDiagnostics{}},
		subscriptions: map[session.SessionID]map[int64]chan session.Event{
			id: {1: observed},
		},
	}
	svc.recordPlanContinuationFailure(t.Context(), id, &planContinuation{planRunID: "plan-run", askID: "plan-ask"})
	select {
	case ev := <-observed:
		t.Fatalf("published unrecorded continuation failure: %+v", ev)
	default:
	}
}
