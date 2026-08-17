package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func newSessionManagementService(t *testing.T, ownership bool, lease port.SessionLease, llm port.LLMProvider) (*server.Service, *memstore.Store) {
	t.Helper()
	if llm == nil {
		llm = mockllm.New()
	}
	store := memstore.New()
	svc, err := server.NewService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test"}),
		Store:  store, Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now: func() time.Time { return time.Unix(1, 0) }, OwnershipEnforced: ownership,
		SessionLease: lease, LeaseOwner: "manager", LeaseTTL: time.Hour, LeaseRenewInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc, store
}

type pagingOnlySessionStore struct{ inner *memstore.Store }

func (s *pagingOnlySessionStore) Save(ctx context.Context, sess *session.Session) error {
	return s.inner.Save(ctx, sess)
}

func (s *pagingOnlySessionStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return s.inner.Load(ctx, id)
}

func (s *pagingOnlySessionStore) PageSessionMetadata(ctx context.Context, request port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
	return s.inner.PageSessionMetadata(ctx, request)
}

type deleteUnsupportedSessionStore struct{ *memstore.Store }

func (*deleteUnsupportedSessionStore) SupportsSessionDelete() bool { return false }

var _ port.SessionDeleteSupport = (*deleteUnsupportedSessionStore)(nil)

type failingDeleteSessionStore struct {
	*memstore.Store
	err error
}

func (s *failingDeleteSessionStore) Delete(context.Context, session.SessionID) error { return s.err }

func newSessionManagementServiceWithStore(t *testing.T, store port.SessionStore, lease port.SessionLease) *server.Service {
	t.Helper()
	svc, err := server.NewService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test"}),
		Store:  store, Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now: time.Now, SessionLease: lease, LeaseOwner: "manager", LeaseTTL: time.Hour, LeaseRenewInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func TestSessionManagementRenameDeleteAndOwnership(t *testing.T) {
	svc, store := newSessionManagementService(t, true, nil, nil)
	alice := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser})
	bob := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser})
	sess, err := svc.CreateSession(alice, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sess.SetTitle("first prompt")
	if err := store.Save(alice, sess); err != nil {
		t.Fatalf("Save title: %v", err)
	}

	if _, err := svc.RenameSession(bob, sess.ID, "stolen"); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("foreign RenameSession = %v, want ErrNotFound", err)
	}
	if err := svc.DeleteSession(bob, sess.ID); err != nil {
		t.Fatalf("foreign DeleteSession = %v, want idempotent success", err)
	}
	renamed, err := svc.RenameSession(alice, sess.ID, "  Operator title  ")
	if err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if renamed.Title != "Operator title" || renamed.TitleProvenance != session.TitleProvenanceOperator {
		t.Fatalf("renamed title/provenance = %q/%q", renamed.Title, renamed.TitleProvenance)
	}
	persisted, err := store.Load(alice, sess.ID)
	if err != nil || persisted.TitleProvenance != session.TitleProvenanceOperator {
		t.Fatalf("persisted rename = %+v, %v", persisted, err)
	}
	if err := svc.DeleteSession(alice, sess.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := svc.GetSession(alice, sess.ID); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("GetSession after delete = %v, want ErrNotFound", err)
	}
	if err := svc.DeleteSession(alice, sess.ID); err != nil {
		t.Fatalf("second DeleteSession = %v, want idempotent success", err)
	}
}

func TestSessionManagementRejectsInvalidUTF8IDBeforeRenameResponse(t *testing.T) {
	inner := memstore.New()
	corrupt := session.New("bad\xffid", session.ModeDefault, "/workspace", session.Limits{}, time.Unix(1, 0))
	svc := newSessionManagementServiceWithStore(t, &corruptSessionIDStore{SessionStore: inner, sess: corrupt}, nil)

	if _, err := svc.RenameSession(context.Background(), corrupt.ID, "renamed"); !errors.Is(err, server.ErrInternal) {
		t.Fatalf("RenameSession invalid UTF-8 id error = %v, want ErrInternal", err)
	}
}

