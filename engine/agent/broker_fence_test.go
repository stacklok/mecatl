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

type brokerFenceTestTool struct {
	calls int
	err   error
	ref   session.BrokerSessionRef
	cat   session.BrokerCatalogueRef
}

func (*brokerFenceTestTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "remote", Schema: []byte(`{"type":"object"}`)}
}
func (*brokerFenceTestTool) ReadOnly() bool      { return false }
func (*brokerFenceTestTool) DispatchSerialTool() {}
func (t *brokerFenceTestTool) BrokerInvocationRefs() (session.BrokerSessionRef, session.BrokerCatalogueRef) {
	return t.ref, t.cat
}
func (t *brokerFenceTestTool) BrokerInvocationDisposition(err error) session.BrokerAttemptDisposition {
	if err == nil {
		return session.BrokerAttemptCompleted
	}
	return session.BrokerAttemptUnknown
}
func (*brokerFenceTestTool) InspectBrokerAttempt(context.Context, session.BrokerAttempt) (tool.BrokerAttemptStatus, error) {
	return tool.BrokerAttemptStatus{}, errors.New("inspection unavailable")
}
func (*brokerFenceTestTool) AcknowledgeBrokerAttempt(context.Context, session.BrokerAttempt) (tool.BrokerAttemptStatus, error) {
	return tool.BrokerAttemptStatus{}, errors.New("acknowledgement unavailable")
}
func (t *brokerFenceTestTool) Execute(_ context.Context, c session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.calls++
	return session.NewToolResult(c.ID, "ok"), t.err
}

type brokerFenceFailStore struct{ *memstore.Store }

func (*brokerFenceFailStore) Save(context.Context, *session.Session) error {
	return errors.New("save failed")
}

func TestBrokerInvocationFenceSaveFailureAndCrashUnknown(t *testing.T) {
	ref := session.BrokerSessionRef(base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	raw := make([]byte, 32)
	raw[0] = 1
	cat := session.BrokerCatalogueRef(base64.RawURLEncoding.EncodeToString(raw))
	for _, failSave := range []bool{true, false} {
		t.Run(map[bool]string{true: "save-failure", false: "crash-unknown"}[failSave], func(t *testing.T) {
			store := memstore.New()
			sess := session.New("broker-fence", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "fence", Revision: "1"}, session.Limits{}, time.Now())
			if err := sess.BindAuthority(session.Authority{Provenance: "fixture"}); err != nil {
				t.Fatal(err)
			}
			if err := sess.AdoptBrokerCatalogue(ref, cat, session.BrokerConnectionRef(cat), time.Now().Add(time.Hour), []string{"remote"}); err != nil {
				t.Fatal(err)
			}
			deps := Deps{Store: store}
			if failSave {
				deps.Store = &brokerFenceFailStore{store}
			}
			e := NewEngine(deps)
			remote := &brokerFenceTestTool{ref: ref, cat: cat, err: errors.New("outcome unknown")}
			call := session.NewToolCall("original", "remote", []byte(`{}`))
			ctx, prepareErr := e.prepareBrokerAttempt(t.Context(), sess, call, remote)
			result := session.NewToolError(call.ID, "preparation failed")
			if prepareErr == nil {
				result, _ = e.timeExecute(ctx, &Run{}, sess, tool.Environment{}, 0, call, remote, time.Time{})
			}
			if !result.IsError {
				t.Fatal("unknown/save failure invented success")
			}
			if failSave {
				if remote.calls != 0 {
					t.Fatal("executed before durable save")
				}
				return
			}
			restored, err := store.Load(t.Context(), sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			a, _ := restored.BrokerAccess()
			if a.Current == nil || a.Current.CallID != call.ID || a.Current.Disposition != session.BrokerAttemptUnknown {
				t.Fatal("unresolved invocation not durable")
			}
			if err := restored.AdoptBrokerCatalogue(ref, cat, session.BrokerConnectionRef(cat), time.Now().Add(time.Hour), []string{"remote"}); err != nil {
				t.Fatal(err)
			}
			// Even a newly generated call ID after model/crash repair cannot dispatch.
			if _, err := restored.PrepareBrokerInvocation(ref, cat, session.NewToolCall("different-id", "remote", []byte(`{}`)), time.Now()); err == nil {
				t.Fatal("unknown crash auto-reexecuted")
			}
		})
	}
}
