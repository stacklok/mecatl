package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

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
			diag := &startupLoopDiagnostics{
				attempts: newStartupLoopReport(), reservations: newStartupLoopReport(),
			}
			defer diag.attempts.unblock()
			defer diag.reservations.unblock()
			attempts := &startupDiscoveryAttempts{AttemptRepository: memattempt.New(wallclock.Clock{})}
			ledger := &startupDiscoveryLedger{AutomaticAdmissionLedger: automaticStoreForTest(t, t.TempDir(), defaultLearningAutomaticConfig())}
			addr := startSourceDriver(t, func(s *grpc.Server) {
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
						t.Error("profile backend closed before recovery worker joined")
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
				Diagnostics: diag, MCPProfileLifecycle: closer,
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
			closed := make(chan struct{})
			go func() { got.built.Close(); close(closed) }()
			diag.attempts.unblock()
			waitStartupLoopSignal(t, diag.attempts.done, "attempt report completion")
			if closer.calls.Load() != 0 {
				t.Fatal("backend closed while reservation reconciliation was still running")
			}
			diag.reservations.unblock()
			waitStartupLoopSignal(t, closed, "joined recovery cleanup")
			got.built.Close()
			if closer.calls.Load() != 1 || attempts.calls.Load() != 1 || ledger.calls.Load() != 1 {
				t.Fatal("cleanup was not idempotent or discovery continued after cancellation")
			}
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
	entered, release, done chan struct{}
	once                   sync.Once
}

func newStartupLoopReport() *startupLoopReport {
	return &startupLoopReport{entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
}

func (r *startupLoopReport) run() {
	close(r.entered)
	<-r.release
	close(r.done)
}

func (r *startupLoopReport) unblock() { r.once.Do(func() { close(r.release) }) }

func waitStartupLoopSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}
