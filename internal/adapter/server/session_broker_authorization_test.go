package server

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

type sessionHostAuthDescriptor struct{}

func (sessionHostAuthDescriptor) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "mcp__fixture__echo", Schema: []byte(`{"type":"object","properties":{"text":{"type":"string"}}}`)}
}
func (sessionHostAuthDescriptor) ReadOnly() bool { return false }
func (sessionHostAuthDescriptor) Execute(context.Context, session.ToolCall, tool.Environment) (session.ToolResult, error) {
	return session.ToolResult{}, errors.New("descriptor must not execute")
}
func (sessionHostAuthDescriptor) RequestAuthorization(context.Context, session.ToolCall) (session.ExternalAuthorization, bool, error) {
	return session.ExternalAuthorization{}, false, nil
}
func (sessionHostAuthDescriptor) AbortAuthorization(context.Context, session.ExternalAuthorization) error {
	return nil
}

type sessionHostAuthBroker struct {
	c.SessionService
	ref              c.SessionRef
	initial, granted c.Catalogue
	auth             c.AuthorizationRef
	expires          time.Time
	parked           c.Call
	resumes          int
	store            *sessionBrokerFailStore
	t                *testing.T
}

func hostProofRef(b byte) string {
	raw := make([]byte, 32)
	raw[0] = b
	return base64.RawURLEncoding.EncodeToString(raw)
}
func (b *sessionHostAuthBroker) OpenSession(context.Context, *c.SessionRef) (c.SessionSnapshot, error) {
	return c.SessionSnapshot{Ref: b.ref, ExpiresAt: b.expires, Catalogue: b.initial}, nil
}
func (b *sessionHostAuthBroker) BeginEnrollment(context.Context, c.SessionRef) (c.BeginEnrollmentOutcome, error) {
	return c.BeginEnrollmentOutcome{Kind: c.EnrollmentCompletedKind, Catalogue: b.initial}, nil
}
func (b *sessionHostAuthBroker) CheckAuthorization(ctx context.Context, ref c.SessionRef, cat c.CatalogueRef, call *c.Call, auth c.AuthorizationRef, attempt c.BrokerAttempt) (c.AuthorizationCheck, error) {
	if ref != b.ref {
		return c.AuthorizationCheck{}, errors.New("wrong session")
	}
	if call != nil {
		durable, err := b.store.Load(ctx, "broker-session-host")
		if err != nil {
			return c.AuthorizationCheck{}, err
		}
		a, _ := durable.BrokerAccess()
		if a.Current != nil || !attempt.Valid() {
			b.t.Errorf("preflight wrote uncertainty or omitted occurrence: %+v", a.Current)
			return c.AuthorizationCheck{}, errors.New("invalid preflight")
		}
		b.parked = *call
		b.parked.Arguments = append([]byte(nil), call.Arguments...)
		return c.AuthorizationCheck{Authorization: b.auth, ExpiresAt: b.expires}, nil
	}
	if auth != b.auth || cat != b.granted.Ref() {
		return c.AuthorizationCheck{}, errors.New("wrong resume refs")
	}
	return c.AuthorizationCheck{Ready: true}, nil
}
func (b *sessionHostAuthBroker) ObserveAuthorization(context.Context, c.SessionRef, c.AuthorizationRef) (c.FlowStatus, error) {
	return c.FlowStatus{Kind: c.FlowCompleted, Catalogue: b.granted}, nil
}
func (b *sessionHostAuthBroker) ResumeTool(_ context.Context, ref c.SessionRef, auth c.AuthorizationRef, cat c.CatalogueRef, attempt c.BrokerAttempt) (c.InvocationOutcome, error) {
	b.resumes++
	durable, err := b.store.Load(context.Background(), "broker-session-host")
	if err != nil {
		b.t.Fatal(err)
	}
	a, _ := durable.BrokerAccess()
	if a.Session != ref || a.Catalogue != cat || a.Current == nil || a.Current.CallID != b.parked.ID || a.Current.Attempt != attempt || a.Current.Digest != session.BrokerCallDigest(session.NewToolCall(b.parked.ID, b.parked.Name, b.parked.Arguments)) || auth != b.auth {
		b.t.Fatalf("resume before exact durable adoption/fence: %+v", a)
	}
	result := session.NewToolResult(b.parked.ID, string(b.parked.Arguments))
	return c.InvocationOutcome{Kind: c.InvocationCompleted, Result: &result}, nil
}
func (b *sessionHostAuthBroker) CancelAuthorization(context.Context, c.SessionRef, c.AuthorizationRef, c.BrokerAttempt) (c.CancelResult, error) {
	return c.Cancelled, nil
}

func (b *sessionHostAuthBroker) InvokeTool(context.Context, c.SessionRef, c.CatalogueRef, c.Call, c.BrokerAttempt) (c.InvocationOutcome, error) {
	b.t.Error("host invoked instead of exact Resume")
	return c.InvocationOutcome{}, errors.New("unexpected invoke")
}

