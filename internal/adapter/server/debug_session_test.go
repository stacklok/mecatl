package server_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
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

func debugTestEngine(text string) *agent.Engine {
	return agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn(text)), Catalog: tool.NewCatalog(),
		Policy: permpolicy.NewPolicy(nil, nil), Model: "debug-test",
	})
}

func debugTestService(t *testing.T, store port.SessionStore, ownerEnforced bool, factory server.DebugSessionEngineFactory) *server.Service {
	t.Helper()
	svc, err := server.NewService(server.Config{
		Engine: debugTestEngine("shared"), Store: store,
		Workspaces:        func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:               func() time.Time { return time.Unix(1700000000, 0).UTC() },
		OwnershipEnforced: ownerEnforced, DebugSessionEngine: factory, DebugMCP: true,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func debugFactory(calls *int) server.DebugSessionEngineFactory {
	return func(_ context.Context, _ server.ProviderSelector, profile server.SessionProfile, mode session.PermissionMode, target session.SessionID, fingerprint string, _ *session.Principal, selected, _ []string) (server.SessionEngineResult, error) {
		*calls++
		if profile != server.ProfileNoFS || target == "" || fingerprint == "" {
			return server.SessionEngineResult{}, errors.New("invalid debug factory inputs")
		}
		var mounted []string
		for _, name := range selected {
			mounted = append(mounted, "mcp__"+name+"__tool")
		}
		return server.SessionEngineResult{Engine: debugTestEngine("debug"), BuiltForMode: mode, DebugMCPTools: mounted}, nil
	}
}

func TestDebugSessionCreateIsSeparateAndTargetImmutable(t *testing.T) {
	store := memstore.New()
	calls := 0
	svc := debugTestService(t, store, false, debugFactory(&calls))
	target, err := svc.CreateSession(context.Background(), "/target", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	if err := target.RecordUserPrompt("target evidence", nil); err != nil {
		t.Fatalf("record target prompt: %v", err)
	}
	if err := store.Save(context.Background(), target); err != nil {
		t.Fatalf("save target: %v", err)
	}
	before, err := store.Load(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("load target before: %v", err)
	}

	debug, err := svc.CreateSessionWithProfile(context.Background(), "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithDebugTarget(target.ID))
	if err != nil {
		t.Fatalf("create debug session: %v", err)
	}
	if calls != 1 || debug.ID == target.ID || debug.Kind != session.SessionKindDebug || debug.Relationship.DebugTargetID != target.ID {
		t.Fatalf("debug identity = id %q kind %q relationship %+v calls %d", debug.ID, debug.Kind, debug.Relationship, calls)
	}
	if debug.Workspace != "" || debug.Profile != string(server.ProfileNoFS) || len(debug.Conversation.Messages) != 0 {
		t.Fatalf("debug session carried target state: workspace=%q profile=%q messages=%d", debug.Workspace, debug.Profile, len(debug.Conversation.Messages))
	}
	after, err := store.Load(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("load target after: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("debug creation mutated the target session")
	}
}

func TestDebugSessionCreateRulesAndOwnershipConcealment(t *testing.T) {
	store := memstore.New()
	calls := 0
	svc := debugTestService(t, store, true, debugFactory(&calls))
	alice := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "issuer", Subject: "alice"})
	bob := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "issuer", Subject: "bob"})
	target, err := svc.CreateSession(alice, "/target", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}

	_, foreignErr := svc.CreateSessionWithProfile(bob, "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithDebugTarget(target.ID))
	_, missingErr := svc.CreateSessionWithProfile(bob, "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithDebugTarget("missing"))
	if !errors.Is(foreignErr, server.ErrNotFound) || !errors.Is(missingErr, server.ErrNotFound) || foreignErr.Error() != missingErr.Error() {
		t.Fatalf("foreign/missing errors differ: foreign=%v missing=%v", foreignErr, missingErr)
	}
	if calls != 0 {
		t.Fatalf("debug factory called before ownership authorization: %d", calls)
	}
	if _, err := svc.CreateSessionWithProfile(alice, "/wrong", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithDebugTarget(target.ID)); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("debug workspace rule: %v", err)
	}
	if _, err := svc.CreateSessionWithProfile(alice, "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithDebugTarget(target.ID)); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("debug profile rule: %v", err)
	}
	if _, err := svc.CreateSessionWithProfile(alice, "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithDebugTarget(target.ID), server.WithSourceSession(target.ID)); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("debug/source relationship rule: %v", err)
	}
	if _, err := svc.CreateSessionWithProfile(alice, "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithDebugMCP([]string{"github"})); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("debug MCP without target: %v", err)
	}
	if _, err := svc.CreateSessionWithProfile(alice, "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithDebugTarget(target.ID), server.WithDebugMCP([]string{"github", "github"})); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("duplicate debug MCP: %v", err)
	}
	if _, err := svc.CreateSessionWithProfile(alice, "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithDebugTarget(target.ID), server.WithDebugMCP([]string{"bad/name"})); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("invalid debug MCP name: %v", err)
	}
	debug, err := svc.CreateSessionWithProfile(alice, "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithDebugTarget(target.ID), server.WithDebugMCP([]string{"github", "slack"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(debug.DebugMCPServers, ",") != "github,slack" || strings.Join(debug.DebugMCPTools, ",") != "mcp__github__tool,mcp__slack__tool" {
		t.Fatalf("persisted debug MCP labels = %v/%v", debug.DebugMCPServers, debug.DebugMCPTools)
	}
}

func TestDebugSessionGRPCProjectionAndCapability(t *testing.T) {
	store := memstore.New()
	calls := 0
	svc := debugTestService(t, store, false, debugFactory(&calls))
	target, err := svc.CreateSession(context.Background(), "/target", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	h := server.NewHarnessServer(svc)
	created, err := h.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
		Profile: string(server.ProfileNoFS), DebugTargetSessionId: string(target.ID), DebugMcpServers: []string{"github"},
	})
	if err != nil {
		t.Fatalf("grpc CreateSession: %v", err)
	}
	if !created.GetCapabilities().GetSessionDebug() || !created.GetCapabilities().GetDebugMcp() {
		t.Fatal("session_debug/debug_mcp capability is false with a debug MCP factory")
	}
	got, err := h.GetSession(context.Background(), &mecatlv1.GetSessionRequest{SessionId: created.GetSessionId()})
	if err != nil {
		t.Fatalf("grpc GetSession: %v", err)
	}
	if got.GetSession().GetKind() != string(session.SessionKindDebug) || got.GetSession().GetRelationship().GetDebugTargetSessionId() != string(target.ID) {
		t.Fatalf("grpc debug identity = kind %q relationship %+v", got.GetSession().GetKind(), got.GetSession().GetRelationship())
	}
	if strings.Join(got.GetSession().GetDebugMcpServers(), ",") != "github" || strings.Join(got.GetSession().GetDebugMcpTools(), ",") != "mcp__github__tool" {
		t.Fatalf("grpc debug MCP labels = %v/%v", got.GetSession().GetDebugMcpServers(), got.GetSession().GetDebugMcpTools())
	}
}

func TestPersistedDebugSessionRejectsTargetReplacement(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	calls := 0
	svc1 := debugTestService(t, store, false, debugFactory(&calls))
	target, err := svc1.CreateSession(ctx, "/target", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	debug, err := svc1.CreateSessionWithProfile(ctx, "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithDebugTarget(target.ID))
	if err != nil {
		t.Fatal(err)
	}
	if debug.DebugTargetFingerprint == "" || debug.DebugTargetFingerprint != session.DebugTargetFingerprint(target) {
		t.Fatal("debug target incarnation was not persisted")
	}
	svc1.CloseSession(debug.ID)
	if err := store.Delete(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	replacement := session.New(target.ID, session.ModeDefault, "/target", session.Limits{}, target.CreatedAt)
	if replacement.Incarnation() == target.Incarnation() || replacement.ID != target.ID || replacement.CreatedAt != target.CreatedAt {
		t.Fatal("replacement did not preserve identical ID/time while changing only incarnation")
	}
	if err := store.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	svc2 := debugTestService(t, store, false, debugFactory(&calls))
	if _, err := svc2.StartRun(ctx, debug.ID, "diagnose replacement"); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("replacement target rehydration = %v, want inaccessible", err)
	}
}

func TestPersistedDebugSessionRehydrationUsesStableOwnerIdentity(t *testing.T) {
	store := memstore.New()
	calls := 0
	owner := &session.Principal{Issuer: "issuer-a", Subject: "subject", GrantType: session.GrantTypeUser, Name: "before"}
	createCtx := session.WithPrincipal(context.Background(), owner)
	svc1 := debugTestService(t, store, true, debugFactory(&calls))
	target, err := svc1.CreateSession(createCtx, "/target", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	debug, err := svc1.CreateSessionWithProfile(createCtx, "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithDebugTarget(target.ID))
	if err != nil {
		t.Fatal(err)
	}
	svc1.CloseSession(debug.ID)

	svc2 := debugTestService(t, store, true, debugFactory(&calls))
	changedMetadata := session.WithPrincipal(context.Background(), &session.Principal{Issuer: owner.Issuer, Subject: owner.Subject, GrantType: session.GrantTypeClientCredentials, Name: "after"})
	run, err := svc2.StartRun(changedMetadata, debug.ID, "diagnose")
	if err != nil {
		t.Fatalf("same identity after restart: %v", err)
	}
	for range run.Events() {
	}
	svc2.CloseSession(debug.ID)

	for _, foreign := range []*session.Principal{
		{Issuer: "issuer-b", Subject: owner.Subject},
		{Issuer: owner.Issuer, Subject: "other-subject"},
	} {
		svc := debugTestService(t, store, true, debugFactory(&calls))
		if _, err := svc.StartRun(session.WithPrincipal(context.Background(), foreign), debug.ID, "diagnose"); !errors.Is(err, server.ErrNotFound) {
			t.Fatalf("foreign identity %+v was not concealed: %v", foreign, err)
		}
	}
}

func TestPersistedDebugSessionRehydrationFailsClosedWithoutFactory(t *testing.T) {
	store := memstore.New()
	calls := 0
	svc1 := debugTestService(t, store, false, debugFactory(&calls))
	target, err := svc1.CreateSession(context.Background(), "/target", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	debug, err := svc1.CreateSessionWithProfile(context.Background(), "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithDebugTarget(target.ID))
	if err != nil {
		t.Fatalf("create debug: %v", err)
	}

	svc2 := debugTestService(t, store, false, nil)
	if _, err := svc2.StartRun(context.Background(), debug.ID, "diagnose"); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("restart without debug factory = %v, want ErrInvalidArgument", err)
	}
	persisted, err := store.Load(context.Background(), debug.ID)
	if err != nil {
		t.Fatalf("load persisted debug: %v", err)
	}
	if persisted.State != session.StateIdle || len(persisted.Conversation.Messages) != 0 {
		t.Fatalf("failed rehydration mutated debug session: state=%q messages=%d", persisted.State, len(persisted.Conversation.Messages))
	}
}

func TestDebuggerLifecycleNeverMutatesOrLeasesTarget(t *testing.T) {
	store := &debugStoreSpy{Store: memstore.New()}
	leases := &debugLeaseSpy{}
	newService := func() *server.Service {
		t.Helper()
		calls := 0
		svc, err := server.NewService(server.Config{
			Engine: debugTestEngine("shared"), Store: store,
			Workspaces:         func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
			Now:                func() time.Time { return time.Unix(1700000000, 0).UTC() },
			DebugSessionEngine: debugFactory(&calls), SessionLease: leases,
			LeaseOwner: "debug-test", LeaseTTL: time.Hour, LeaseRenewInterval: time.Hour,
		})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		return svc
	}

	svc1 := newService()
	target, err := svc1.CreateSession(context.Background(), "/target", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := target.RecordUserPrompt("target evidence", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	before, err := store.Load(context.Background(), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	store.resetWrites()

	debug, err := svc1.CreateSessionWithProfile(context.Background(), "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithDebugTarget(target.ID))
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc1.StartRun(context.Background(), debug.ID, "diagnose")
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	svc1.CloseSession(debug.ID)

	// A fresh Service has no in-memory engine for the persisted debugger, so this
	// second run necessarily exercises the dedicated rehydration path.
	svc2 := newService()
	defer svc2.CloseSession(debug.ID)
	run, err = svc2.StartRun(context.Background(), debug.ID, "diagnose after restart")
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}

	after, err := store.Load(context.Background(), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("target transitioned or changed during debugger lifecycle: before=%+v after=%+v", before, after)
	}
	if ids := store.writtenIDs(); containsSessionID(ids, target.ID) {
		t.Fatalf("target was saved by debugger lifecycle: writes=%v", ids)
	}
	if ids := leases.acquiredIDs(); containsSessionID(ids, target.ID) {
		t.Fatalf("target lease was acquired by debugger lifecycle: acquires=%v", ids)
	}
	if ids := leases.acquiredIDs(); !containsSessionID(ids, debug.ID) {
		t.Fatalf("debug session itself was not leased: acquires=%v", ids)
	}
}

type debugStoreSpy struct {
	*memstore.Store
	mu     sync.Mutex
	writes []session.SessionID
}

func (s *debugStoreSpy) Save(ctx context.Context, sess *session.Session) error {
	s.mu.Lock()
	s.writes = append(s.writes, sess.ID)
	s.mu.Unlock()
	return s.Store.Save(ctx, sess)
}

func (s *debugStoreSpy) Create(ctx context.Context, sess *session.Session) error {
	s.mu.Lock()
	s.writes = append(s.writes, sess.ID)
	s.mu.Unlock()
	return s.Store.Create(ctx, sess)
}

func (s *debugStoreSpy) resetWrites() {
	s.mu.Lock()
	s.writes = nil
	s.mu.Unlock()
}

func (s *debugStoreSpy) writtenIDs() []session.SessionID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.SessionID(nil), s.writes...)
}

type debugLeaseSpy struct {
	mu       sync.Mutex
	acquired []session.SessionID
}

func (s *debugLeaseSpy) Acquire(_ context.Context, id session.SessionID, owner string) (port.Lease, error) {
	s.mu.Lock()
	s.acquired = append(s.acquired, id)
	s.mu.Unlock()
	return port.Lease{SessionID: id, Owner: owner, Token: 1, Expiry: time.Now().Add(time.Hour)}, nil
}

func (*debugLeaseSpy) Renew(_ context.Context, lease port.Lease) (port.Lease, error) {
	lease.Expiry = time.Now().Add(time.Hour)
	return lease, nil
}

func (*debugLeaseSpy) Release(context.Context, port.Lease) error { return nil }

func (s *debugLeaseSpy) acquiredIDs() []session.SessionID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.SessionID(nil), s.acquired...)
}

func containsSessionID(ids []session.SessionID, want session.SessionID) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
