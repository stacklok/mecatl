package app

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// scheduleModeFireTimeout bounds each fire so a fire that blocks on an
// unanswerable ask fails the test instead of hanging it.
const scheduleModeFireTimeout = 20 * time.Second

// TestScheduledFireRunsInCreatingSessionMode is the regression for a mutating
// in-chat schedule that always recorded default mode: on a yolo server the
// fire then ran in default, its Write asked with no approver, and the fire
// hung until its deadline. A schedule created from a yolo session must fire in
// yolo and run its mutation without an ask. Once the same store is served by a
// deployment whose ceiling no longer admits yolo, the recorded mode is refused
// at fire-session admission: the fire fails fast and nothing runs.
func TestScheduledFireRunsInCreatingSessionMode(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	storeDir := t.TempDir()
	written := filepath.Join(workspace, "fired.txt")
	writeCall := func(id session.ToolCallID, path string) mockllm.Turn {
		args, err := json.Marshal(map[string]string{"path": path, "content": "fired\n"})
		if err != nil {
			t.Fatal(err)
		}
		return mockllm.ToolCallTurn(session.NewToolCall(id, "Write", args))
	}
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("create", agent.ScheduleToolName,
			json.RawMessage(`{"verb":"create","name":"writer","prompt":"write the file","cron":"@every 1h","mutating":true}`))),
		mockllm.TextTurn("scheduled"),
		writeCall("fire-write", written),
		mockllm.TextTurn("fire done"),
	)
	built, err := buildIsolated(t, ctx, Config{
		Workspace: workspace, StoreDir: storeDir, NoSoul: true, UseMock: true, MockProvider: provider,
		Model: "main-model", PermissionMode: "yolo", PermissionModeFlagSet: true,
	})
	if err != nil {
		t.Fatalf("Build(yolo): %v", err)
	}
	creator, err := built.Service.CreateSession(ctx, session.ModeYolo, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession(yolo): %v", err)
	}
	run, err := built.Service.StartRun(ctx, creator.ID, "schedule a writer")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.IsError {
			t.Fatalf("in-chat create failed: %q", ev.ToolResult.Content)
		}
	}
	built.Service.FinishRun(creator.ID, run)

	jstore, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	sched, err := jstore.ScheduleStore().Load(ctx, "writer")
	if err != nil {
		t.Fatalf("Load(writer): %v", err)
	}
	if sched.Spec.Mode != session.ModeYolo || !sched.Spec.Mutating {
		t.Fatalf("recorded (mode %q, mutating %t), want (yolo, true): the creating session's mode", sched.Spec.Mode, sched.Spec.Mutating)
	}

	fire, err := makeFireFunc(built.Service, jstore.ScheduleStore(), scheduleModeFireTimeout, nil)(ctx, sched, time.Now())
	if err != nil || fire.Stop != session.StopEndTurn {
		t.Fatalf("yolo fire = (stop %q, err %v, %q), want end_turn", fire.Stop, err, fire.Err)
	}
	fired, err := built.Service.GetSession(ctx, fire.SessionID)
	if err != nil {
		t.Fatalf("GetSession(fire): %v", err)
	}
	if fired.Mode != session.ModeYolo {
		t.Fatalf("fire session mode = %q, want yolo", fired.Mode)
	}
	if got, err := os.ReadFile(written); err != nil || string(got) != "fired\n" {
		t.Fatalf("fire Write did not run unasked: content %q, err %v", got, err)
	}
	built.Close()

	// The same store under a deployment whose ceiling is accept-edits: the
	// yolo-recorded schedule must be refused, not escalated or downgraded.
	refusedPath := filepath.Join(workspace, "refused.txt")
	tightProvider := mockllm.New(writeCall("refused-write", refusedPath), mockllm.TextTurn("should not run"))
	tight, err := buildIsolated(t, ctx, Config{
		Workspace: workspace, StoreDir: storeDir, NoSoul: true, UseMock: true, MockProvider: tightProvider,
		Model: "main-model", PermissionMode: "default", PermissionModeFlagSet: true,
	})
	if err != nil {
		t.Fatalf("Build(default): %v", err)
	}
	defer tight.Close()
	tightStore, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	sched, err = tightStore.ScheduleStore().Load(ctx, "writer")
	if err != nil {
		t.Fatalf("Load(writer) after relaunch: %v", err)
	}
	start := time.Now()
	refused, err := makeFireFunc(tight.Service, tightStore.ScheduleStore(), scheduleModeFireTimeout, nil)(ctx, sched, time.Now())
	if err == nil || refused.Stop != session.StopError {
		t.Fatalf("over-ceiling fire = (stop %q, err %v), want a refused fire", refused.Stop, err)
	}
	if elapsed := time.Since(start); elapsed >= scheduleModeFireTimeout {
		t.Fatalf("over-ceiling fire took %s, want refused at admission, not at the deadline", elapsed)
	}
	if tightProvider.Calls() != 0 {
		t.Fatalf("over-ceiling fire reached the model %d times, want 0", tightProvider.Calls())
	}
	if _, err := os.Stat(refusedPath); !os.IsNotExist(err) {
		t.Fatalf("over-ceiling fire wrote %s (stat err %v), want nothing run", refusedPath, err)
	}
}