func TestSessionManagementRejectsKindAwaitingAndLive(t *testing.T) {
	svc, store := newSessionManagementService(t, false, nil, blockingProvider{})
	ctx := context.Background()

	child := session.New("subagent-parent-call", session.ModeDefault, "/ws", session.Limits{}, time.Unix(1, 0))
	if err := child.RestoreSessionMetadata(session.SessionKindSubagent, session.SessionRelationship{ParentSessionID: "parent", CallID: "call"}); err != nil {
		t.Fatalf("RestoreSessionMetadata: %v", err)
	}
	if err := store.Save(ctx, child); err != nil {
		t.Fatalf("Save child: %v", err)
	}
	if _, err := svc.ForkSession(ctx, child.ID, "", ""); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("ForkSession(child) = %v, want ErrFailedPrecondition", err)
	}
	if _, err := svc.RenameSession(ctx, child.ID, "no"); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("RenameSession(child) = %v, want ErrFailedPrecondition", err)
	}
	if err := svc.DeleteSession(ctx, child.ID); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("DeleteSession(child) = %v, want ErrFailedPrecondition", err)
	}

	awaiting, err := svc.CreateSession(ctx, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("Create awaiting: %v", err)
	}
	if err := awaiting.RecordUserPrompt("go", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := awaiting.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	call := session.NewToolCall("c1", "Write", json.RawMessage(`{"path":"f"}`))
	if err := awaiting.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := awaiting.PauseForApproval(session.PendingAsk{AskID: "a1", Tool: "Write", Call: call.ID}); err != nil {
		t.Fatalf("PauseForApproval: %v", err)
	}
	if err := store.Save(ctx, awaiting); err != nil {
		t.Fatalf("Save awaiting: %v", err)
	}
	if _, err := svc.ForkSession(ctx, awaiting.ID, "", ""); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("ForkSession(awaiting) = %v, want ErrFailedPrecondition", err)
	}
	if _, err := svc.RenameSession(ctx, awaiting.ID, "no"); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("RenameSession(awaiting) = %v, want ErrFailedPrecondition", err)
	}
	if err := svc.DeleteSession(ctx, awaiting.ID); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("DeleteSession(awaiting) = %v, want ErrFailedPrecondition", err)
	}

	live, err := svc.CreateSession(ctx, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("Create live: %v", err)
	}
	run, err := svc.StartRun(ctx, live.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := svc.ForkSession(ctx, live.ID, "", ""); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("ForkSession(live) = %v, want ErrFailedPrecondition", err)
	}
	if _, err := svc.RenameSession(ctx, live.ID, "no"); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("RenameSession(live) = %v, want ErrFailedPrecondition", err)
	}
	if err := svc.DeleteSession(ctx, live.ID); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("DeleteSession(live) = %v, want ErrFailedPrecondition", err)
	}
	run.Cancel()
	for range run.Events() {
	}
	svc.FinishRun(live.ID, run)
}

func TestSessionManagementRejectsLeaseHeldElsewhere(t *testing.T) {
	lease := &fakeLease{acquireErr: port.ErrLeaseHeld}
	svc, _ := newSessionManagementService(t, false, lease, nil)
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := svc.RenameSession(context.Background(), sess.ID, "no"); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("RenameSession lease = %v, want ErrSessionLeasedElsewhere", err)
	}
	if err := svc.DeleteSession(context.Background(), sess.ID); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("DeleteSession lease = %v, want ErrSessionLeasedElsewhere", err)
	}
	if err := svc.DeleteSessionForRetention(context.Background(), sess.ID); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("DeleteSessionForRetention lease = %v, want ErrSessionLeasedElsewhere", err)
	}
}

func TestRetentionDeleteRevalidatesCandidateAfterPlanning(t *testing.T) {
	svc, store := newSessionManagementService(t, false, nil, nil)
	ctx := context.Background()

	running, err := svc.CreateSession(ctx, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := running.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := store.Save(ctx, running); err != nil {
		t.Fatalf("Save running: %v", err)
	}
	if err := svc.DeleteSessionForRetention(ctx, running.ID); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("DeleteSessionForRetention(running) = %v, want ErrFailedPrecondition", err)
	}
	if _, err := store.Load(ctx, running.ID); err != nil {
		t.Fatalf("running candidate was deleted: %v", err)
	}

	unknown := session.New("legacy-unknown", session.ModeDefault, "/ws", session.Limits{}, time.Now())
	if err := unknown.RestoreSessionMetadata(session.SessionKindUnknown, session.SessionRelationship{}); err != nil {
		t.Fatalf("RestoreSessionMetadata: %v", err)
	}
	if err := store.Save(ctx, unknown); err != nil {
		t.Fatalf("Save unknown: %v", err)
	}
	if err := svc.DeleteSessionForRetention(ctx, unknown.ID); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("DeleteSessionForRetention(unknown) = %v, want ErrFailedPrecondition", err)
	}
	if _, err := store.Load(ctx, unknown.ID); err != nil {
		t.Fatalf("unknown candidate was deleted: %v", err)
	}
}

