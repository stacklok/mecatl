package server_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func adoptionPrincipal(subject string) *session.Principal {
	return &session.Principal{Issuer: "https://idp.example", Subject: subject, GrantType: session.GrantTypeUser}
}

func adoptionContext(subject string) context.Context {
	return session.WithPrincipal(context.Background(), adoptionPrincipal(subject))
}

func adoptionGRPCClient(t *testing.T, svc *server.Service, principal *session.Principal) (mecatlv1.HarnessServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(session.WithPrincipal(ctx, principal), req)
	}))
	mecatlv1.RegisterHarnessServiceServer(gs, server.NewHarnessServer(svc))
	go func() { _ = gs.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///adoption", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	return mecatlv1.NewHarnessServiceClient(conn), func() {
		_ = conn.Close()
		gs.Stop()
		_ = listener.Close()
	}
}

func adoptionBindings() server.AdoptionBindings {
	return server.AdoptionBindings{
		Workspace:      "/adopted",
		EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/adopted"},
		ProviderID:     "provider-b",
		ModelID:        "model-b",
		Profile:        server.ProfileDefault,
	}
}

func adoptionService(t *testing.T) (*server.Service, *memstore.Store) {
	return adoptionServiceWithLease(t, nil)
}

func adoptionServiceWithLease(t *testing.T, lease port.SessionLease) (*server.Service, *memstore.Store) {
	t.Helper()
	store := memstore.New()
	return adoptionServiceWithStore(t, store, lease), store
}

