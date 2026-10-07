package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/localauthority"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
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
func (t *brokerFenceTestTool) Execute(_ context.Context, c session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.calls++
	return session.NewToolResult(c.ID, "ok"), t.err
}

type brokerLateAuthorizationTool struct {
	*brokerFenceTestTool
	store *memstore.Store
	t     *testing.T
	id    session.SessionID
}

func (b *brokerLateAuthorizationTool) RequestAuthorization(ctx context.Context, _ session.ToolCall) (session.ExternalAuthorization, bool, error) {
	saved, err := b.store.Load(ctx, b.id)
	if err != nil {
		return session.ExternalAuthorization{}, false, err
	}
	if a, _ := saved.BrokerAccess(); a.Current != nil {
		b.t.Error("nonexecuting preflight persisted uncertainty")
	}
	return session.ExternalAuthorization{}, false, nil
}
func (*brokerLateAuthorizationTool) AbortAuthorization(context.Context, session.ExternalAuthorization) error {
	return nil
}
func (b *brokerLateAuthorizationTool) Execute(ctx context.Context, call session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	saved, err := b.store.Load(ctx, b.id)
	if err != nil {
		return session.ToolResult{}, err
	}
	attempt, _ := tool.BrokerInvocationFromContext(ctx)
	if a, _ := saved.BrokerAccess(); a.Current == nil || a.Current.Attempt != attempt || a.Current.Digest != session.BrokerCallDigest(call) {
		b.t.Error("Invoke reached broker before exact uncertainty was saved")
	}
	return b.brokerFenceTestTool.Execute(ctx, call, env)
}

type brokerParkFailStore struct {
	*memstore.Store
	ambiguous bool
}

func (s *brokerParkFailStore) Save(ctx context.Context, sess *session.Session) error {
	if sess.State == session.StateAuthorizing {
		if s.ambiguous {
			if err := s.Store.Save(ctx, sess); err != nil {
				return err
			}
		}
		return errors.New("parking save failed")
	}
	return s.Store.Save(ctx, sess)
}

func TestBrokerAuthorizationRequiredAfterReadyAtomicallyReplacesUncertainty(t *testing.T) {
	for _, mode := range []string{"success", "save-failure", "ambiguous-save"} {
		t.Run(mode, func(t *testing.T) {
			_, _, base, call, _ := brokerReconcileFixture(t)
			store := memstore.New()
			env := MemEnv("/broker")
			sess := session.New("late-authorization", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
			if err := sess.BindAuthority(session.Authority{Provenance: "test"}); err != nil {
				t.Fatal(err)
			}
			if err := sess.AdoptBrokerCatalogue(base.ref, base.cat, session.BrokerConnectionRef(base.cat), time.Now().Add(time.Hour), []string{call.Name}); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(t.Context(), sess); err != nil {
				t.Fatal(err)
			}
			base.err = &tool.BrokerAuthorizationRequired{Authorization: session.ExternalAuthorization{ID: session.NewBrokerAttempt().ID, Binding: session.AuthorizationBinding(base.ref), ExpiresAt: time.Now().Add(time.Hour)}}
			catalog := tool.NewCatalog()
			if err := catalog.Register(&brokerLateAuthorizationTool{base, store, t, sess.ID}); err != nil {
				t.Fatal(err)
			}
			deps := Deps{Store: store, Catalog: catalog, LLM: mockllm.New(mockllm.ToolCallTurn(call)), AuthorityEvaluator: localauthority.New(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)}
			if mode != "success" {
				deps.Store = &brokerParkFailStore{store, mode == "ambiguous-save"}
			}
			for range NewEngine(deps).Run(t.Context(), sess, env, RunRequest{Text: "call", CanPresentAuthorization: true}).Events() {
			}
			saved, err := store.Load(t.Context(), sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			a, _ := saved.BrokerAccess()
			pending, parked := saved.PendingAuthorization()
			if base.calls != 1 {
				t.Fatalf("Invoke count = %d", base.calls)
			}
			if mode == "success" {
				if a.Current != nil || !parked || session.BrokerCallDigest(pending.Call) != session.BrokerCallDigest(call) {
					t.Fatal("zero dispatch did not atomically replace uncertainty with exact pending authorization")
				}
			} else if a.Current == nil || parked {
				t.Fatal("failed parking save released uncertainty or retained pending authorization")
			}
		})
	}
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
				result, _, _, _ = e.timeExecute(ctx, &Run{}, sess, tool.Environment{}, 0, call, remote, time.Time{})
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
			if a.Current == nil || a.Current.CallID != call.ID {
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
