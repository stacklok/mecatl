package app

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/stacklok/mecatl/adapters/grpcdriver"
	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/memattempt"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
)

func TestBuildRecoveryLoopsWaitForSuccessfulCompositionAndJoinBeforeTeardown(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				diag := &startupLoopDiagnostics{
					attempts: newStartupLoopReport(), reservations: newStartupLoopReport(),
				}
				defer diag.attempts.unblock()
				defer diag.reservations.unblock()
				attempts := &startupDiscoveryAttempts{AttemptRepository: memattempt.New(wallclock.Clock{})}
				ledger := &startupDiscoveryLedger{AutomaticAdmissionLedger: automaticStoreForTest(t, t.TempDir(), defaultLearningAutomaticConfig())}
				addr, conns := startStartupLoopDriver(t, diag.captureContext, func(s *grpc.Server) {
					driverv1.RegisterLearningRepositoryCapabilitiesServiceServer(s, grpcdriver.NewLearningRepositoryCapabilitiesServer(grpcdriver.LearningRepositoryCapabilities{
						AttemptRepository: true, ProposalRepository: true, SkillRepository: true,
						ValidatedSkillActivation: true, AutomaticAdmissionLedger: true,
						OwnershipMode: grpcdriver.LearningRepositoryOwnershipTrusted,
					}))
					driverv1.RegisterAttemptRepositoryServiceServer(s, grpcdriver.NewAttemptRepositoryServer(attempts))
					driverv1.RegisterProposalRepositoryServiceServer(s, grpcdriver.NewProposalRepositoryServer(memproposal.New()))
					driverv1.RegisterSkillRepositoryServiceServer(s, grpcdriver.NewSkillRepositoryServer(memskill.New()))
					driverv1.RegisterAutomaticAdmissionLedgerServiceServer(s, grpcdriver.NewAutomaticAdmissionLedgerServer(ledger))
				})
				closer := &countingMCPProfileCloser{onClose: func() {
					if fail {
						if attempts.calls.Load() != 0 || ledger.calls.Load() != 0 {
							t.Error("failed composition launched recovery discovery")
						}
						return
					}
					for _, report := range []*startupLoopReport{diag.attempts, diag.reservations} {
						select {
						case <-report.done:
						default:
							t.Error("profile backend closed before recovery callback returned")
						}
					}
				}}
				blocked, release := make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				defer unblock()
				signerErr := errors.New("offline signer failure")
				cfg := isolateConfig(t, Config{
					Workspace: t.TempDir(), StoreDir: t.TempDir(), MockProvider: mockllm.New(),
					NoSoul: true, NoShell: true, LearningMode: learning.Auto,
					LearningAutomatic: defaultLearningAutomaticConfig(), LearningStoreURL: addr,
					Diagnostics: diag, MCPProfileLifecycle: closer, driverConns: conns,
					placementSelectorRead: func(p []byte) (int, error) {
						close(blocked)
						<-release
						if fail {
							return 0, signerErr
						}
						return len(p), nil
					},
				})
				type result struct {
					built *Built
					err   error
				}
				done := make(chan result, 1)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				go func() { built, err := Build(ctx, cfg); done <- result{built, err} }()
				defer func() {
					unblock()
					diag.attempts.unblock()
					diag.reservations.unblock()
					got := <-done
					if got.built != nil {
						got.built.Close()
					}
				}()
				waitStartupLoopSignal(t, blocked, "composition gate")
				synctest.Wait()
				if attempts.calls.Load() != 0 || ledger.calls.Load() != 0 {
					t.Fatal("recovery discovered work while composition was blocked")
				}
				unblock()
				got := <-done
				done <- got // deferred cleanup owns the returned build, including failures.
				if fail {
					if !errors.Is(got.err, signerErr) || got.built != nil {
						t.Fatalf("Build = %v, %v; want signer failure", got.built, got.err)
					}
					if attempts.calls.Load() != 0 || ledger.calls.Load() != 0 || closer.calls.Load() != 1 {
						t.Fatal("failed startup launched discovery or did not close profiles exactly once")
					}
					return
				}
				if got.err != nil {
					t.Fatal(got.err)
				}
				waitStartupLoopSignal(t, diag.attempts.entered, "immediate attempt discovery")
				waitStartupLoopSignal(t, diag.reservations.entered, "immediate reservation discovery")
				if attempts.calls.Load() != 1 || ledger.calls.Load() != 1 {
					t.Fatal("workers did not perform their immediate discovery pass")
				}
				attemptCtx, reservationCtx := <-diag.attempts.contexts, <-diag.reservations.contexts
				assertDependenciesOpen := func() {
					t.Helper()
					if got.built.Service.IsDraining() || closer.calls.Load() != 0 || conns.conns[addr].conn.GetState() == connectivity.Shutdown {
						t.Error("service, profile backend, or learning connection torn down while recovery callback was held")
					}
				}
				closed := make(chan struct{})
				go func() { got.built.Close(); close(closed) }()
				waitStartupLoopSignal(t, attemptCtx.Done(), "attempt cancellation")
				synctest.Wait()
				assertDependenciesOpen()
				if reservationCtx.Err() != nil {
					t.Error("cleanup advanced to reservations before joining the held attempt worker")
				}
				diag.attempts.unblock()
				waitStartupLoopSignal(t, reservationCtx.Done(), "reservation cancellation")
				synctest.Wait()
				assertDependenciesOpen()
				diag.reservations.unblock()
				waitStartupLoopSignal(t, closed, "joined recovery cleanup")
				if !got.built.Service.IsDraining() || conns.conns[addr].conn.GetState() != connectivity.Shutdown {
					t.Error("cleanup did not close the service and learning connection")
				}
				got.built.Close()
				if closer.calls.Load() != 1 || attempts.calls.Load() != 1 || ledger.calls.Load() != 1 {
					t.Fatal("cleanup was not idempotent or discovery continued after cancellation")
				}
			})
		})
	}
}