// adoptionServiceWithStore builds a Service over a CALLER-SUPPLIED store, so two
// independent Services can share one backend — the cross-service create-collision
// scenario. adoptionServiceWithLease is the single-service convenience over it.
func adoptionServiceWithStore(t *testing.T, store port.SessionStore, lease port.SessionLease) *server.Service {
	t.Helper()
	factory := func(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, mode session.PermissionMode) (server.SessionEngineResult, error) {
		if sel.ProviderID == "missing" || sel.ModelID == "missing" {
			return server.SessionEngineResult{}, server.ErrInvalidArgument
		}
		eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: sel.ModelID})
		return server.SessionEngineResult{Engine: eng, ProviderID: sel.ProviderID, ModelID: sel.ModelID, BuiltForMode: mode, Close: func() error { return nil }}, nil
	}
	svc, err := server.NewService(server.Config{
		Engine:               agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "default"}),
		Store:                store,
		Workspaces:           func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		SessionEngine:        factory,
		DefaultResolvedModel: server.ResolvedModel{ProviderID: "provider-a", ModelID: "model-a"},
		OwnershipEnforced:    true,
		SessionLease:         lease,
		LeaseOwner:           "adoption-server",
		LeaseTTL:             time.Minute,
		LeaseRenewInterval:   20 * time.Second,
		Now:                  func() time.Time { return time.Unix(1700000000, 0) },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func saveLegacy(t *testing.T, store *memstore.Store, id session.SessionID, owner *session.Principal, state session.State) *session.Session {
	t.Helper()
	sess := session.New(id, session.ModeDefault, "/legacy", session.Limits{MaxTurns: 10}, time.Unix(1, 0))
	if err := sess.RestoreSessionMetadata(session.SessionKindUnknown, session.SessionRelationship{}); err != nil {
		t.Fatal(err)
	}
	if err := sess.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	sess.ProviderID, sess.ModelID = "provider-a", "model-a"
	if err := sess.RecordUserPrompt("legacy request", nil); err != nil {
		t.Fatal(err)
	}
	if state != session.StateIdle {
		if err := sess.BeginTurn(); err != nil {
			t.Fatal(err)
		}
		if state != session.StateRunning {
			if err := sess.RecordAssistant(session.Message{Role: session.RoleAssistant, Text: "legacy answer", Reasoning: "private", ProviderPhase: "commentary"}); err != nil {
				t.Fatal(err)
			}
			switch state {
			case session.StateCompleted:
				if err := sess.Complete(); err != nil {
					t.Fatal(err)
				}
			case session.StateCancelled:
				if err := sess.Cancel(); err != nil {
					t.Fatal(err)
				}
			case session.StateFailed:
				if err := sess.Fail(); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestSessionStorageContinuity_Scenario7_AdoptionEligibilityMatrix(t *testing.T) {
	svc, store := adoptionService(t)
	ctx := adoptionContext("alice")
	for _, tc := range []struct {
		name   string
		id     session.SessionID
		state  session.State
		want   bool
		reason server.AdoptionReason
	}{
		{name: "idle", id: "legacy-idle", state: session.StateIdle, want: true},
		{name: "completed", id: "legacy-completed", state: session.StateCompleted, want: true},
		{name: "running", id: "legacy-running", state: session.StateRunning, reason: server.AdoptionReasonActive},
		{name: "protected child prefix", id: "subagent-legacy", state: session.StateCompleted, reason: server.AdoptionReasonProtectedProvenance},
		{name: "protected schedule prefix", id: "sched--legacy", state: session.StateCompleted, reason: server.AdoptionReasonProtectedProvenance},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saveLegacy(t, store, tc.id, adoptionPrincipal("alice"), tc.state)
			got, err := svc.PreflightSessionAdoption(ctx, tc.id, adoptionBindings())
			if err != nil {
				t.Fatalf("preflight: %v", err)
			}
			if got.Eligible != tc.want || got.Reason != tc.reason {
				t.Fatalf("preflight = eligible:%v reason:%q, want %v/%q", got.Eligible, got.Reason, tc.want, tc.reason)
			}
		})
	}

	leaseBackend := memlease.New(wallclock.Clock{}, time.Minute)
	leasedService, leasedStore := adoptionServiceWithLease(t, leaseBackend)
	leasedSource := saveLegacy(t, leasedStore, "legacy-leased", adoptionPrincipal("alice"), session.StateCompleted)
	if _, err := leaseBackend.Acquire(ctx, leasedSource.ID, "other-replica"); err != nil {
		t.Fatal(err)
	}
	leasedPreflight, err := leasedService.PreflightSessionAdoption(ctx, leasedSource.ID, adoptionBindings())
	if err != nil || leasedPreflight.Eligible || leasedPreflight.Reason != server.AdoptionReasonLeased {
		t.Fatalf("leased preflight = %+v, %v", leasedPreflight, err)
	}

	invalid := session.New("legacy-invalid", session.ModeDefault, "/legacy", session.Limits{}, time.Unix(1, 0))
	if err := invalid.RestoreSessionMetadata(session.SessionKindUnknown, session.SessionRelationship{}); err != nil {
		t.Fatal(err)
	}
	if err := invalid.RestoreLabels(adoptionPrincipal("alice"), session.Authority{}); err != nil {
		t.Fatal(err)
	}
	invalid.Conversation.Append(session.Message{Role: session.RoleAssistant, ToolCalls: []session.ToolCall{{ID: "dangling", Name: "Read"}}})
	if err := store.Save(ctx, invalid); err != nil {
		t.Fatal(err)
	}
	invalidPreflight, err := svc.PreflightSessionAdoption(ctx, invalid.ID, adoptionBindings())
	if err != nil || invalidPreflight.Eligible || invalidPreflight.Reason != server.AdoptionReasonInvalidTranscript {
		t.Fatalf("invalid transcript preflight = %+v, %v", invalidPreflight, err)
	}

	awaiting := session.New("legacy-awaiting", session.ModeDefault, "/legacy", session.Limits{}, time.Unix(1, 0))
	if err := awaiting.RestoreSessionMetadata(session.SessionKindUnknown, session.SessionRelationship{}); err != nil {
		t.Fatal(err)
	}
	if err := awaiting.RestoreLabels(adoptionPrincipal("alice"), session.Authority{}); err != nil {
		t.Fatal(err)
	}
	if err := awaiting.RecordUserPrompt("wait", nil); err != nil {
		t.Fatal(err)
	}
	if err := awaiting.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("call", "Read", nil)
	if err := awaiting.RecordAssistant(session.Message{Role: session.RoleAssistant, ToolCalls: []session.ToolCall{call}}); err != nil {
		t.Fatal(err)
	}
	if err := awaiting.PauseForApproval(session.PendingAsk{AskID: "ask", Tool: "Read", Call: call.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, awaiting); err != nil {
		t.Fatal(err)
	}
	awaitingPreflight, err := svc.PreflightSessionAdoption(ctx, awaiting.ID, adoptionBindings())
	if err != nil || awaitingPreflight.Eligible || awaitingPreflight.Reason != server.AdoptionReasonAwaiting {
		t.Fatalf("awaiting preflight = %+v, %v", awaitingPreflight, err)
	}

	main := session.New("explicit-main", session.ModeDefault, "/legacy", session.Limits{}, time.Unix(1, 0))
	if err := main.RestoreLabels(adoptionPrincipal("alice"), session.Authority{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, main); err != nil {
		t.Fatal(err)
	}
	got, err := svc.PreflightSessionAdoption(ctx, main.ID, adoptionBindings())
	if err != nil || got.Eligible || got.Reason != server.AdoptionReasonNotLegacy {
		t.Fatalf("main preflight = %+v, %v", got, err)
	}
}

func TestCallerSeparation_ForeignAdoptionPreflightDoesNotContendOnOwnerCoordination(t *testing.T) {
	base := memstore.New()
	source := saveLegacy(t, base, "legacy", adoptionPrincipal("alice"), session.StateCompleted)
	store := &managementBarrierStore{Store: base, authoritativeLoad: make(chan struct{}), releaseLoad: make(chan struct{})}
	t.Cleanup(store.release)
	lease := &fakeLease{}
	svc := adoptionServiceWithStore(t, store, lease)
	aliceCtx, cancel := context.WithCancel(adoptionContext("alice"))
	t.Cleanup(cancel)

	type result struct {
		preflight server.AdoptionPreflight
		err       error
	}
	ownerResult := make(chan result, 1)
	go func() {
		preflight, err := svc.PreflightSessionAdoption(aliceCtx, source.ID, adoptionBindings())
		ownerResult <- result{preflight: preflight, err: err}
	}()
	select {
	case <-store.authoritativeLoad:
	case <-time.After(time.Second):
		t.Fatal("owner adoption preflight did not reach the authoritative under-lock load")
	}

	foreignResult := make(chan error, 1)
	go func() {
		_, err := svc.PreflightSessionAdoption(adoptionContext("bob"), source.ID, adoptionBindings())
		foreignResult <- err
	}()
	select {
	case err := <-foreignResult:
		if !errors.Is(err, server.ErrNotFound) {
			t.Fatalf("foreign adoption preflight = %v, want ErrNotFound", err)
		}
	case <-time.After(time.Second):
		t.Fatal("foreign adoption preflight contended on the owner's coordination lock")
	}
	if _, err := svc.PreflightSessionAdoption(adoptionContext("bob"), "missing", adoptionBindings()); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("missing adoption preflight = %v, want ErrNotFound", err)
	}
	if saves, deletes := store.counts(); saves != 0 || deletes != 0 {
		t.Fatalf("foreign adoption preflight effects: saves=%d deletes=%d, want zero", saves, deletes)
	}
	if lease.acquires != 0 {
		t.Fatalf("foreign adoption preflight lease acquires = %d, want 0", lease.acquires)
	}

	store.release()
	owner := <-ownerResult
	if owner.err != nil || !owner.preflight.Eligible {
		t.Fatalf("owner adoption preflight = %+v, %v", owner.preflight, owner.err)
	}
}

func TestCallerSeparation_ForeignAdoptDoesNotContendOnOwnerCoordination(t *testing.T) {
	base := memstore.New()
	source := saveLegacy(t, base, "legacy", adoptionPrincipal("alice"), session.StateCompleted)
	store := &managementBarrierStore{Store: base, authoritativeLoad: make(chan struct{}), releaseLoad: make(chan struct{})}
	t.Cleanup(store.release)
	lease := &fakeLease{}
	svc := adoptionServiceWithStore(t, store, lease)
	aliceCtx, cancel := context.WithCancel(adoptionContext("alice"))
	t.Cleanup(cancel)

	type result struct {
		sess *session.Session
		err  error
	}
	ownerResult := make(chan result, 1)
	go func() {
		adopted, err := svc.AdoptSession(aliceCtx, source.ID, "owner-request", adoptionBindings())
		ownerResult <- result{sess: adopted, err: err}
	}()
	select {
	case <-store.authoritativeLoad:
	case <-time.After(time.Second):
		t.Fatal("owner adoption did not reach the authoritative under-lock load")
	}

	foreignResult := make(chan error, 1)
	go func() {
		_, err := svc.AdoptSession(adoptionContext("bob"), source.ID, "foreign-request", adoptionBindings())
		foreignResult <- err
	}()
	select {
	case err := <-foreignResult:
		if !errors.Is(err, server.ErrNotFound) {
			t.Fatalf("foreign AdoptSession = %v, want ErrNotFound", err)
		}
	case <-time.After(time.Second):
		t.Fatal("foreign adoption contended on the owner's coordination lock")
	}
	if _, err := svc.AdoptSession(adoptionContext("bob"), "missing", "foreign-request", adoptionBindings()); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("missing AdoptSession = %v, want ErrNotFound", err)
	}
	if saves, deletes := store.counts(); saves != 0 || deletes != 0 {
		t.Fatalf("foreign adoption effects: saves=%d deletes=%d, want zero", saves, deletes)
	}
	if lease.acquires != 0 {
		t.Fatalf("foreign adoption lease acquires = %d, want 0", lease.acquires)
	}

	store.release()
	owner := <-ownerResult
	if owner.err != nil || owner.sess == nil {
		t.Fatalf("owner AdoptSession = %+v, %v", owner.sess, owner.err)
	}
}

func TestCallerSeparation_AdoptionReloadReauthorizesAfterPreflight(t *testing.T) {
	tests := []struct {
		name string
		run  func(*server.Service, context.Context, session.SessionID) error
	}{
		{name: "preflight", run: func(svc *server.Service, ctx context.Context, id session.SessionID) error {
			_, err := svc.PreflightSessionAdoption(ctx, id, adoptionBindings())
			return err
		}},
		{name: "adopt", run: func(svc *server.Service, ctx context.Context, id session.SessionID) error {
			_, err := svc.AdoptSession(ctx, id, "owner-request", adoptionBindings())
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := memstore.New()
			source := saveLegacy(t, base, "legacy", adoptionPrincipal("alice"), session.StateCompleted)
			replacement := session.New(source.ID, session.ModeDefault, "/legacy", session.Limits{}, time.Unix(2, 0))
			if err := replacement.RestoreSessionMetadata(session.SessionKindUnknown, session.SessionRelationship{}); err != nil {
				t.Fatalf("RestoreSessionMetadata replacement: %v", err)
			}
			if err := replacement.RestoreLabels(adoptionPrincipal("bob"), session.Authority{}); err != nil {
				t.Fatalf("RestoreLabels replacement: %v", err)
			}
			store := &managementBarrierStore{Store: base, authoritativeLoad: make(chan struct{}), releaseLoad: make(chan struct{})}
			store.mutation = func(ctx context.Context) error { return base.Save(ctx, replacement) }
			t.Cleanup(store.release)
			lease := &fakeLease{}
			svc := adoptionServiceWithStore(t, store, lease)
			ctx, cancel := context.WithCancel(adoptionContext("alice"))
			t.Cleanup(cancel)

			result := make(chan error, 1)
			go func() { result <- tt.run(svc, ctx, source.ID) }()
			select {
			case <-store.authoritativeLoad:
			case <-time.After(time.Second):
				t.Fatalf("%s did not reach the authoritative under-lock load", tt.name)
			}
			store.release()
			if err := <-result; !errors.Is(err, server.ErrNotFound) {
				t.Fatalf("%s after owner replacement = %v, want ErrNotFound", tt.name, err)
			}
			if saves, deletes := store.counts(); saves != 0 || deletes != 0 {
				t.Fatalf("%s after owner replacement effects: saves=%d deletes=%d, want zero", tt.name, saves, deletes)
			}
			if lease.acquires != 0 {
				t.Fatalf("%s after owner replacement lease acquires = %d, want 0", tt.name, lease.acquires)
			}
		})
	}
}

func TestCallerSeparation_AdoptionReauthorizesAfterLeaseAcquisition(t *testing.T) {
	tests := []struct {
		name string
		run  func(*server.Service, context.Context, session.SessionID) error
	}{
		{name: "preflight", run: func(svc *server.Service, ctx context.Context, id session.SessionID) error {
			_, err := svc.PreflightSessionAdoption(ctx, id, adoptionBindings())
			return err
		}},
		{name: "adopt", run: func(svc *server.Service, ctx context.Context, id session.SessionID) error {
			_, err := svc.AdoptSession(ctx, id, "owner-request", adoptionBindings())
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := memstore.New()
			source := saveLegacy(t, base, "legacy", adoptionPrincipal("alice"), session.StateCompleted)
			replacement := session.New(source.ID, session.ModeDefault, "/legacy", session.Limits{}, time.Unix(2, 0))
			if err := replacement.RestoreSessionMetadata(session.SessionKindUnknown, session.SessionRelationship{}); err != nil {
				t.Fatalf("RestoreSessionMetadata replacement: %v", err)
			}
			if err := replacement.RestoreLabels(adoptionPrincipal("bob"), session.Authority{}); err != nil {
				t.Fatalf("RestoreLabels replacement: %v", err)
			}
			store := &managementEffectStore{Store: base}
			lease := &mutationLease{mutation: func(ctx context.Context) error { return base.Save(ctx, replacement) }}
			svc := adoptionServiceWithStore(t, store, lease)
			err := tt.run(svc, adoptionContext("alice"), source.ID)
			if !errors.Is(err, server.ErrNotFound) {
				t.Fatalf("%s after lease-time owner replacement = %v, want ErrNotFound", tt.name, err)
			}
			if saves, deletes := store.counts(); saves != 0 || deletes != 0 {
				t.Fatalf("%s after lease-time owner replacement effects: saves=%d deletes=%d, want zero", tt.name, saves, deletes)
			}
			if acquires, releases := lease.counts(); acquires != 1 || releases != 1 {
				t.Fatalf("%s lease counts = %d/%d, want 1/1", tt.name, acquires, releases)
			}
		})
	}
}

func TestSessionStorageContinuity_Scenario7_OwnershipComesFromCallerContext(t *testing.T) {
	svc, store := adoptionService(t)
	saveLegacy(t, store, "owned", adoptionPrincipal("alice"), session.StateCompleted)
	_, absentErr := svc.PreflightSessionAdoption(adoptionContext("bob"), "absent", adoptionBindings())
	_, foreignErr := svc.PreflightSessionAdoption(adoptionContext("bob"), "owned", adoptionBindings())
	if !errors.Is(absentErr, server.ErrNotFound) || !errors.Is(foreignErr, server.ErrNotFound) || absentErr.Error() != foreignErr.Error() {
		t.Fatalf("absent/foreign errors differ: %v / %v", absentErr, foreignErr)
	}
	if _, err := svc.PreflightSessionAdoption(context.Background(), "owned", adoptionBindings()); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("unauthenticated preflight = %v, want NotFound", err)
	}
}

func TestSessionStorageContinuity_Scenario7_ExplicitBindingsAndProviderNeutrality(t *testing.T) {
	svc, store := adoptionService(t)
	ctx := adoptionContext("alice")
	saveLegacy(t, store, "legacy", adoptionPrincipal("alice"), session.StateCompleted)

	missing := adoptionBindings()
	missing.ProviderID = ""
	got, err := svc.PreflightSessionAdoption(ctx, "legacy", missing)
	if err != nil || got.Eligible || got.Reason != server.AdoptionReasonBindingUnresolved {
		t.Fatalf("missing binding preflight = %+v, %v", got, err)
	}
	unresolved := adoptionBindings()
	unresolved.ProviderID = "missing"
	got, err = svc.PreflightSessionAdoption(ctx, "legacy", unresolved)
	if err != nil || got.Eligible || got.Reason != server.AdoptionReasonBindingUnresolved {
		t.Fatalf("unresolved binding preflight = %+v, %v", got, err)
	}

	target, err := svc.AdoptSession(ctx, "legacy", "request-1", adoptionBindings())
	if err != nil {
		t.Fatalf("AdoptSession: %v", err)
	}
	if target.Workspace != "/adopted" || target.EnvironmentRef != adoptionBindings().EnvironmentRef || target.ProviderID != "provider-b" || target.ModelID != "model-b" {
		t.Fatalf("explicit bindings not persisted: %+v", target)
	}
	for _, msg := range target.Conversation.Messages {
		if msg.Reasoning != "" || msg.ProviderPhase != "" {
			t.Fatalf("cross-provider private state survived: %+v", msg)
		}
	}

	wireSource := saveLegacy(t, store, "wire-legacy", adoptionPrincipal("alice"), session.StateCompleted)
	client, closeClient := adoptionGRPCClient(t, svc, adoptionPrincipal("alice"))
	defer closeClient()
	wireBindings := &mecatlv1.AdoptionBindings{Workspace: "/adopted", EnvironmentKind: "local", EnvironmentId: "/adopted", ProviderId: "provider-b", ModelId: "model-b"}
	preflight, err := client.PreflightSessionAdoption(context.Background(), &mecatlv1.PreflightSessionAdoptionRequest{SourceSessionId: string(wireSource.ID), Bindings: wireBindings})
	if err != nil || !preflight.GetEligible() || preflight.GetBindings().GetProviderId() != "provider-b" {
		t.Fatalf("gRPC preflight = %+v, %v", preflight, err)
	}
	adopted, err := client.AdoptSession(context.Background(), &mecatlv1.AdoptSessionRequest{SourceSessionId: string(wireSource.ID), IdempotencyKey: "grpc-key", Bindings: wireBindings})
	if err != nil || adopted.GetSessionId() == "" || adopted.GetSourceSessionId() != string(wireSource.ID) {
		t.Fatalf("gRPC adoption = %+v, %v", adopted, err)
	}

	httpSource := saveLegacy(t, store, "http-legacy", adoptionPrincipal("alice"), session.StateCompleted)
	handler := server.NewHTTPHandler(svc)
	body := strings.NewReader(`{"workspace":"/adopted","environment_kind":"local","environment_id":"/adopted","provider_id":"provider-b","model_id":"model-b"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+string(httpSource.ID)+"/adoption:preflight", body).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"eligible":true`) {
		t.Fatalf("HTTP preflight = %d %s", rec.Code, rec.Body.String())
	}
	adoptBody := strings.NewReader(`{"workspace":"/adopted","environment_kind":"local","environment_id":"/adopted","provider_id":"provider-b","model_id":"model-b","idempotency_key":"http-key"}`)
	adoptReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+string(httpSource.ID)+"/adopt", adoptBody).WithContext(ctx)
	adoptRec := httptest.NewRecorder()
	handler.ServeHTTP(adoptRec, adoptReq)
	if adoptRec.Code != http.StatusCreated || !strings.Contains(adoptRec.Body.String(), `"source_session_id":"http-legacy"`) {
		t.Fatalf("HTTP adoption = %d %s", adoptRec.Code, adoptRec.Body.String())
	}
}

