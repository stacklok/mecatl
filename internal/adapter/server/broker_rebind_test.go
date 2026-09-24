package server

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// TestWorkspaceEnrollmentRebindsAfterBrokerRestart pins the live Stage 3 failure:
// a session created by one process, then reached by /tools-connect after that
// process restarted, must still be able to enroll. A Runtime's binding prefix is
// random per process and its generation counter lives in memory, so a persisted
// ExternalBinding can never match a fresh incarnation; before the rebind seam
// every attach reported a mismatch, which surfaced as the misleading "workspace
// enrollment is not pending" and left the session unable to ever connect
// workspace services again.
func TestSingletonBrokerRemediation_Scenario2_FreshClientPrePromptRecovery(t *testing.T) {
	store := memstore.New()
	firstRuntime := testBrokerRuntime(t)
	secondRuntime := testBrokerRuntime(t)
	firstBroker := &enrollmentBroker{Service: firstRuntime}
	secondBroker := &enrollmentBroker{Service: secondRuntime}
	var factoryCalls, firstCloses, secondCloses int
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID:     func() session.SessionID { return "factory-recovery" },
		MCPBroker: firstBroker, MCPBrokerClose: func() error { firstCloses++; return firstRuntime.Close() },
		MCPBrokerFactory: func(context.Context) (brokercontract.Service, func() error, error) {
			factoryCalls++
			return secondBroker, func() error { secondCloses++; return secondRuntime.Close() }, nil
		},
		WorkspaceEnrollment: true,
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
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	staleBinding := created.ExternalBinding
	if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
		t.Fatalf("begin pre-restart enrollment: %v", err)
	}
	svc.closeSessionLocal(created.ID)
	// The pinned client has authenticated a different broker incarnation. Only
	// that explicit signal may invoke the composition-owned factory and retire the
	// stale client generation; a mere per-session binding mismatch must not.
	firstBroker.attachErr = brokercontract.ErrBrokerIncarnationLost
	projection, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil || projection.Status != brokercontract.WorkspaceEnrollmentPending {
		t.Fatalf("pre-prompt recovery = %+v, %v", projection, err)
	}
	reloaded, err := store.Load(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if factoryCalls != 1 || firstCloses != 1 || reloaded.ExternalBinding == staleBinding {
		t.Fatalf("factory calls=%d stale closes=%d binding=%q, want one replacement and a fresh binding", factoryCalls, firstCloses, reloaded.ExternalBinding)
	}
	svc.Drain()
	svc.Close()
	if secondCloses != 1 {
		t.Fatalf("replacement client closes = %d, want exactly 1", secondCloses)
	}
}

