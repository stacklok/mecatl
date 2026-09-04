package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type enrollmentAttachment struct {
	brokercontract.Attachment
	ref           brokercontract.WorkspaceEnrollmentRef
	result        brokercontract.WorkspaceEnrollmentResult
	catalogue     brokercontract.WorkspaceCatalogue
	cancelWait    bool
	beginCalls    int
	observeCalls  int
	cancelCalls   int
	lastCancelRef brokercontract.WorkspaceEnrollmentRef
	mu            sync.Mutex
	beginEntered  chan struct{}
	releaseBegin  <-chan struct{}
}

func (a *enrollmentAttachment) BeginWorkspaceEnrollment(context.Context) (brokercontract.WorkspaceEnrollmentPresentation, error) {
	a.mu.Lock()
	a.beginCalls++
	entered, release := a.beginEntered, a.releaseBegin
	a.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if release != nil {
		<-release
	}
	return brokercontract.WorkspaceEnrollmentPresentation{Ref: a.ref, URL: "https://broker.example/authorize?state=opaque"}, nil
}

func (a *enrollmentAttachment) ObserveWorkspaceEnrollment(context.Context, brokercontract.WorkspaceEnrollmentRef) (brokercontract.WorkspaceEnrollmentResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.observeCalls++
	return a.result, nil
}

func (a *enrollmentAttachment) CancelWorkspaceEnrollment(ctx context.Context, ref brokercontract.WorkspaceEnrollmentRef) (brokercontract.WorkspaceEnrollmentResult, error) {
	a.mu.Lock()
	a.cancelCalls++
	a.lastCancelRef = ref
	cancelWait := a.cancelWait
	a.mu.Unlock()
	if cancelWait {
		<-ctx.Done()
		return brokercontract.WorkspaceEnrollmentResult{}, ctx.Err()
	}
	return brokercontract.WorkspaceEnrollmentResult{Ref: a.ref, Status: brokercontract.WorkspaceEnrollmentCancelled}, nil
}

func (a *enrollmentAttachment) Tools() []tool.Tool {
	if a.catalogue != nil {
		return a.catalogue.Tools()
	}
	return a.Attachment.Tools()
}

type enrollmentBroker struct {
	brokercontract.Service
	attachment *enrollmentAttachment
	attachErr  error
}

func (b *enrollmentBroker) AttachSession(ctx context.Context, id session.SessionID) (brokercontract.Attachment, brokercontract.AttachOutcome, error) {
	if b.attachErr != nil {
		return nil, "", b.attachErr
	}
	attachment, outcome, err := b.Service.AttachSession(ctx, id)
	if err != nil {
		return nil, outcome, err
	}
	if b.attachment == nil {
		ref := brokercontract.WorkspaceEnrollmentRef{ID: "enrollment-1", RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour)}
		b.attachment = &enrollmentAttachment{Attachment: attachment, ref: ref, result: brokercontract.WorkspaceEnrollmentResult{Ref: ref, Status: brokercontract.WorkspaceEnrollmentPending}}
	}
	return b.attachment, outcome, nil
}

type enrollmentTool struct{ name string }

func (t enrollmentTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: t.name, Schema: json.RawMessage(`{"type":"object"}`)}
}
func (enrollmentTool) ReadOnly() bool { return true }
func (enrollmentTool) Execute(context.Context, session.ToolCall, tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult("call", "ok"), nil
}

func newWorkspaceEnrollmentHTTPService(t *testing.T, ownershipEnforced bool) (*Service, *enrollmentBroker, *session.Session) {
	t.Helper()
	runtime := testBrokerRuntime(t)
	t.Cleanup(func() { _ = runtime.Close() })
	broker := &enrollmentBroker{Service: runtime}
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: memstore.New(),
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "workspace-enrollment-http" }, MCPBroker: broker,
		OwnershipEnforced: ownershipEnforced,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	createdCtx := t.Context()
	if ownershipEnforced {
		createdCtx = session.WithPrincipal(createdCtx, &session.Principal{Issuer: "https://idp.example", Subject: "owner", GrantType: session.GrantTypeUser})
	}
	created, err := svc.CreateSession(createdCtx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return svc, broker, created
}

