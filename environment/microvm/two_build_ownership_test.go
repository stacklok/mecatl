package microvm

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/control/controltest"
	microvmadapter "github.com/stacklok/mecatl/internal/adapter/microvm"
	"github.com/stacklok/mecatl/internal/adapter/server"
	app "github.com/stacklok/mecatl/internal/app"
)

type twoBuildDaemon struct {
	endpoint string
	cancel   context.CancelFunc
	done     chan error
}

func startTwoBuildDaemon(t *testing.T, fixture *repositoryAttachmentFixture) twoBuildDaemon {
	t.Helper()
	socketDir := filepath.Join(filepath.Dir(fixture.stateRoot), "socket")
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(socketDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	fdRoot := "/proc/self/fd"
	if runtime.GOOS == "darwin" {
		fdRoot = "/dev/fd"
	}
	socket := filepath.Join(fdRoot, fmt.Sprint(dir.Fd()), "ownership.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := control.NewService(control.ServiceConfig{AccountUID: uint32(os.Getuid()), PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: uint32(os.Getuid())}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntimeDaemon(RuntimeDaemonConfig{
		Control:    auth,
		Info:       DaemonInfo{ProtocolVersion: LifecycleProtocolVersion, ReleaseIdentity: "test", BinaryIdentity: "test", ConfigDigest: "test", PolicyRevision: "test", Profiles: []string{"microvm-local"}, Socket: socket},
		Repository: fixture.composition,
		RepositoryProvisioner: func(_ context.Context, request ProvisionRequest) (RepositoryPlacement, error) {
			return RepositoryPlacement{
				Request: LogicalEnvironmentRequest{Owner: request.Owner, Checkout: request.SourceCheckout, Artifacts: testArtifactSnapshot(fixture.verified)},
				Status:  EnforcedProfileStatus{Profile: request.Profile, GuestEgress: "deny-all", HostEgress: "not constrained"},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runtime.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("production microvmd did not stop")
		}
	})
	return twoBuildDaemon{endpoint: "unix://" + socket, cancel: cancel, done: done}
}

func buildAgainstProductionDaemon(t *testing.T, endpoint, checkout, storeDir string, turns ...mockllm.Turn) (*app.Built, *microvmadapter.Client) {
	t.Helper()
	const scope server.PlacementScope = "deployment"
	provider, err := microvmadapter.NewPlacementProvider(endpoint, checkout, "microvm-local", scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	built, err := app.Build(t.Context(), app.Config{
		Workspace: checkout, StoreDir: storeDir, UserModelDir: t.TempDir(), MemoryDir: t.TempDir(),
		NoSoul: true, UseMock: true, MockProvider: mockllm.New(turns...),
		PlacementProvider: provider, PlacementScope: scope,
		EnvironmentForkers: map[session.EnvironmentKind]tool.EnvironmentForker{session.EnvironmentKind("microvm"): provider},
		EnvironmentMergers: map[session.EnvironmentKind]tool.EnvironmentMerger{session.EnvironmentKind("microvm"): provider},
		SchedulerEnabled:   true, SchedulerTickInterval: time.Hour, SessionLeaseTTL: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return built, provider
}

func TestMicroVMTwoBuildsSeparatePlacementsSurvivePeerClose(t *testing.T) {
	for _, linked := range []bool{false, true} {
		name := "same-checkout"
		if linked {
			name = "linked-worktree"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newRepositoryAttachmentFixture(t)
			daemon := startTwoBuildDaemon(t, fixture)
			checkoutB := fixture.repository
			if linked {
				checkoutB = filepath.Join(filepath.Dir(fixture.repository), "linked")
				if _, err := gitexecForLogicalTest(t.Context(), fixture.repository, "worktree", "add", checkoutB); err != nil {
					t.Fatal(err)
				}
			}
			first, _ := buildAgainstProductionDaemon(t, daemon.endpoint, fixture.repository, t.TempDir(), mockllm.TextTurn("first"))
			second, secondProvider := buildAgainstProductionDaemon(t, daemon.endpoint, checkoutB, t.TempDir(), mockllm.TextTurn("second"))
			defer second.Close()
			one, err := first.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			two, err := second.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if one.EnvironmentRef == two.EnvironmentRef {
				t.Fatal("independent Builds shared one logical ref")
			}
			borrow, err := secondProvider.Reattach(t.Context(), server.PlacementReattachRequest{Ref: two.EnvironmentRef, Scope: "deployment"})
			if err != nil {
				t.Fatal(err)
			}
			defer borrow.Close()
			if _, err := borrow.Environment.CommandRunner().Run(t.Context(), "printf build-b > ownership-marker"); err != nil {
				t.Fatal(err)
			}
			first.Service.CloseSession(one.ID)
			first.Close()
			if got, err := borrow.Environment.Workspace().Read(t.Context(), "ownership-marker"); err != nil || string(got) != "build-b" {
				t.Fatalf("peer close revoked surviving workspace: %q, %v", got, err)
			}
			if result, err := borrow.Environment.CommandRunner().Run(t.Context(), "cat ownership-marker"); err != nil || result.Stdout != "build-b" {
				t.Fatalf("peer close revoked surviving runner: %+v, %v", result, err)
			}
		})
	}
}

func TestMicroVMCrossBuildScheduleClosePreservesOrigin(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	daemon := startTwoBuildDaemon(t, fixture)
	storeDir := t.TempDir()
	leader, _ := buildAgainstProductionDaemon(t, daemon.endpoint, fixture.repository, storeDir, mockllm.TextTurn("fired"))
	originBuild, originProvider := buildAgainstProductionDaemon(t, daemon.endpoint, fixture.repository, storeDir, mockllm.TextTurn("origin survives"))
	defer originBuild.Close()
	origin, err := originBuild.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := originBuild.Service.CreateSchedule(t.Context(), port.ScheduleSpec{Name: "cross-build", Prompt: "read only", Trigger: port.TriggerSpec{Cron: "0 0 * * *"}, OriginSessionID: origin.ID, Mutating: true})
	if err != nil {
		t.Fatal(err)
	}
	if schedule.Spec.EnvironmentRef != origin.EnvironmentRef || schedule.Spec.PlacementOwned {
		t.Fatalf("origin-backed schedule placement = %+v", schedule.Spec)
	}
	fire, err := leader.Service.FireNow(t.Context(), schedule.Spec.Name)
	if err != nil || fire.SessionID == "" {
		t.Fatalf("leader fire = %+v, %v", fire, err)
	}
	leader.Close()
	borrow, err := originProvider.Reattach(t.Context(), server.PlacementReattachRequest{Ref: origin.EnvironmentRef, Scope: "deployment"})
	if err != nil {
		t.Fatal(err)
	}
	defer borrow.Close()
	if _, err := borrow.Environment.CommandRunner().Run(t.Context(), "printf origin-alive > schedule-marker"); err != nil {
		t.Fatal(err)
	}
	if got, err := borrow.Environment.Workspace().Read(t.Context(), "schedule-marker"); err != nil || string(got) != "origin-alive" {
		t.Fatalf("fire cleanup revoked origin: %q, %v", got, err)
	}
}