func TestSessionStorageContinuity_Scenario7_NewMainCopyPreservesSource(t *testing.T) {
	svc, store := adoptionService(t)
	ctx := adoptionContext("alice")
	source := saveLegacy(t, store, "legacy", adoptionPrincipal("alice"), session.StateCompleted)
	target, err := svc.AdoptSession(ctx, source.ID, "copy-once", adoptionBindings())
	if err != nil {
		t.Fatal(err)
	}
	adoption := target.Adoption
	if target.ID == source.ID || target.Kind != session.SessionKindMain || target.State != session.StateIdle || adoption == nil || adoption.AdoptionSourceID != source.ID {
		t.Fatalf("target metadata = %+v", target)
	}
	after, err := store.Load(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != source.State || len(after.Conversation.Messages) != len(source.Conversation.Messages) || after.Kind != session.SessionKindUnknown {
		t.Fatalf("source changed: before=%+v after=%+v", source, after)
	}
	if len(target.Conversation.Messages) != len(source.Conversation.Messages) {
		t.Fatalf("target transcript len = %d, want %d", len(target.Conversation.Messages), len(source.Conversation.Messages))
	}

	cancelSource := saveLegacy(t, store, "legacy-cancel", adoptionPrincipal("alice"), session.StateCompleted)
	beforeRows, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.AdoptSession(cancelled, cancelSource.ID, "cancelled", adoptionBindings()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled adoption = %v, want context.Canceled", err)
	}
	afterRows, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterRows) != len(beforeRows) {
		t.Fatalf("cancelled adoption published a partial target: rows %d -> %d", len(beforeRows), len(afterRows))
	}
}