func TestSessionManagementGRPCAndHTTPParity(t *testing.T) {
	svc, _ := newSessionManagementService(t, false, nil, nil)
	ctx := context.Background()
	grpcServer := server.NewHarnessServer(svc)
	httpServer := httptest.NewServer(server.NewHTTPHandler(svc))
	defer httpServer.Close()

	grpcSession, err := svc.CreateSession(ctx, "/grpc", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("Create gRPC session: %v", err)
	}
	grpcRename, err := grpcServer.RenameSession(ctx, &mecatlv1.RenameSessionRequest{SessionId: string(grpcSession.ID), Title: "renamed"})
	if err != nil {
		t.Fatalf("gRPC RenameSession: %v", err)
	}
	if got := grpcRename.GetSession(); got.GetTitle() != "renamed" || got.GetTitleProvenance() != string(session.TitleProvenanceOperator) {
		t.Fatalf("gRPC rename response = %+v", got)
	}
	if _, err := grpcServer.DeleteSession(ctx, &mecatlv1.DeleteSessionRequest{SessionId: string(grpcSession.ID)}); err != nil {
		t.Fatalf("gRPC DeleteSession: %v", err)
	}
	if _, err := grpcServer.DeleteSession(ctx, &mecatlv1.DeleteSessionRequest{SessionId: string(grpcSession.ID)}); err != nil {
		t.Fatalf("gRPC idempotent delete = %v", err)
	}

	httpSession, err := svc.CreateSession(ctx, "/http", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("Create HTTP session: %v", err)
	}
	resp, err := http.Post(httpServer.URL+"/v1/sessions/"+string(httpSession.ID)+"/rename", "application/json", strings.NewReader(`{"title":"renamed"}`))
	if err != nil {
		t.Fatalf("HTTP rename: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("HTTP rename status = %d", resp.StatusCode)
	}
	var got struct {
		Title           string `json:"title"`
		TitleProvenance string `json:"title_provenance"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		resp.Body.Close()
		t.Fatalf("decode HTTP rename: %v", err)
	}
	resp.Body.Close()
	if got.Title != "renamed" || got.TitleProvenance != string(session.TitleProvenanceOperator) {
		t.Fatalf("HTTP rename response = %+v", got)
	}
	resp, err = http.Post(httpServer.URL+"/v1/sessions/"+string(httpSession.ID)+"/delete", "application/json", nil)
	if err != nil {
		t.Fatalf("HTTP delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("HTTP delete status = %d", resp.StatusCode)
	}
	resp, err = http.Post(httpServer.URL+"/v1/sessions/"+string(httpSession.ID)+"/delete", "application/json", nil)
	if err != nil {
		t.Fatalf("HTTP second delete: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("HTTP second delete status = %d, want 204", resp.StatusCode)
	}
}

func TestSessionInventoryProjectsDeleteStorageCapability(t *testing.T) {
	for _, tc := range []struct {
		name       string
		store      port.SessionStore
		wantDelete bool
		wantReason server.CapabilityReason
	}{
		{name: "prunable", store: memstore.New(), wantDelete: true},
		{name: "explicitly unsupported", store: &deleteUnsupportedSessionStore{Store: memstore.New()}, wantReason: server.CapabilityReasonStorageUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newSessionManagementServiceWithStore(t, tc.store, nil)
			sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			rows, err := svc.ListSessions(context.Background())
			if err != nil {
				t.Fatalf("ListSessions: %v", err)
			}
			if len(rows) != 1 || rows[0].SessionID != string(sess.ID) {
				t.Fatalf("ListSessions = %+v, want session %q", rows, sess.ID)
			}
			if rows[0].Capabilities.Delete != tc.wantDelete || rows[0].Reasons.Delete != tc.wantReason {
				t.Fatalf("delete capability/reason = %v/%q, want %v/%q", rows[0].Capabilities.Delete, rows[0].Reasons.Delete, tc.wantDelete, tc.wantReason)
			}
		})
	}
}

func TestSessionManagementActionReasonsTransportParity(t *testing.T) {
	store := &pagingOnlySessionStore{inner: memstore.New(memstore.WithNow(func() time.Time { return time.Unix(2_000, 0) }))}
	svc := newSessionManagementServiceWithStore(t, store, nil)
	ctx := context.Background()
	main, err := svc.CreateSession(ctx, "/main", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	child := session.New("child", session.ModeDefault, "/child", session.Limits{}, time.Unix(1, 0))
	if err := child.RestoreSessionMetadata(session.SessionKindSubagent, session.SessionRelationship{ParentSessionID: main.ID, CallID: "call"}); err != nil {
		t.Fatalf("RestoreSessionMetadata: %v", err)
	}
	if err := store.Save(ctx, child); err != nil {
		t.Fatalf("Save child: %v", err)
	}

	grpcClient, cleanup := dialGRPC(t, svc)
	defer cleanup()
	grpcList, err := grpcClient.ListSessions(ctx, &mecatlv1.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("gRPC ListSessions: %v", err)
	}
	httpServer := httptest.NewServer(server.NewHTTPHandler(svc))
	defer httpServer.Close()
	resp, err := http.Get(httpServer.URL + "/v1/sessions")
	if err != nil {
		t.Fatalf("HTTP ListSessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP ListSessions status = %d", resp.StatusCode)
	}
	var httpList mecatlv1.ListSessionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&httpList); err != nil {
		t.Fatalf("decode HTTP ListSessions: %v", err)
	}

	assertRows := func(t *testing.T, rows []*mecatlv1.SessionSummary) {
		t.Helper()
		byID := make(map[string]*mecatlv1.SessionSummary, len(rows))
		for _, row := range rows {
			byID[row.GetSessionId()] = row
		}
		mainRow := byID[string(main.ID)]
		if mainRow == nil {
			t.Fatalf("missing main row: %+v", rows)
		}
		mainReasons := mainRow.GetCapabilities().GetReasons()
		if !mainRow.GetCapabilities().GetFork() || !mainRow.GetCapabilities().GetRename() || mainRow.GetCapabilities().GetDelete() ||
			mainReasons.GetFork() != "" || mainReasons.GetRename() != "" || mainReasons.GetDelete() != string(server.CapabilityReasonStorageUnsupported) {
			t.Fatalf("main capabilities/reasons = %+v", mainRow.GetCapabilities())
		}
		childRow := byID["child"]
		if childRow == nil {
			t.Fatalf("missing child row: %+v", rows)
		}
		childReasons := childRow.GetCapabilities().GetReasons()
		for action, got := range map[string]string{
			"public_chat": childReasons.GetPublicChat(), "fork": childReasons.GetFork(),
			"rename": childReasons.GetRename(), "delete": childReasons.GetDelete(),
		} {
			if got != string(server.CapabilityReasonInspectOnlyKind) {
				t.Errorf("child %s reason = %q, want %q", action, got, server.CapabilityReasonInspectOnlyKind)
			}
		}
		if childReasons.GetCopyId() != "" || childReasons.GetViewTranscript() != "" {
			t.Errorf("enabled child action reasons = %+v, want empty", childReasons)
		}
	}
	assertRows(t, grpcList.GetSessions())
	assertRows(t, httpList.GetSessions())
}

func TestForkSessionUsesManagementGateAndScopedLease(t *testing.T) {
	lease := &fakeLease{}
	svc, store := newSessionManagementService(t, true, lease, nil)
	alice := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser})
	bob := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser})
	src, err := svc.CreateSession(alice, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := svc.ForkSession(bob, src.ID, "", ""); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("foreign ForkSession = %v, want ErrNotFound", err)
	}
	if lease.acquires != 0 {
		t.Fatalf("foreign fork acquired %d leases, want 0", lease.acquires)
	}

	child := session.New("subagent-legacy", session.ModeDefault, "/ws", session.Limits{}, time.Unix(1, 0))
	child.Owner = session.PrincipalFromContext(alice).Clone()
	if err := child.RestoreSessionMetadata(session.SessionKindMain, session.SessionRelationship{}); err != nil {
		t.Fatalf("RestoreSessionMetadata: %v", err)
	}
	if err := store.Save(alice, child); err != nil {
		t.Fatalf("Save child: %v", err)
	}
	if _, err := svc.ForkSession(alice, child.ID, "", ""); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("legacy-prefix ForkSession = %v, want ErrFailedPrecondition", err)
	}

	forkID, err := svc.ForkSession(alice, src.ID, "", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if forkID == "" {
		t.Fatal("ForkSession returned empty id")
	}
	if lease.acquires != 1 || lease.releaseCount() != 1 {
		t.Fatalf("fork lease calls = acquire %d release %d, want 1/1", lease.acquires, lease.releaseCount())
	}
}

func TestSessionManagementMutationLeasesAreScoped(t *testing.T) {
	lease := &fakeLease{}
	svc, _ := newSessionManagementService(t, false, lease, nil)
	ctx := context.Background()
	rename, err := svc.CreateSession(ctx, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("Create rename: %v", err)
	}
	if _, err := svc.RenameSession(ctx, rename.ID, "renamed"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if lease.acquires != 1 || lease.releaseCount() != 1 {
		t.Fatalf("rename lease calls = acquire %d release %d, want 1/1", lease.acquires, lease.releaseCount())
	}

	preheld, err := svc.CreateSession(ctx, "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("Create preheld: %v", err)
	}
	run, err := svc.StartRun(ctx, preheld.ID, "hello")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for range run.Events() {
	}
	svc.FinishRun(preheld.ID, run)
	beforeAcquire, beforeRelease := lease.acquires, lease.releaseCount()
	if _, err := svc.RenameSession(ctx, preheld.ID, "still held"); err != nil {
		t.Fatalf("RenameSession preheld: %v", err)
	}
	if lease.acquires != beforeAcquire || lease.releaseCount() != beforeRelease {
		t.Fatalf("preheld lease changed: acquire %d→%d release %d→%d", beforeAcquire, lease.acquires, beforeRelease, lease.releaseCount())
	}
}

func TestSessionManagementFailedDeleteLeaseOwnership(t *testing.T) {
	t.Run("newly acquired lease is released", func(t *testing.T) {
		lease := &fakeLease{}
		store := &failingDeleteSessionStore{Store: memstore.New(), err: errors.New("delete failed")}
		svc := newSessionManagementServiceWithStore(t, store, lease)
		sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if err := svc.DeleteSession(context.Background(), sess.ID); !errors.Is(err, server.ErrInternal) {
			t.Fatalf("DeleteSession = %v, want ErrInternal", err)
		}
		if lease.acquires != 1 || lease.releaseCount() != 1 {
			t.Fatalf("failed delete lease calls = acquire %d release %d, want 1/1", lease.acquires, lease.releaseCount())
		}
	})

	t.Run("pre-held lease is preserved", func(t *testing.T) {
		lease := &fakeLease{}
		store := &failingDeleteSessionStore{Store: memstore.New(), err: errors.New("delete failed")}
		svc := newSessionManagementServiceWithStore(t, store, lease)
		sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		run, err := svc.StartRun(context.Background(), sess.ID, "hello")
		if err != nil {
			t.Fatalf("StartRun: %v", err)
		}
		for range run.Events() {
		}
		svc.FinishRun(sess.ID, run)
		beforeAcquire, beforeRelease := lease.acquires, lease.releaseCount()
		if err := svc.DeleteSession(context.Background(), sess.ID); !errors.Is(err, server.ErrInternal) {
			t.Fatalf("DeleteSession = %v, want ErrInternal", err)
		}
		if lease.acquires != beforeAcquire || lease.releaseCount() != beforeRelease {
			t.Fatalf("failed delete changed pre-held lease: acquire %d→%d release %d→%d", beforeAcquire, lease.acquires, beforeRelease, lease.releaseCount())
		}
		if svc.ActiveRuns() != 1 {
			t.Fatalf("held leases = %d, want pre-held lease retained", svc.ActiveRuns())
		}
	})
}
