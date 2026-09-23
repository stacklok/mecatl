package boatenv

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/app"
)

// composedRun drives one scripted session through the real app.Build
// composition with provider as the deployment PlacementProvider and returns the
// session's durable environment ref plus every tool result.
func composedRun(t *testing.T, provider *Provider, cfg app.Config, calls ...session.ToolCall) (session.EnvironmentRef, map[session.ToolCallID]session.ToolResult) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	turns := make([]mockllm.Turn, 0, len(calls)+1)
	for _, call := range calls {
		turns = append(turns, mockllm.ToolCallTurn(call))
	}
	cfg.MockProvider = mockllm.New(append(turns, mockllm.TextTurn("done"))...)
	cfg.PlacementProvider = provider
	cfg.PlacementScope = provider.scope
	cfg.NoSoul = true
	cfg.UserModelDir = t.TempDir()
	cfg.StoreDir = t.TempDir()
	built, err := app.Build(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sess, err := built.Service.CreateSession(ctx, session.ModeAccept, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "exercise the Boat sandbox")
	if err != nil {
		t.Fatal(err)
	}
	results := map[session.ToolCallID]session.ToolResult{}
	for ev := range run.Events() {
		if ev.Ask != nil {
			if _, err := built.Service.ApproveRun(ctx, sess.ID, ev.Ask.AskID, session.VerdictAllowOnce, ""); err != nil {
				t.Fatal(err)
			}
		}
		if ev.ToolResult != nil {
			results[ev.ToolResult.CallID] = *ev.ToolResult
		}
	}
	built.Service.FinishRun(sess.ID, run)
	got, err := built.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got.EnvironmentRef, results
}

func assertToolResults(t *testing.T, results map[session.ToolCallID]session.ToolResult, want map[session.ToolCallID]string) {
	t.Helper()
	for id, content := range want {
		result, ok := results[id]
		if !ok || result.IsError || !strings.Contains(result.Content, content) {
			t.Errorf("%s result = %+v (present=%t), want success containing %q", id, result, ok, content)
		}
	}
}

func fakeProvider(t *testing.T, fake *fakeBoatAPI) *Provider {
	t.Helper()
	provider, err := New(Config{
		APIKey: "test-key", BaseURL: fake.server.URL, HTTPClient: fake.server.Client(),
		Scope: "composition", TTLSeconds: 60, ReadyTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.client.poll = time.Millisecond
	return provider
}

func (f *fakeBoatAPI) root(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if sandbox := f.sandboxes[id]; sandbox != nil {
		return sandbox.root
	}
	return ""
}

func call(id, name, args string) session.ToolCall {
	return session.NewToolCall(session.ToolCallID(id), name, json.RawMessage(args))
}

// TestAppBuildComposesBoatPlacement is the remote-only deployment shape: no
// local workspace at all. The engine's file tools must pass authority
// evaluation, land on the sandbox's disk, and cost exactly one sandbox.
func TestAppBuildComposesBoatPlacement(t *testing.T) {
	requireLocalHelper(t)
	fake := newFakeBoatAPI(t)
	ref, results := composedRun(t, fakeProvider(t, fake), app.Config{},
		call("write", "Write", `{"path":"notes/composed.txt","content":"alpha\nneedle\n"}`),
		call("read", "Read", `{"path":"notes/composed.txt"}`),
		call("edit", "Edit", `{"path":"notes/composed.txt","old_string":"alpha","new_string":"omega"}`),
		call("grep", "Grep", `{"pattern":"needle"}`),
		call("glob", "Glob", `{"pattern":"**/*.txt"}`),
	)
	assertToolResults(t, results, map[session.ToolCallID]string{
		"write": "", "read": "needle", "edit": "", "grep": "notes/composed.txt", "glob": "notes/composed.txt",
	})
	// Startup preflight goes through ValidatePlacement and never provisions.
	if ref.Kind != Kind || fake.createCount() != 1 {
		t.Fatalf("session environment = %+v after %d creates, want exactly one %q sandbox", ref, fake.createCount(), Kind)
	}
	if data, err := os.ReadFile(filepath.Join(fake.root(ref.ID), "notes", "composed.txt")); err != nil || string(data) != "omega\nneedle\n" {
		t.Fatalf("sandbox disk content = %q, %v", data, err)
	}
}

// TestAppBuildRoutesShellToBoatSandbox proves the engine's Shell runs through
// the bound Boat CommandRunner. On main the Shell catalog entry still requires
// a local workspace and shell even when the placement supplies the runner;
// #1614's RemoteExecution posture removes that requirement. The local
// workspace here exists only to switch Shell on, and the test proves nothing
// runs in it.
func TestAppBuildRoutesShellToBoatSandbox(t *testing.T) {
	requireLocalHelper(t)
	fake := newFakeBoatAPI(t)
	host := t.TempDir()
	ref, results := composedRun(t, fakeProvider(t, fake), app.Config{Workspace: host, Shell: "/bin/sh"},
		call("write", "Write", `{"path":"composed.txt","content":"composed-through-app-build\n"}`),
		call("shell", "Shell", `{"command":"cat composed.txt && printf from-shell > shell.txt"}`),
		call("read", "Read", `{"path":"shell.txt"}`),
	)
	assertToolResults(t, results, map[session.ToolCallID]string{
		"write": "", "shell": "composed-through-app-build", "read": "from-shell",
	})
	if ref.Kind != Kind || fake.createCount() != 1 {
		t.Fatalf("session environment = %+v after %d creates, want exactly one %q sandbox", ref, fake.createCount(), Kind)
	}
	if data, err := os.ReadFile(filepath.Join(fake.root(ref.ID), "shell.txt")); err != nil || string(data) != "from-shell" {
		t.Fatalf("sandbox shell.txt = %q, %v", data, err)
	}
	for _, name := range []string{"composed.txt", "shell.txt"} {
		if _, err := os.Stat(filepath.Join(host, name)); !os.IsNotExist(err) {
			t.Fatalf("%s reached the host workspace: %v", name, err)
		}
	}
}
