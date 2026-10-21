package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestResumedAutoSubagentRefusedAfterCheckerRemoved proves Build hands the
// server's mode admission to the Subagent tool. A yolo-ceiling deployment with
// an enforcing checker runs an auto session whose child inherits and persists
// auto. After a restart over the same store without a checker, the parent is
// switched to yolo (auto is no longer admitted) and resumes the child: the
// child would keep auto, so the resume is refused with the server's reason,
// the child never calls the model, and the parent run completes. Both Subagent
// builders carry the admission, so it runs for a filesystem and a no-FS parent.
func TestResumedAutoSubagentRefusedAfterCheckerRemoved(t *testing.T) {
	for name, profile := range map[string]server.SessionProfile{"filesystem": server.ProfileDefault, "no-fs": server.ProfileNoFS} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			storeDir := t.TempDir()
			memoryDir := t.TempDir()
			workspace := t.TempDir()
			baseCfg := func(llm *mockllm.Provider) Config {
				return Config{
					Workspace:           workspace,
					NoSoul:              true,
					StoreDir:            storeDir,
					MemoryDir:           memoryDir,
					Posture:             PostureYolo,
					PostureFlagSet:      true,
					GuardrailsDisabled:  true,
					envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
					liveModelHTTPClient: offlineHTTPClient(),
					providerConstructor: func(Config, string, string, string) port.LLMProvider { return llm },
				}
			}

			// Build #1: an enforcing checker admits auto; the child inherits auto.
			first := mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"investigate"}`))),
				mockllm.TextTurn("FIRST_PASS"),
				mockllm.TextTurn("parent done"),
			)
			built1, err := buildIsolated(t, ctx, withInertEnforcingChecker(baseCfg(first)))
			if err != nil {
				t.Fatalf("Build #1: %v", err)
			}
			sess, err := built1.Service.CreateSessionWithProfile(ctx, session.ModeAuto, session.Limits{}, server.ProviderSelector{}, profile)
			if err != nil {
				built1.Close()
				t.Fatalf("CreateSession(auto): %v", err)
			}
			run1, err := built1.Service.StartRun(ctx, sess.ID, "delegate it")
			if err != nil {
				built1.Close()
				t.Fatalf("StartRun #1: %v", err)
			}
			var childID string
			for ev := range run1.Events() {
				if ev.Type == session.EvSubagentStart && ev.Subagent != nil {
					childID = ev.Subagent.ChildID
				}
			}
			built1.Close()
			if childID == "" {
				t.Fatal("the first run never started a subagent")
			}

			// Build #2: same store, no checker, so auto is no longer admitted.
			second := mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("p2", "Subagent",
					json.RawMessage(`{"prompt":"again","resume":"`+childID+`"}`))),
				mockllm.TextTurn("parent done again"),
			)
			built2, err := buildIsolated(t, ctx, baseCfg(second))
			if err != nil {
				t.Fatalf("Build #2: %v", err)
			}
			defer built2.Close()
			if _, err := built2.Service.SetMode(ctx, sess.ID, session.ModeYolo); err != nil {
				t.Fatalf("SetMode(yolo): %v", err)
			}
			run2, err := built2.Service.StartRun(ctx, sess.ID, "resume it")
			if err != nil {
				t.Fatalf("StartRun #2: %v", err)
			}
			var resumed *session.ToolResult
			var terminal session.StopReason
			for ev := range run2.Events() {
				if ev.ToolResult != nil && ev.ToolResult.CallID == "p2" {
					resumed = ev.ToolResult
				}
				if ev.Type == session.EvSubagentStart {
					t.Errorf("the refused child started: %+v", ev.Subagent)
				}
				if ev.Result != nil {
					terminal = ev.Result.Stop
				}
			}
			if resumed == nil || !resumed.IsError {
				t.Fatalf("resume result = %+v, want a tool error", resumed)
			}
			for _, want := range []string{`permission mode "auto"`, "does not admit", "requires an enforcing guardrails checker", "Start a new Subagent"} {
				if !strings.Contains(resumed.Content, want) {
					t.Errorf("resume error %q does not contain %q", resumed.Content, want)
				}
			}
			if terminal != session.StopEndTurn {
				t.Errorf("parent run stop = %q, want %q", terminal, session.StopEndTurn)
			}
			if n := second.Calls(); n != 2 {
				t.Errorf("provider calls = %d, want 2 (parent only; the refused child must not call the model)", n)
			}
		})
	}
}
