package executioncontroller

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/internal/executorrpc/commandserver"
	"github.com/stacklok/mecatl/internal/executorrpc/memexecutor"
)

func TestUnknownCommandOutcomeFencesUntilAdminRecovery(t *testing.T) {
	pvc := map[string]any{"name": "workspace", "uid": "pvc-uid"}
	f := newProxyFixture(t, proxyOptions{
		administrator: true,
		mutate: func(env *unstructured.Unstructured) {
			_ = unstructured.SetNestedMap(env.Object, pvc, "status", "pvc")
		},
	})
	f.handler.backend.(*Store).WithKubeClient(f.kube)
	f.runner.Script("survivor", memexecutor.OutcomeUnknown(memexecutor.Exit("", "", 0)))

	result, err := runToEnd(startCommand(t.Context(), t, f.conn, proxyRequestContext(), "survivor"))
	if err != nil || result.GetState() != executorv1.CommandState_COMMAND_STATE_OUTCOME_UNKNOWN {
		t.Fatalf("RunCommand = %v, %v; want the unknown outcome", result, err)
	}
	requireFenced(t, f.dynamic, "env")
	_, err = runToEnd(startCommand(t.Context(), t, f.conn, proxyRequestContext(), "survivor"))
	if !refusedBeforeForwarding(status.Convert(err)) {
		t.Fatalf("command in a fenced environment = %v, want NOT_FORWARDED", err)
	}

	admin := executionv1.NewExecutionProviderServiceClient(f.conn)
	ref := &executionv1.EnvironmentRef{Id: "env", Revision: "rev"}
	owner := &executionv1.Owner{Issuer: scopeOwner.Issuer, Subject: scopeOwner.Subject}
	recoverEnv := func() error {
		_, err := admin.RecoverEnvironment(t.Context(), &executionv1.RecoverEnvironmentRequest{
			Environment:            ref,
			Owner:                  owner,
			ExpectedExecutionEpoch: 1,
			ExpectedPodUid:         f.pin.PodUID,
			ExpectedPvcUid:         "pvc-uid",
			OperationId:            "recover",
		})
		return err
	}
	if err := recoverEnv(); err == nil {
		t.Fatal("recovery accepted a running executor")
	}
	requireFenced(t, f.dynamic, "env")

	pod := terminalExecutor()
	pod.Name, pod.UID = f.pin.PodName, types.UID(f.pin.PodUID)
	claim := retainedPVC()
	for _, create := range []func() error{
		func() error {
			_, err := f.kube.CoreV1().Pods("ns").Create(t.Context(), pod, metav1.CreateOptions{})
			return err
		},
		func() error {
			_, err := f.kube.CoreV1().PersistentVolumeClaims("ns").Create(
				t.Context(),
				claim,
				metav1.CreateOptions{},
			)
			return err
		},
	} {
		if err := create(); err != nil {
			t.Fatal(err)
		}
	}
	if err := recoverEnv(); err != nil {
		t.Fatalf("recovery after the executor terminated: %v", err)
	}
	_, err = admin.ReplaceExecutor(t.Context(), &executionv1.ReplaceExecutorRequest{
		Environment:            ref,
		Owner:                  owner,
		ExpectedExecutionEpoch: 1,
		ExpectedPodUid:         f.pin.PodUID,
		ExpectedPvcUid:         "pvc-uid",
		OperationId:            "replace",
	})
	if err != nil {
		t.Fatalf("replacement after recovery: %v", err)
	}
	env := f.environment(t)
	if textNested(env.Object, statusField, "fenceState") != fenceHealthy ||
		textNested(env.Object, statusField, "lifecycleOperation", "id") != "replace" {
		t.Fatalf("recovered status = %v", env.Object[statusField])
	}
}