func TestSessionBrokerHostAuthorizationExactResumeAndAdoptionSaveFailure(t *testing.T) {
	for _, mode := range []string{"resume", "definitive", "ambiguous", "restart", "parking-definitive", "parking-ambiguous", "dispatch-save-failure", "dispatch-save-ambiguous", "completion-save-failure"} {
		fail := mode == "definitive" || mode == "ambiguous"
		t.Run(mode, func(t *testing.T) {
			store := &sessionBrokerFailStore{Store: memstore.New()}
			initial, err := c.NewCatalogue(c.CatalogueRef(hostProofRef(2)), c.ConnectionRef(hostProofRef(5)), []tool.Tool{sessionHostAuthDescriptor{}})
			if err != nil {
				t.Fatal(err)
			}
			granted, err := c.NewCatalogue(c.CatalogueRef(hostProofRef(3)), c.ConnectionRef(hostProofRef(5)), []tool.Tool{sessionHostAuthDescriptor{}, brokerContributionDescriptor{name: "mcp__fixture__new"}})
			if err != nil {
				t.Fatal(err)
			}
			broker := &sessionHostAuthBroker{ref: c.SessionRef(hostProofRef(1)), initial: initial, granted: granted, auth: c.AuthorizationRef(hostProofRef(4)), expires: time.Now().Add(time.Hour), store: store, t: t}
			client := sessionBrokerRPCClient(t, broker, &session.Principal{Issuer: "https://owner.test", Subject: "owner"})
			exact := []byte("{ \"text\" : \"original\" }")
			provider := mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("parked", "mcp__fixture__echo", exact)), mockllm.TextTurn("done"))
			svc := sessionBrokerTestHost(t, client, store, provider, nil)
			created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
				t.Fatal(err)
			}
			if mode == "parking-definitive" || mode == "parking-ambiguous" {
				store.phase.Store("parked")
				store.ambiguous.Store(mode == "parking-ambiguous")
			}
			run, err := svc.StartInteractiveRunContent(t.Context(), created.ID, "call", nil)
			if err != nil {
				t.Fatal(err)
			}
			for range run.Events() {
			}
			parked := svc.runs[created.ID].sess
			svc.FinishRun(created.ID, run)
			saved, err := store.Load(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "parking-definitive" || mode == "parking-ambiguous" {
				if broker.parked.ID == "" || broker.resumes != 0 {
					t.Fatal("parking save failure executed or skipped preflight")
				}
				a, _ := saved.BrokerAccess()
				if a.Current != nil {
					t.Fatal("nonexecuting preflight persisted uncertainty")
				}
				return
			}
			pending, ok := saved.PendingAuthorization()
			if !ok {
				t.Fatalf("not parked: %s", saved.State)
			}
			if string(broker.parked.Arguments) != string(exact) || broker.resumes != 0 {
				t.Fatal("preflight executed or changed bytes")
			}
			if mode == "restart" {
				// Simulate loss without graceful shutdown settling pending authorization.
				svc = sessionBrokerTestHost(t, client, store, mockllm.New(mockllm.TextTurn("done")), nil)
			}
			if fail {
				store.ambiguous.Store(mode == "ambiguous")
				store.fail.Store(true)
				conversation := parked.Conversation
				before := parked.Authority.Clone()
				if _, err := svc.sessionBrokerResumeTools(t.Context(), parked, pending); err == nil {
					t.Fatal("adoption save failure exposed resume")
				}
				if broker.resumes != 0 {
					t.Fatal("save failure executed")
				}
				durable, _ := store.Load(t.Context(), created.ID)
				a, _ := durable.BrokerAccess()
				wantCatalogue := initial.Ref()
				if mode == "ambiguous" {
					wantCatalogue = granted.Ref()
				}
				if a.Catalogue != wantCatalogue {
					t.Fatal("unexpected durable save outcome")
				}
				if !reflect.DeepEqual(before, parked.Authority) || parked.Conversation != conversation {
					t.Fatal("failed adoption widened or replaced parked aggregate")
				}
				if _, cached := svc.sessionEngines[created.ID]; cached {
					t.Fatal("failed save retained a cached engine instead of requiring reload")
				}
				return
			}
			if mode == "dispatch-save-failure" || mode == "dispatch-save-ambiguous" {
				store.phase.Store("dispatched")
				store.ambiguous.Store(mode == "dispatch-save-ambiguous")
			}
			if mode == "completion-save-failure" {
				store.phase.Store("terminal")
			}
			result, err := svc.RecheckMCPAuthorization(t.Context(), created.ID, MCPAuthorizationControl{SessionID: created.ID, AuthorizationID: pending.Authorization.ID})
			if err != nil {
				t.Fatal(err)
			}
			if result.Run == nil {
				t.Fatal("no continuation")
			}
			for range result.Run.Events() {
			}
			local, _ := svc.runs[created.ID].sess.BrokerAccess()
			svc.FinishRun(created.ID, result.Run)
			if mode == "dispatch-save-failure" || mode == "dispatch-save-ambiguous" || mode == "completion-save-failure" {
				durable, err := store.Load(t.Context(), created.ID)
				if err != nil {
					t.Fatal(err)
				}
				a, _ := durable.BrokerAccess()
				if mode == "dispatch-save-failure" || mode == "dispatch-save-ambiguous" {
					if broker.resumes != 0 || local.Current == nil || (mode == "dispatch-save-ambiguous" && a.Current == nil) {
						t.Fatalf("dispatch save failure executed or lost uncertainty: calls=%d local=%+v durable=%+v", broker.resumes, local.Current, a.Current)
					}
				} else {
					if broker.resumes != 1 || a.Current == nil {
						t.Fatalf("unsaved completion released exact occurrence: calls=%d current=%+v", broker.resumes, a.Current)
					}
					if _, err := durable.PrepareBrokerInvocation(a.Session, a.Catalogue, pending.Call, time.Now()); err == nil {
						t.Fatal("same-ID paired history released unsaved completion")
					}
				}
				return
			}
			if broker.resumes != 1 || string(broker.parked.Arguments) != string(exact) {
				t.Fatalf("resume count/bytes: %d %q", broker.resumes, broker.parked.Arguments)
			}
			durable, err := store.Load(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			a, _ := durable.BrokerAccess()
			if a.Current != nil {
				t.Fatal("paired completion did not release unresolved fence")
			}
		})
	}
}