func TestWorkspaceEnrollmentRecoversPinnedRemoteClientAfterBrokerRestart(t *testing.T) {
	for _, cached := range []bool{false, true} {
		name := "uncached attachment"
		if cached {
			name = "cached attachment"
		}
		t.Run(name, func(t *testing.T) {
			firstRuntime := testBrokerRuntime(t)
			firstConn, stopFirst := serveRebindBroker(t, &enrollmentBroker{Service: firstRuntime})
			defer stopFirst()
			secondRuntime := testBrokerRuntime(t)
			secondConn, stopSecond := serveRebindBroker(t, &enrollmentBroker{Service: secondRuntime})
			defer stopSecond()

			switcher := &rebindSwitchConn{current: firstConn}
			transport := mcpbrokergrpc.DefaultConfig()
			pinned, err := mcpbrokergrpc.NewClientWithConfig(switcher, transport)
			if err != nil {
				t.Fatal(err)
			}
			var factoryCalls int
			svc, err := NewService(Config{
				Engine: brokerEngineResult().Engine, Store: memstore.New(),
				PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
				NewID:     func() session.SessionID { return "remote-restart-recovery" },
				MCPBroker: pinned, MCPBrokerClose: func() error { return nil },
				MCPBrokerFactory: func(context.Context) (brokercontract.Service, func() error, error) {
					factoryCalls++
					fresh, freshErr := mcpbrokergrpc.NewClientWithConfig(switcher, transport)
					return fresh, func() error { return nil }, freshErr
				},
				WorkspaceEnrollment: true,
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
			staleBinding := created.ExternalBinding
			if cached {
				if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
					t.Fatalf("begin enrollment: %v", err)
				}
			} else {
				svc.closeSessionLocal(created.ID)
			}

			switcher.set(secondConn)
			projection, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
			if err != nil || projection.Status != brokercontract.WorkspaceEnrollmentPending {
				t.Fatalf("recovery = %+v, %v", projection, err)
			}
			reloaded, err := svc.cfg.Store.Load(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if factoryCalls != 1 || reloaded.ExternalBinding == staleBinding {
				t.Fatalf("factory calls=%d binding=%q, want one replacement and a fresh binding", factoryCalls, reloaded.ExternalBinding)
			}
		})
	}
}

func TestWorkspaceEnrollmentRebindSaveFailureDropsTransientAttachmentAndRetries(t *testing.T) {
	inner := memstore.New()
	store := &failNextAuthorizationSaveStore{SessionStore: inner}
	freshRuntime := testBrokerRuntime(t)
	fresh := &multiEnrollmentBroker{Service: freshRuntime}
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		MCPBroker: &alwaysIncarnationLostBroker{}, MCPBrokerClose: func() error { return nil },
		MCPBrokerFactory: func(context.Context) (brokercontract.Service, func() error, error) {
			return fresh, func() error { return nil }, nil
		},
		WorkspaceEnrollment: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	id := session.SessionID("rebind-save-failure")
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/repo", Revision: "v1"}, session.Limits{}, time.Now())
	sess.ExternalBinding = "stale-binding"
	if err := sess.BindAuthority(session.Authority{
		CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}},
		Provenance:    "derived-test",
	}); err != nil {
		t.Fatalf("bind authority: %v", err)
	}
	pending := session.PendingWorkspaceEnrollment{ID: "lost-enrollment", RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour)}
	if err := sess.BeginWorkspaceEnrollment(pending); err != nil {
		t.Fatal(err)
	}
	if err := inner.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}

	store.failures.Store(1)
	if _, _, _, err := svc.workspaceEnrollmentTarget(t.Context(), id); !errors.Is(err, ErrInternal) {
		t.Fatalf("rebind save failure = %v, want internal error", err)
	}
	svc.mu.Lock()
	leaked := svc.brokerAttachments[id] != nil
	_, leakedGeneration := svc.brokerAttachmentGeneration[id]
	svc.mu.Unlock()
	if leaked || leakedGeneration {
		t.Fatal("failed rebind retained a transient attachment")
	}
	reloaded, err := inner.Load(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ExternalBinding != "stale-binding" {
		t.Fatalf("failed rebind persisted binding %q", reloaded.ExternalBinding)
	}
	if got, ok := reloaded.PendingWorkspaceEnrollment(); !ok || got != pending {
		t.Fatalf("failed rebind changed durable pending enrollment: %+v, %v", got, ok)
	}

	_, enroller, release, err := svc.workspaceEnrollmentTarget(t.Context(), id)
	if err != nil {
		t.Fatalf("retry rebind: %v", err)
	}
	if _, err := enroller.BeginWorkspaceEnrollment(t.Context()); err != nil {
		release()
		t.Fatalf("retry attachment unusable: %v", err)
	}
	release()
	reloaded, err = inner.Load(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ExternalBinding == "stale-binding" {
		t.Fatal("retry did not persist the replacement binding")
	}
	if _, ok := reloaded.PendingWorkspaceEnrollment(); ok {
		t.Fatal("retry retained the lost pending enrollment")
	}
}

func TestWorkspaceEnrollmentRebindCommitFailureAbortsWithoutPublishing(t *testing.T) {
	store := memstore.New()
	fresh := &commitFailureBroker{}
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		MCPBroker: &alwaysIncarnationLostBroker{}, MCPBrokerClose: func() error { return nil },
		MCPBrokerFactory: func(context.Context) (brokercontract.Service, func() error, error) {
			return fresh, func() error { return nil }, nil
		},
		WorkspaceEnrollment: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	id := session.SessionID("rebind-commit-failure")
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/repo", Revision: "v1"}, session.Limits{}, time.Now())
	sess.ExternalBinding = "stale-binding"
	if err := store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := svc.workspaceEnrollmentTarget(t.Context(), id); !errors.Is(err, ErrInternal) {
		t.Fatalf("commit failure = %v, want internal error", err)
	}
	first := fresh.first
	if first == nil || first.aborts.Load() != 1 {
		t.Fatalf("provisional attachment aborts = %v, want 1", first)
	}
	svc.mu.Lock()
	leaked := svc.brokerAttachments[id] != nil
	_, leakedGeneration := svc.brokerAttachmentGeneration[id]
	svc.mu.Unlock()
	if leaked || leakedGeneration {
		t.Fatal("commit failure published an attachment")
	}

	_, _, release, err := svc.workspaceEnrollmentTarget(t.Context(), id)
	if err != nil {
		t.Fatalf("retry after commit failure: %v", err)
	}
	release()
}

type commitFailureBroker struct {
	brokercontract.Service
	mu    sync.Mutex
	calls int
	first *commitFailureAttachment
}

func (b *commitFailureBroker) AttachSession(context.Context, session.SessionID) (brokercontract.Attachment, brokercontract.AttachOutcome, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	attachment := &commitFailureAttachment{binding: session.ExternalBinding("commit-binding")}
	if b.calls == 1 {
		attachment.fail = true
		b.first = attachment
	}
	ref := brokercontract.WorkspaceEnrollmentRef{ID: "commit-enrollment", RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour)}
	return &enrollmentAttachment{Attachment: attachment, ref: ref, result: brokercontract.WorkspaceEnrollmentResult{Ref: ref, Status: brokercontract.WorkspaceEnrollmentPending}}, brokercontract.AttachCreated, nil
}

