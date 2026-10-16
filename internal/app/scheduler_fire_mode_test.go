package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
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