// dialScheduleService serves the real ScheduleService handler for built over an
// in-memory listener, so a test drives the wire mapping a remote client uses.
func dialScheduleService(t *testing.T, built *Built) mecatlv1.ScheduleServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	mecatlv1.RegisterScheduleServiceServer(gs, server.NewScheduleServer(built.Service))
	go func() { _ = gs.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		gs.Stop()
		_ = lis.Close()
	})
	return mecatlv1.NewScheduleServiceClient(conn)
}

// TestGRPCScheduleUnspecifiedModeUsesServerDefault pins the wire half of the
// same hang: a gRPC CreateSchedule that left mode UNSPECIFIED was stored as
// default, so on a yolo server its fire asked with no approver. UNSPECIFIED
// now stays unset, reads back as UNSPECIFIED, and the fire runs in the
// server's default session mode. An explicit DEFAULT is still stored and read
// back as default, and a read-leaning schedule still has to name PLAN.
func TestGRPCScheduleUnspecifiedModeUsesServerDefault(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	storeDir := t.TempDir()
	written := filepath.Join(workspace, "wire-fired.txt")
	args, err := json.Marshal(map[string]string{"path": written, "content": "fired\n"})
	if err != nil {
		t.Fatal(err)
	}
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("fire-write", "Write", args)),
		mockllm.TextTurn("fire done"),
	)
	built, err := buildIsolated(t, ctx, Config{
		Workspace: workspace, StoreDir: storeDir, NoSoul: true, UseMock: true, MockProvider: provider,
		Model: "main-model", PermissionMode: "yolo", PermissionModeFlagSet: true,
	})
	if err != nil {
		t.Fatalf("Build(yolo): %v", err)
	}
	defer built.Close()
	client := dialScheduleService(t, built)
	spec := func(name string, mutating bool, mode mecatlv1.PermissionMode) *mecatlv1.ScheduleSpec {
		return &mecatlv1.ScheduleSpec{
			Name: name, Prompt: "write the file", Mutating: mutating, Mode: mode,
			Trigger: &mecatlv1.TriggerSpec{Cron: "@every 1h"},
		}
	}

	unset, err := client.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{Spec: spec("unset", true, mecatlv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED)})
	if err != nil {
		t.Fatalf("CreateSchedule(unset): %v", err)
	}
	if got := unset.GetSchedule().GetSpec().GetMode(); got != mecatlv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED {
		t.Fatalf("unset create read back mode %s, want UNSPECIFIED", got)
	}
	explicit, err := client.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{Spec: spec("explicit", true, mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT)})
	if err != nil {
		t.Fatalf("CreateSchedule(explicit): %v", err)
	}
	if got := explicit.GetSchedule().GetSpec().GetMode(); got != mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT {
		t.Fatalf("explicit create read back mode %s, want DEFAULT", got)
	}
	_, err = client.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{Spec: spec("ro-unset", false, mecatlv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED)})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("read-leaning UNSPECIFIED create = %v, want InvalidArgument (a read-leaning schedule must name plan)", err)
	}
	if _, err := client.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{Spec: spec("ro-plan", false, mecatlv1.PermissionMode_PERMISSION_MODE_PLAN)}); err != nil {
		t.Fatalf("read-leaning PLAN create: %v", err)
	}

	jstore, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	stored, err := jstore.ScheduleStore().Load(ctx, "explicit")
	if err != nil || stored.Spec.Mode != session.ModeDefault {
		t.Fatalf("explicit stored mode = %q (err %v), want default", stored.Spec.Mode, err)
	}
	sched, err := jstore.ScheduleStore().Load(ctx, "unset")
	if err != nil {
		t.Fatalf("Load(unset): %v", err)
	}
	if sched.Spec.Mode != "" {
		t.Fatalf("unset stored mode = %q, want empty (the server default at fire)", sched.Spec.Mode)
	}
	if got, err := client.GetSchedule(ctx, &mecatlv1.GetScheduleRequest{Name: "unset"}); err != nil ||
		got.GetSchedule().GetSpec().GetMode() != mecatlv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED {
		t.Fatalf("GetSchedule(unset) = (%v, %v), want mode UNSPECIFIED", got.GetSchedule().GetSpec().GetMode(), err)
	}

	fire, err := makeFireFunc(built.Service, jstore.ScheduleStore(), scheduleModeFireTimeout, nil)(ctx, sched, time.Now())
	if err != nil || fire.Stop != session.StopEndTurn {
		t.Fatalf("unset-mode fire = (stop %q, err %v, %q), want end_turn", fire.Stop, err, fire.Err)
	}
	fired, err := built.Service.GetSession(ctx, fire.SessionID)
	if err != nil {
		t.Fatalf("GetSession(fire): %v", err)
	}
	if fired.Mode != session.ModeYolo {
		t.Fatalf("fire session mode = %q, want the server default yolo", fired.Mode)
	}
	if got, err := os.ReadFile(written); err != nil || string(got) != "fired\n" {
		t.Fatalf("fire Write did not run unasked: content %q, err %v", got, err)
	}
}