func (*commitFailureBroker) DeleteSession(context.Context, session.SessionID) (brokercontract.DeleteOutcome, error) {
	return brokercontract.DeleteDeleted, nil
}

type commitFailureAttachment struct {
	brokercontract.Attachment
	binding session.ExternalBinding
	fail    bool
	aborts  atomic.Int32
}

func (a *commitFailureAttachment) Commit(context.Context) error {
	if a.fail {
		return errors.New("commit failed")
	}
	return nil
}

func (a *commitFailureAttachment) Abort(context.Context) error {
	a.aborts.Add(1)
	return nil
}

func (a *commitFailureAttachment) Binding() session.ExternalBinding { return a.binding }
func (*commitFailureAttachment) Tools() []tool.Tool                 { return nil }
func (*commitFailureAttachment) Close(context.Context) (brokercontract.CloseOutcome, error) {
	return brokercontract.CloseClosed, nil
}

type alwaysIncarnationLostBroker struct{ brokercontract.Service }

func (*alwaysIncarnationLostBroker) AttachSession(context.Context, session.SessionID) (brokercontract.Attachment, brokercontract.AttachOutcome, error) {
	return nil, "", brokercontract.ErrBrokerIncarnationLost
}

func TestBrokerCloseJoinsInFlightClientReplacement(t *testing.T) {
	store := memstore.New()
	freshRuntime := testBrokerRuntime(t)
	fresh := &multiEnrollmentBroker{Service: freshRuntime}
	factoryEntered := make(chan struct{})
	releaseFactory := make(chan struct{})
	var freshCloses atomic.Int32
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		MCPBroker: &alwaysIncarnationLostBroker{}, MCPBrokerClose: func() error { return nil },
		MCPBrokerFactory: func(context.Context) (brokercontract.Service, func() error, error) {
			close(factoryEntered)
			<-releaseFactory
			return fresh, func() error {
				freshCloses.Add(1)
				return freshRuntime.Close()
			}, nil
		},
		WorkspaceEnrollment: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := session.SessionID("close-during-replacement")
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/repo", Revision: "v1"}, session.Limits{}, time.Now())
	sess.ExternalBinding = "stale-binding"
	if err := store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}

	type targetResult struct {
		release func()
		err     error
	}
	targetDone := make(chan targetResult, 1)
	go func() {
		_, _, release, targetErr := svc.workspaceEnrollmentTarget(context.Background(), id)
		targetDone <- targetResult{release: release, err: targetErr}
	}()
	<-factoryEntered
	closeDone := make(chan struct{})
	go func() {
		svc.Close()
		close(closeDone)
	}()
	closeStartedDeadline := time.Now().Add(2 * time.Second)
	for svc.closeMu.TryLock() {
		svc.closeMu.Unlock()
		if time.Now().After(closeStartedDeadline) {
			t.Fatal("service close did not start")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-closeDone:
		t.Fatal("service close returned while replacement factory was in flight")
	default:
	}
	close(releaseFactory)
	result := <-targetDone
	if result.err != nil {
		t.Fatalf("in-flight replacement: %v", result.err)
	}
	result.release()
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("service close did not join replacement")
	}
	if freshCloses.Load() != 1 {
		t.Fatalf("replacement closes = %d, want 1", freshCloses.Load())
	}
	svc.mu.Lock()
	attachments := len(svc.brokerAttachments)
	svc.mu.Unlock()
	if attachments != 0 {
		t.Fatalf("attachments after close = %d, want 0", attachments)
	}
}