func TestSessionStorageContinuity_Scenario7_AdoptionIdempotency(t *testing.T) {
	svc, store := adoptionService(t)
	ctx := adoptionContext("alice")
	saveLegacy(t, store, "legacy", adoptionPrincipal("alice"), session.StateCompleted)
	first, err := svc.AdoptSession(ctx, "legacy", "retry-key", adoptionBindings())
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.AdoptSession(ctx, "legacy", "retry-key", adoptionBindings())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || len(second.Conversation.Messages) != len(first.Conversation.Messages) {
		t.Fatalf("retry target = %q/%d, want %q/%d", second.ID, len(second.Conversation.Messages), first.ID, len(first.Conversation.Messages))
	}
}

func TestSessionStorageContinuity_Scenario7_AdoptionCrossServiceRetryIsAtomic(t *testing.T) {
	inner := memstore.New()
	store := &barrierCreateStore{Store: inner, release: make(chan struct{})}
	firstService := adoptionServiceWithStore(t, store, nil)
	secondService := adoptionServiceWithStore(t, store, nil)
	ctx := adoptionContext("alice")
	saveLegacy(t, inner, "legacy-cross-service", adoptionPrincipal("alice"), session.StateCompleted)

	var wg sync.WaitGroup
	results := make([]*session.Session, 2)
	errs := make([]error, 2)
	services := []*server.Service{firstService, secondService}
	wg.Add(2)
	for i := range services {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = services[i].AdoptSession(ctx, "legacy-cross-service", "retry-key", adoptionBindings())
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil || results[i] == nil {
			t.Fatalf("adoption %d = (%+v, %v), want idempotent success", i, results[i], errs[i])
		}
	}
	if results[0].ID != results[1].ID {
		t.Fatalf("adoption targets differ: %q != %q", results[0].ID, results[1].ID)
	}
}