func enrollmentCallCounts(a *enrollmentAttachment) (begin, observe, cancel int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.beginCalls, a.observeCalls, a.cancelCalls
}

func TestADR_0298_ToolHiveEnrollmentControlsRedactUpstreamStateE2E(t *testing.T) {
	type enrollmentResponse struct {
		EnrollmentID     string `json:"enrollment_id"`
		Status           string `json:"status"`
		RequiredServices uint32 `json:"required_services"`
		PresentationURL  string `json:"presentation_url"`
	}
	request := func(t *testing.T, h http.Handler, ctx context.Context, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
		resp := httptest.NewRecorder()
		h.ServeHTTP(resp, req)
		return resp
	}
	decode := func(t *testing.T, resp *httptest.ResponseRecorder) enrollmentResponse {
		t.Helper()
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(resp.Body.Bytes(), &fields); err != nil {
			t.Fatalf("decode enrollment fields: %v", err)
		}
		for name := range fields {
			switch name {
			case "enrollment_id", "status", "required_services", "presentation_url":
			default:
				t.Fatalf("unsafe enrollment response field %q", name)
			}
		}
		var result enrollmentResponse
		if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode enrollment response: %v", err)
		}
		return result
	}

	t.Run("connect observes and projects only safe fields", func(t *testing.T) {
		svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
		h := NewHTTPHandler(svc)
		path := "/v1/sessions/" + string(created.ID) + "/workspace-enrollment/connect"
		startedResp := request(t, h, t.Context(), path, "")
		if startedResp.Code != http.StatusOK {
			t.Fatalf("connect status = %d: %s", startedResp.Code, startedResp.Body.String())
		}
		started := decode(t, startedResp)
		if started.EnrollmentID != "enrollment-1" || started.Status != "pending" || started.RequiredServices != 1 || started.PresentationURL == "" {
			t.Fatalf("connect response = %#v", started)
		}
		if attachment := broker.attachment; attachment.beginCalls != 1 {
			t.Fatalf("begin calls = %d, want 1", attachment.beginCalls)
		}
		observedResp := request(t, h, t.Context(), path, "")
		if observedResp.Code != http.StatusOK {
			t.Fatalf("observe status = %d: %s", observedResp.Code, observedResp.Body.String())
		}
		observed := decode(t, observedResp)
		if observed.EnrollmentID != started.EnrollmentID || observed.Status != "pending" || observed.PresentationURL != "" {
			t.Fatalf("observed response = %#v", observed)
		}
		if attachment := broker.attachment; attachment.observeCalls != 1 {
			t.Fatalf("observe calls = %d, want 1", attachment.observeCalls)
		}
	})

	t.Run("retry uses the exact pending correlation", func(t *testing.T) {
		svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
		h := NewHTTPHandler(svc)
		base := "/v1/sessions/" + string(created.ID) + "/workspace-enrollment/"
		started := decode(t, request(t, h, t.Context(), "/v1/sessions/"+string(created.ID)+"/workspace-enrollment/connect", ""))
		resp := request(t, h, t.Context(), base+started.EnrollmentID+"/retry", "")
		if resp.Code != http.StatusOK {
			t.Fatalf("retry status = %d: %s", resp.Code, resp.Body.String())
		}
		if got := decode(t, resp); got.EnrollmentID != started.EnrollmentID || got.Status != "pending" {
			t.Fatalf("retry response = %#v", got)
		}
		attachment := broker.attachment
		if attachment.cancelCalls != 1 || !sameWorkspaceEnrollmentRef(attachment.lastCancelRef, attachment.ref) || attachment.beginCalls != 2 {
			t.Fatalf("retry calls = cancel:%d ref:%#v begin:%d", attachment.cancelCalls, attachment.lastCancelRef, attachment.beginCalls)
		}
	})

	t.Run("cancel rejects stale and malformed correlation without effects", func(t *testing.T) {
		svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
		h := NewHTTPHandler(svc)
		base := "/v1/sessions/" + string(created.ID) + "/workspace-enrollment/"
		_ = request(t, h, t.Context(), "/v1/sessions/"+string(created.ID)+"/workspace-enrollment/connect", "")
		for _, tc := range []struct {
			name, id string
			want     int
		}{{"stale", "other", http.StatusPreconditionFailed}, {"malformed", "bad%20id", http.StatusBadRequest}} {
			t.Run(tc.name, func(t *testing.T) {
				resp := request(t, h, t.Context(), base+tc.id+"/cancel", "")
				if resp.Code != tc.want {
					t.Fatalf("cancel status = %d, want %d: %s", resp.Code, tc.want, resp.Body.String())
				}
			})
		}
		if got := broker.attachment.cancelCalls; got != 0 {
			t.Fatalf("cancel calls = %d, want 0", got)
		}
	})

	t.Run("foreign owner has no broker side effects", func(t *testing.T) {
		foreign := &session.Principal{Issuer: "https://idp.example", Subject: "foreign", GrantType: session.GrantTypeUser}
		svc, broker, created := newWorkspaceEnrollmentHTTPService(t, true)
		h := NewHTTPHandler(svc)
		resp := request(t, h, session.WithPrincipal(t.Context(), foreign), "/v1/sessions/"+string(created.ID)+"/workspace-enrollment/connect", "")
		if resp.Code != http.StatusNotFound {
			t.Fatalf("foreign connect status = %d, want 404", resp.Code)
		}
		attachment := broker.attachment
		if attachment.beginCalls != 0 || attachment.observeCalls != 0 || attachment.cancelCalls != 0 {
			t.Fatalf("foreign control side effects = begin:%d observe:%d cancel:%d", attachment.beginCalls, attachment.observeCalls, attachment.cancelCalls)
		}
	})

	t.Run("rejects request bodies", func(t *testing.T) {
		svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
		h := NewHTTPHandler(svc)
		for _, path := range []string{
			"/v1/sessions/" + string(created.ID) + "/workspace-enrollment/connect",
			"/v1/sessions/" + string(created.ID) + "/workspace-enrollment/enrollment-1/retry",
			"/v1/sessions/" + string(created.ID) + "/workspace-enrollment/enrollment-1/cancel",
		} {
			resp := request(t, h, t.Context(), path, `{}`)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("body rejection status = %d, want 400: %s", resp.Code, resp.Body.String())
			}
		}
		attachment := broker.attachment
		if attachment.beginCalls != 0 || attachment.observeCalls != 0 || attachment.cancelCalls != 0 {
			t.Fatalf("body rejection side effects = begin:%d observe:%d cancel:%d", attachment.beginCalls, attachment.observeCalls, attachment.cancelCalls)
		}
	})

	t.Run("retry rejects malformed and stale IDs without disturbing the pending enrollment", func(t *testing.T) {
		svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
		h := NewHTTPHandler(svc)
		base := "/v1/sessions/" + string(created.ID) + "/workspace-enrollment/"
		started := decode(t, request(t, h, t.Context(), base+"connect", ""))
		beforeBegin, beforeObserve, beforeCancel := enrollmentCallCounts(broker.attachment)
		for _, tc := range []struct {
			name, id string
			want     int
		}{{"malformed", "bad%20id", http.StatusBadRequest}, {"stale", "other", http.StatusPreconditionFailed}} {
			t.Run(tc.name, func(t *testing.T) {
				resp := request(t, h, t.Context(), base+tc.id+"/retry", "")
				if resp.Code != tc.want {
					t.Fatalf("retry status = %d, want %d: %s", resp.Code, tc.want, resp.Body.String())
				}
			})
		}
		if begin, observe, cancel := enrollmentCallCounts(broker.attachment); begin != beforeBegin || observe != beforeObserve || cancel != beforeCancel {
			t.Fatalf("retry rejection side effects = begin:%d observe:%d cancel:%d, want %d:%d:%d", begin, observe, cancel, beforeBegin, beforeObserve, beforeCancel)
		}
		loaded, err := svc.cfg.Store.Load(t.Context(), created.ID)
		if err != nil {
			t.Fatalf("load pending enrollment: %v", err)
		}
		if pending, ok := loaded.PendingWorkspaceEnrollment(); !ok || pending.ID != session.WorkspaceEnrollmentID(started.EnrollmentID) {
			t.Fatalf("pending enrollment = %#v, %v", pending, ok)
		}
	})

	t.Run("foreign retry and cancel are non-disclosing and side-effect free", func(t *testing.T) {
		owner := &session.Principal{Issuer: "https://idp.example", Subject: "owner", GrantType: session.GrantTypeUser}
		foreign := &session.Principal{Issuer: "https://idp.example", Subject: "foreign", GrantType: session.GrantTypeUser}
		svc, broker, created := newWorkspaceEnrollmentHTTPService(t, true)
		h := NewHTTPHandler(svc)
		base := "/v1/sessions/" + string(created.ID) + "/workspace-enrollment/"
		started := decode(t, request(t, h, session.WithPrincipal(t.Context(), owner), base+"connect", ""))
		beforeBegin, beforeObserve, beforeCancel := enrollmentCallCounts(broker.attachment)
		for _, action := range []string{"retry", "cancel"} {
			t.Run(action, func(t *testing.T) {
				resp := request(t, h, session.WithPrincipal(t.Context(), foreign), base+started.EnrollmentID+"/"+action, "")
				if resp.Code != http.StatusNotFound {
					t.Fatalf("foreign %s status = %d, want 404", action, resp.Code)
				}
			})
		}
		if begin, observe, cancel := enrollmentCallCounts(broker.attachment); begin != beforeBegin || observe != beforeObserve || cancel != beforeCancel {
			t.Fatalf("foreign control side effects = begin:%d observe:%d cancel:%d, want %d:%d:%d", begin, observe, cancel, beforeBegin, beforeObserve, beforeCancel)
		}
	})

	t.Run("contending connects begin once then observe the same enrollment", func(t *testing.T) {
		svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
		h := NewHTTPHandler(svc)
		attachment := broker.attachment
		entered := make(chan struct{})
		release := make(chan struct{})
		attachment.mu.Lock()
		attachment.beginEntered = entered
		attachment.releaseBegin = release
		attachment.mu.Unlock()
		path := "/v1/sessions/" + string(created.ID) + "/workspace-enrollment/connect"
		responses := make(chan *httptest.ResponseRecorder, 2)
		for range 2 {
			go func() { responses <- request(t, h, t.Context(), path, "") }()
		}
		<-entered
		close(release)
		first, second := <-responses, <-responses
		if first.Code != http.StatusOK || second.Code != http.StatusOK {
			t.Fatalf("connect statuses = %d, %d", first.Code, second.Code)
		}
		one, two := decode(t, first), decode(t, second)
		if one.EnrollmentID != two.EnrollmentID || one.Status != "pending" || two.Status != "pending" {
			t.Fatalf("contended connect responses = %#v, %#v", one, two)
		}
		if (one.PresentationURL == "") == (two.PresentationURL == "") {
			t.Fatalf("contended connect presentation URLs = %q, %q, want one initial URL and one empty observation URL", one.PresentationURL, two.PresentationURL)
		}
		if begin, observe, cancel := enrollmentCallCounts(attachment); begin != 1 || observe != 1 || cancel != 0 {
			t.Fatalf("contended connect calls = begin:%d observe:%d cancel:%d", begin, observe, cancel)
		}
	})

	t.Run("terminal cancel projects an empty presentation URL", func(t *testing.T) {
		svc, _, created := newWorkspaceEnrollmentHTTPService(t, false)
		h := NewHTTPHandler(svc)
		base := "/v1/sessions/" + string(created.ID) + "/workspace-enrollment/"
		started := decode(t, request(t, h, t.Context(), base+"connect", ""))
		cancelledResp := request(t, h, t.Context(), base+started.EnrollmentID+"/cancel", "")
		if cancelledResp.Code != http.StatusOK {
			t.Fatalf("cancel status = %d: %s", cancelledResp.Code, cancelledResp.Body.String())
		}
		cancelled := decode(t, cancelledResp)
		if cancelled.Status != "cancelled" || cancelled.PresentationURL != "" {
			t.Fatalf("cancel response = %#v", cancelled)
		}
	})
}