func TestConcurrentWorkspaceEnrollmentRebindCoalescesBrokerReplacement(t *testing.T) {
	store := memstore.New()
	stale := &incarnationLostBroker{ready: make(chan struct{})}
	freshRuntime := testBrokerRuntime(t)
	fresh := &multiEnrollmentBroker{Service: freshRuntime}
	var factoryCalls, freshCloses int
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		MCPBroker: stale, MCPBrokerClose: func() error { return nil },
		MCPBrokerFactory: func(context.Context) (brokercontract.Service, func() error, error) {
			factoryCalls++
			return fresh, func() error { freshCloses++; return nil }, nil
		},
		WorkspaceEnrollment: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	for _, id := range []session.SessionID{"concurrent-rebind-a", "concurrent-rebind-b"} {
		sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/repo", Revision: "v1"}, session.Limits{}, time.Now())
		sess.ExternalBinding = "stale-binding"
		if err := store.Save(t.Context(), sess); err != nil {
			t.Fatal(err)
		}
	}

	type result struct {
		id       session.SessionID
		enroller brokercontract.WorkspaceEnrollmentAttachment
		release  func()
		err      error
	}
	results := make(chan result, 2)
	for _, id := range []session.SessionID{"concurrent-rebind-a", "concurrent-rebind-b"} {
		go func() {
			_, enroller, release, err := svc.workspaceEnrollmentTarget(context.Background(), id)
			results <- result{id: id, enroller: enroller, release: release, err: err}
		}()
	}
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatalf("workspace enrollment target %q: %v", got.id, got.err)
		}
		if _, err := got.enroller.BeginWorkspaceEnrollment(t.Context()); err != nil {
			got.release()
			t.Fatalf("replacement attachment %q is unusable: %v", got.id, err)
		}
		got.release()
	}

	// A third pre-restart session reaches the already-published generation later.
	// Its stale binding is session-local state loss, not proof that the shared
	// client generation is stale, so adopting it must not rotate the client again.
	lateID := session.SessionID("late-binding-mismatch")
	late := session.New(lateID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/repo", Revision: "v1"}, session.Limits{}, time.Now())
	late.ExternalBinding = "stale-binding"
	if err := store.Save(t.Context(), late); err != nil {
		t.Fatal(err)
	}
	_, lateEnroller, lateRelease, err := svc.workspaceEnrollmentTarget(t.Context(), lateID)
	if err != nil {
		t.Fatalf("late binding adoption: %v", err)
	}
	if _, err := lateEnroller.BeginWorkspaceEnrollment(t.Context()); err != nil {
		lateRelease()
		t.Fatalf("late replacement attachment is unusable: %v", err)
	}
	lateRelease()
	if factoryCalls != 1 || freshCloses != 0 {
		t.Fatalf("replacement lifecycle = factory %d closes %d, want 1/0", factoryCalls, freshCloses)
	}
}

type incarnationLostBroker struct {
	brokercontract.Service
	mu    sync.Mutex
	calls int
	ready chan struct{}
}

func (b *incarnationLostBroker) AttachSession(context.Context, session.SessionID) (brokercontract.Attachment, brokercontract.AttachOutcome, error) {
	b.mu.Lock()
	b.calls++
	if b.calls == 2 {
		close(b.ready)
	}
	ready := b.ready
	b.mu.Unlock()
	<-ready
	return nil, "", brokercontract.ErrBrokerIncarnationLost
}

type multiEnrollmentBroker struct{ brokercontract.Service }