func TestCommandStreamEndFencesOnlyAfterStart(t *testing.T) {
	started := &executorv1.RunCommandResponse{
		Event: &executorv1.RunCommandResponse_Started{Started: &executorv1.CommandStarted{}},
	}
	for _, tc := range []struct {
		name   string
		run    func(commandStream) error
		fenced bool
	}{
		{
			name:   "refused before start",
			run:    func(commandStream) error { return status.Error(codes.Internal, "no start") },
			fenced: false,
		},
		{
			name: "known outcome",
			run: func(s commandStream) error {
				_ = s.Send(started)
				return s.Send(&executorv1.RunCommandResponse{
					Event: &executorv1.RunCommandResponse_Result{Result: &executorv1.CommandResult{
						State: executorv1.CommandState_COMMAND_STATE_FAILED,
					}},
				})
			},
			fenced: false,
		},
		{
			name: "ended after start",
			run: func(s commandStream) error {
				return s.Send(started)
			},
			fenced: true,
		},
		{
			name: "broken after start",
			run: func(s commandStream) error {
				_ = s.Send(started)
				return status.Error(codes.Internal, "lost")
			},
			fenced: true,
		},
		{
			name: "unrecognised outcome",
			run: func(s commandStream) error {
				result := &executorv1.CommandResult{}
				return s.Send(&executorv1.RunCommandResponse{
					Event: &executorv1.RunCommandResponse_Result{Result: result},
				})
			},
			fenced: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProxyFixture(t, proxyOptions{})
			f.connect.conn = serveCommands(t, scriptedCommands{run: tc.run})
			_, _ = runToEnd(startCommand(t.Context(), t, f.conn, proxyRequestContext(), "x"))
			fenced := textNested(f.environment(t).Object, statusField, "fenceState") == fenceUnknown
			if fenced != tc.fenced {
				t.Fatalf("fenced = %v, want %v", fenced, tc.fenced)
			}
		})
	}
}

func TestClosedCommandCallWaitsForTheOutcome(t *testing.T) {
	grace := commandCancelGrace
	commandCancelGrace = 50 * time.Millisecond
	t.Cleanup(func() { commandCancelGrace = grace })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stuck := func(_ context.Context, out, _ io.Writer) commandserver.Exit {
		_, _ = io.WriteString(out, "ready")
		<-release
		return commandserver.Exit{Confirmed: true}
	}
	for _, tc := range []struct {
		name   string
		script memexecutor.Script
		fenced bool
	}{
		{name: "cancel confirmed", script: memexecutor.Block("ready"), fenced: false},
		{name: "cancel unanswered", script: stuck, fenced: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProxyFixture(t, proxyOptions{})
			f.runner.Script("cmd", tc.script)
			ctx, cancel := context.WithCancel(t.Context())
			stream := startCommand(ctx, t, f.conn, proxyRequestContext(), "cmd")
			for range 2 {
				if _, err := stream.Recv(); err != nil {
					t.Fatal(err)
				}
			}
			cancel()
			deadline := time.Now().Add(withdrawalWait)
			for f.handler.flights.tracked() != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			fenced := textNested(f.environment(t).Object, statusField, "fenceState") == fenceUnknown
			if fenced != tc.fenced {
				t.Fatalf("fenced = %v, want %v", fenced, tc.fenced)
			}
		})
	}
}

func (r *inflightCalls) tracked() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byEnv)
}

func (f *proxyFixture) environment(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	res := f.dynamic.Resource(ExecutionEnvironmentGVR).Namespace("ns")
	env, err := res.Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func runToEnd(
	stream executorv1.CommandService_RunCommandClient,
) (*executorv1.CommandResult, error) {
	for {
		msg, err := stream.Recv()
		if err != nil || msg.GetResult() != nil {
			return msg.GetResult(), err
		}
	}
}

type scriptedCommands struct {
	executorv1.UnimplementedCommandServiceServer
	run func(commandStream) error
}

func (s scriptedCommands) RunCommand(stream commandStream) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	return s.run(stream)
}

func serveCommands(t *testing.T, srv executorv1.CommandServiceServer) *grpc.ClientConn {
	t.Helper()
	gs := grpc.NewServer()
	executorv1.RegisterCommandServiceServer(gs, srv)
	lis := bufconn.Listen(1 << 20)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient(
		"passthrough:///commands",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