func TestWorkspaceEnrollmentCompensationIsBounded(t *testing.T) {
	oldTimeout := engineCloseTimeout
	engineCloseTimeout = 20 * time.Millisecond
	defer func() { engineCloseTimeout = oldTimeout }()

	attachment := &enrollmentAttachment{cancelWait: true}
	started := time.Now()
	cancelWorkspaceEnrollmentDetached(context.Background(), attachment, brokercontract.WorkspaceEnrollmentRef{})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("compensation took %s", elapsed)
	}
}

func TestWorkspaceEnrollmentAcquiresLeaseBeforePersisting(t *testing.T) {
	// ConnectWorkspaceServices/cancelWorkspaceEnrollment must acquire the
	// session mutation lease themselves: a fresh session that never had a run
	// driven through it (the ordinary case — /tools-connect is a pre-prompt
	// gate) never gets Grant()-ed any other way, and under a configured
	// SessionLease the guarded store then rejects every save with
	// ErrSessionLeasedElsewhere. This regressed silently because no other
	// workspace-enrollment test configures a SessionLease at all.
	runtime := testBrokerRuntime(t)
	defer runtime.Close()
	broker := &enrollmentBroker{Service: runtime}
	store := memstore.New()
	lease := memlease.New(wallclock.Clock{}, time.Minute)
	svc, err := NewService(Config{
		Engine:            brokerEngineResult().Engine,
		Store:             store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID:        func() session.SessionID { return "leased-enrollment-session" },
		MCPBroker:    broker,
		SessionLease: lease,
		LeaseOwner:   "test-owner",
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
		t.Fatalf("ConnectWorkspaceServices on a never-run session under a configured lease: %v", err)
	}
	pending, ok := created.PendingWorkspaceEnrollment()
	if loaded, loadErr := store.Load(t.Context(), created.ID); loadErr == nil {
		pending, ok = loaded.PendingWorkspaceEnrollment()
	}
	if !ok {
		t.Fatal("no pending enrollment persisted")
	}
	if _, err := svc.CancelWorkspaceEnrollment(t.Context(), created.ID, pending.ID); err != nil {
		t.Fatalf("CancelWorkspaceEnrollment under a configured lease: %v", err)
	}
}

func TestWorkspaceEnrollmentStateLossClearsPendingGate(t *testing.T) {
	runtime := testBrokerRuntime(t)
	broker := &enrollmentBroker{Service: runtime}
	store := memstore.New()
	svc, err := NewService(Config{
		Engine:            brokerEngineResult().Engine,
		Store:             store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID:     func() session.SessionID { return "lost-enrollment-session" },
		MCPBroker: broker,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	started, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}

	svc.mu.Lock()
	delete(svc.brokerAttachments, created.ID)
	svc.mu.Unlock()
	broker.attachErr = brokercontract.ErrStateUnavailable
	failed, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != brokercontract.WorkspaceEnrollmentFailed || !sameWorkspaceEnrollmentRef(failed.Ref, started.Ref) {
		t.Fatalf("failed projection = %#v, want failed for %#v", failed, started.Ref)
	}
	loaded, err := store.Load(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := loaded.PendingWorkspaceEnrollment(); pending {
		t.Fatal("unavailable broker left workspace enrollment pending")
	}

	broker.attachErr = nil
	if restarted, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err != nil || restarted.Status != brokercontract.WorkspaceEnrollmentPending {
		t.Fatalf("restart after state loss = %#v, %v", restarted, err)
	}
}

func TestWorkspaceEnrollmentPublishesFrozenCatalogueBeforePrompt(t *testing.T) {
	runtime := testBrokerRuntime(t)
	defer runtime.Close()
	broker := &enrollmentBroker{Service: runtime}
	store := memstore.New()
	var catalogues [][]string
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "enrollment-session" }, MCPBroker: broker,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngineWithTools: func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, _ session.PermissionMode, tools []tool.Tool) (SessionEngineResult, error) {
			names := make([]string, len(tools))
			for i, candidate := range tools {
				names[i] = candidate.Spec().Name
			}
			catalogues = append(catalogues, names)
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}

	started, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != brokercontract.WorkspaceEnrollmentPending || started.URL == "" {
		t.Fatalf("started = %#v", started)
	}
	loaded, _ := store.Load(t.Context(), created.ID)
	if pending, ok := loaded.PendingWorkspaceEnrollment(); !ok || pending.ID != started.Ref.ID {
		t.Fatalf("pending = %#v, %v", pending, ok)
	}

	mismatchedRef := started.Ref
	mismatchedRef.ID = "enrollment-other"
	broker.attachment.ref = mismatchedRef
	if _, err := svc.CancelWorkspaceEnrollment(t.Context(), created.ID, started.Ref.ID); err == nil {
		t.Fatal("mismatched enrollment cancellation succeeded")
	}
	broker.attachment.ref = started.Ref
	loaded, _ = store.Load(t.Context(), created.ID)
	if pending, ok := loaded.PendingWorkspaceEnrollment(); !ok || pending.ID != started.Ref.ID {
		t.Fatalf("pending after mismatched cancellation = %#v, %v", pending, ok)
	}

	mismatchedCatalogue, err := brokercontract.NewWorkspaceCatalogue(mismatchedRef, []tool.Tool{enrollmentTool{name: "mcp__calendar__list"}})
	if err != nil {
		t.Fatal(err)
	}
	broker.attachment.result = brokercontract.WorkspaceEnrollmentResult{
		Ref: mismatchedRef, Status: brokercontract.WorkspaceEnrollmentConnected, Catalogue: mismatchedCatalogue,
	}
	if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err == nil {
		t.Fatal("mismatched enrollment observation succeeded")
	}
	loaded, _ = store.Load(t.Context(), created.ID)
	if pending, ok := loaded.PendingWorkspaceEnrollment(); !ok || pending.ID != started.Ref.ID {
		t.Fatalf("pending after mismatched observation = %#v, %v", pending, ok)
	}

	complete, err := brokercontract.NewWorkspaceCatalogue(started.Ref, []tool.Tool{
		enrollmentTool{name: "mcp__calendar__list"}, enrollmentTool{name: "mcp__github__review"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drifted, err := brokercontract.NewWorkspaceCatalogue(started.Ref, []tool.Tool{
		enrollmentTool{name: "mcp__calendar__list"},
	})
	if err != nil {
		t.Fatal(err)
	}
	broker.attachment.catalogue = drifted
	broker.attachment.result = brokercontract.WorkspaceEnrollmentResult{Ref: started.Ref, Status: brokercontract.WorkspaceEnrollmentConnected, Catalogue: complete}
	connected, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if connected.Status != brokercontract.WorkspaceEnrollmentConnected || len(catalogues) != 2 {
		t.Fatalf("connected = %#v, catalogues = %v", connected, catalogues)
	}
	if got := catalogues[1]; len(got) != 2 || got[1] != "mcp__github__review" {
		t.Fatalf("rebuilt catalogue = %v", got)
	}
	loaded, _ = store.Load(t.Context(), created.ID)
	if _, ok := loaded.PendingWorkspaceEnrollment(); ok {
		t.Fatal("completed enrollment remained pending")
	}
	authority, ok := loaded.BoundAuthority()
	if !ok || len(authority.CapabilitySet.Tools) != 2 || authority.CapabilitySet.Tools[1] != "mcp__github__review" {
		t.Fatalf("authority = %#v, %v", authority, ok)
	}
}