func (b *multiEnrollmentBroker) AttachSession(ctx context.Context, id session.SessionID) (brokercontract.Attachment, brokercontract.AttachOutcome, error) {
	attachment, outcome, err := b.Service.AttachSession(ctx, id)
	if err != nil {
		return nil, outcome, err
	}
	ref := brokercontract.WorkspaceEnrollmentRef{ID: session.WorkspaceEnrollmentID("enrollment-" + string(id)), RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour)}
	wrapped := &enrollmentAttachment{Attachment: attachment, ref: ref, result: brokercontract.WorkspaceEnrollmentResult{Ref: ref, Status: brokercontract.WorkspaceEnrollmentPending}}
	return wrapped, outcome, nil
}

type rebindSwitchConn struct {
	mu      sync.RWMutex
	current grpc.ClientConnInterface
}

func (c *rebindSwitchConn) set(conn grpc.ClientConnInterface) {
	c.mu.Lock()
	c.current = conn
	c.mu.Unlock()
}

func (c *rebindSwitchConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	c.mu.RLock()
	current := c.current
	c.mu.RUnlock()
	return current.Invoke(ctx, method, args, reply, opts...)
}

func (c *rebindSwitchConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	c.mu.RLock()
	current := c.current
	c.mu.RUnlock()
	return current.NewStream(ctx, desc, method, opts...)
}

func serveRebindBroker(t *testing.T, service brokercontract.Service) (grpc.ClientConnInterface, func()) {
	t.Helper()
	return serveRebindBrokerWithOptions(t, service)
}

func serveRebindBrokerWithOptions(t *testing.T, service brokercontract.Service, options ...grpc.ServerOption) (grpc.ClientConnInterface, func()) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	cfg := mcpbrokergrpc.DefaultConfig()
	cfg.MaxHandles = 64
	brokerServer, err := mcpbrokergrpc.NewServer(service, cfg)
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer(options...)
	mcpbrokergrpc.RegisterServer(grpcServer, brokerServer)
	go func() { _ = grpcServer.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///broker",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	return conn, func() {
		_ = conn.Close()
		grpcServer.Stop()
		_ = brokerServer.Shutdown(context.Background())
		_ = listener.Close()
	}
}

func TestWorkspaceEnrollmentRebindsAfterBrokerRestart(t *testing.T) {
	newService := func(t *testing.T, store *memstore.Store, broker brokercontract.Service) *Service {
		t.Helper()
		svc, err := NewService(Config{
			Engine:            brokerEngineResult().Engine,
			Store:             store,
			PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
			NewID:     func() session.SessionID { return "restarted-broker-session" },
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
		return svc
	}

	for _, tc := range []struct {
		name        string
		enrollFirst bool
	}{
		{name: "never enrolled"},
		{name: "pending enrollment lost with the incarnation", enrollFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memstore.New()
			firstRuntime := testBrokerRuntime(t)
			defer firstRuntime.Close()
			first := newService(t, store, &enrollmentBroker{Service: firstRuntime})
			created, err := first.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			original := created.ExternalBinding
			if original == "" {
				t.Fatal("create did not stamp an external binding")
			}
			if tc.enrollFirst {
				if _, err := first.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
					t.Fatal(err)
				}
			}
			first.Close()

			// The restart: a brand-new Runtime with its own random binding prefix over
			// the SAME durable store, exactly as the mecak8s pod does on an image refresh.
			secondRuntime := testBrokerRuntime(t)
			defer secondRuntime.Close()
			second := newService(t, store, &enrollmentBroker{Service: secondRuntime})
			defer second.Close()

			projection, err := second.ConnectWorkspaceServices(t.Context(), created.ID)
			if err != nil {
				t.Fatalf("connect workspace services after a broker restart: %v", err)
			}
			if projection.Status != brokercontract.WorkspaceEnrollmentPending {
				t.Fatalf("status = %q, want pending", projection.Status)
			}
			reloaded, err := store.Load(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.ExternalBinding == original {
				t.Fatal("the stale binding survived the rebind")
			}
			if _, ok := reloaded.PendingWorkspaceEnrollment(); !ok {
				t.Fatal("rebind did not persist a fresh pending enrollment")
			}
			// The rebound attachment is the live one, so the next call observes
			// instead of dead-ending on another mismatch.
			if _, err := second.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
				t.Fatalf("observe after rebind: %v", err)
			}
		})
	}
}