type startupDiscoveryAttempts struct {
	learning.AttemptRepository
	calls atomic.Int32
}

func (r *startupDiscoveryAttempts) DiscoverWork(context.Context, learning.AttemptWorkList) (learning.AttemptWorkPage, error) {
	r.calls.Add(1)
	return learning.AttemptWorkPage{}, errors.New("offline discovery failure")
}

type startupDiscoveryLedger struct {
	learning.AutomaticAdmissionLedger
	calls atomic.Int32
}

func (l *startupDiscoveryLedger) DiscoverExpired(context.Context, uint32) ([]learning.AutomaticReservation, error) {
	l.calls.Add(1)
	return nil, errors.New("offline discovery failure")
}

// Hold the actual Build-owned loops in their synchronous error callbacks, so
// cleanup must join them rather than just cancel their remote RPCs.
type startupLoopDiagnostics struct {
	port.NopDiagnostics
	attempts, reservations *startupLoopReport
}

func (d *startupLoopDiagnostics) Log(_ context.Context, _ port.Level, message string, _ ...any) {
	switch message {
	case "durable learning attempt discovery unavailable":
		d.attempts.run()
	case "durable automatic reservation reconciliation unavailable":
		d.reservations.run()
	}
}

type startupLoopReport struct {
	contexts               chan context.Context
	entered, release, done chan struct{}
	once                   sync.Once
}

func newStartupLoopReport() *startupLoopReport {
	return &startupLoopReport{contexts: make(chan context.Context, 1), entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
}

func (r *startupLoopReport) run() {
	close(r.entered)
	<-r.release
	close(r.done)
}

func (r *startupLoopReport) unblock() { r.once.Do(func() { close(r.release) }) }

// Capture the client-side loop context: the server RPC context ends at response,
// and the diagnostic callback receives Build's parent rather than the loop context.
func (d *startupLoopDiagnostics) captureContext(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	switch method {
	case driverv1.AttemptRepositoryService_DiscoverAttemptWork_FullMethodName:
		d.attempts.contexts <- ctx
	case driverv1.AutomaticAdmissionLedgerService_DiscoverExpiredAutomaticReservations_FullMethodName:
		d.reservations.contexts <- ctx
	}
	return invoke(ctx, method, req, reply, cc, opts...)
}

// Keep the real driver/composition path, but replace OS sockets with bubble-owned
// blocking so synctest.Wait can establish quiescence at startup and shutdown gates.
func startStartupLoopDriver(t *testing.T, interceptor grpc.UnaryClientInterceptor, register func(*grpc.Server)) (string, *driverConns) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	register(srv)
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Stop)
	const target = "passthrough:///startup-recovery"
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
		grpc.WithUnaryInterceptor(interceptor),
	)
	if err != nil {
		t.Fatal(err)
	}
	cached := &driverConn{conn: conn}
	t.Cleanup(cached.close)
	conns := newDriverConns()
	conns.conns[target] = cached
	return target, conns
}

func waitStartupLoopSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}