func TestSessionStorageContinuity_Scenario7_CrossCallerReplayDenied(t *testing.T) {
	svc, store := adoptionService(t)
	saveLegacy(t, store, "legacy", adoptionPrincipal("alice"), session.StateCompleted)
	if _, err := svc.AdoptSession(adoptionContext("alice"), "legacy", "shared-key", adoptionBindings()); err != nil {
		t.Fatal(err)
	}
	_, absentErr := svc.AdoptSession(adoptionContext("bob"), "absent", "shared-key", adoptionBindings())
	_, replayErr := svc.AdoptSession(adoptionContext("bob"), "legacy", "shared-key", adoptionBindings())
	if !errors.Is(replayErr, server.ErrNotFound) || replayErr.Error() != absentErr.Error() {
		t.Fatalf("cross-caller replay leaked existence: absent=%v replay=%v", absentErr, replayErr)
	}
}

func TestSessionStorageContinuity_Scenario7_NoEscalationOrOracle(t *testing.T) {
	svc, store := adoptionService(t)
	ctx := adoptionContext("alice")
	source := saveLegacy(t, store, "parallel-legacy", adoptionPrincipal("alice"), session.StateCompleted)
	pre, err := svc.PreflightSessionAdoption(ctx, source.ID, adoptionBindings())
	if err != nil || pre.Eligible || pre.Reason != server.AdoptionReasonProtectedProvenance {
		t.Fatalf("protected preflight = %+v, %v", pre, err)
	}
	after, err := store.Load(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Kind != session.SessionKindUnknown || after.State != session.StateCompleted {
		t.Fatalf("preflight escalated source: %+v", after)
	}
	if _, err := svc.AdoptSession(ctx, source.ID, "blocked", adoptionBindings()); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("protected adoption = %v, want failed precondition", err)
	}
	if _, err := store.Load(ctx, session.SessionID("adopt-does-not-exist")); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("blocked adoption published a target: %v", err)
	}
}
