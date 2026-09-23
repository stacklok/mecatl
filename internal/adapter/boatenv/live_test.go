//go:build e2e

package boatenv

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/app"
)

// TestLiveBoat is an opt-in contract smoke against the public boat.dev API. It
// never logs or persists BOAT_API_KEY. Ordinary CI skips it.
func TestLiveBoat(t *testing.T) {
	apiKey := os.Getenv("BOAT_API_KEY")
	if apiKey == "" {
		t.Skip("set BOAT_API_KEY to run the live Boat contract smoke")
	}
	provider, err := New(Config{
		APIKey:       apiKey,
		Scope:        "live-test",
		MachineType:  "small",
		Workdir:      "mecatl-live",
		TTLSeconds:   900,
		ReadyTimeout: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	binding, err := provider.Bind(ctx, server.PlacementBindRequest{
		Selector: server.DefaultPlacement(), Scope: "live-test", Operation: server.PlacementOperationCreate,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.client.stopSandbox(context.Background(), binding.Ref.ID) })
	if binding.Ref.Kind != Kind || binding.Ref.ID == "" {
		t.Fatalf("ref = %+v", binding.Ref)
	}
	t.Logf("live sandbox bound: kind=%s revision=%s", binding.Ref.Kind, binding.Ref.Revision)

	ws := binding.Environment.Workspace()
	runner := binding.Environment.CommandRunner()
	bound, ok := runner.(interface{ BoundWorkspaceRoot() string })
	if !ok || bound.BoundWorkspaceRoot() != ws.Root() {
		t.Fatalf("runner/workspace affinity mismatch: runner=%v workspace=%q", runner, ws.Root())
	}

	v1, err := ws.CreateFile(ctx, "mecatl-boat-live.txt", []byte("boat-provider-ok\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, version, err := ws.ReadVersion(ctx, "mecatl-boat-live.txt")
	if err != nil || string(got) != "boat-provider-ok\n" || !v1.Equal(version) {
		t.Fatalf("read after write = %q, versionEqual=%v, err=%v", got, v1.Equal(version), err)
	}
	if _, err := ws.ReplaceFile(ctx, "mecatl-boat-live.txt", tool.NewFileVersion("stale"), []byte("bad")); err == nil {
		t.Fatal("stale conditional replace succeeded on the live sandbox")
	}
	if _, err := ws.ReplaceFile(ctx, "mecatl-boat-live.txt", version, []byte("boat-provider-ok\nneedle\n")); err != nil {
		t.Fatal(err)
	}
	matches, err := ws.Grep(ctx, "needle", "*.txt")
	if err != nil || len(matches) != 1 {
		t.Fatalf("grep = %+v, err=%v", matches, err)
	}

	result, err := runner.Run(ctx, "printf shell-ok > shell.txt; cat mecatl-boat-live.txt | tail -1")
	if err != nil || result.ExitCode != 0 || result.Stdout != "needle\n" {
		t.Fatalf("shell result = %+v, %v", result, err)
	}
	if shell, err := ws.Read(ctx, "shell.txt"); err != nil || string(shell) != "shell-ok" {
		t.Fatalf("shell/workspace namespace disagreement = %q, %v", shell, err)
	}

	// Archive the provisional sandbox, then prove the persisted ref resumes the
	// exact same sandbox with its filesystem intact.
	if binding.Close == nil {
		t.Fatal("binding has no provisional close")
	}
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	// Close only schedules archival; archive now so the reattach below proves
	// the resume path.
	if err := provider.client.stopSandbox(ctx, binding.Ref.ID); err != nil {
		t.Fatal(err)
	}

	rebound, err := provider.Reattach(ctx, server.PlacementReattachRequest{Ref: binding.Ref, Scope: "live-test"})
	if err != nil {
		t.Fatal(err)
	}
	if rebound.Ref != binding.Ref {
		t.Fatalf("reattach returned a different sandbox: %+v vs %+v", rebound.Ref, binding.Ref)
	}
	if survived, err := rebound.Environment.Workspace().Read(ctx, "shell.txt"); err != nil || string(survived) != "shell-ok" {
		t.Fatalf("content after archive/resume = %q, %v", survived, err)
	}

	checkReviewFixesLive(ctx, t, rebound)

	stale := binding.Ref
	stale.Revision = "boat-v1-stale"
	if _, err := provider.Reattach(ctx, server.PlacementReattachRequest{Ref: stale, Scope: "live-test"}); err == nil {
		t.Fatal("stale revision reattached to a live sandbox")
	}
}

// TestLiveBoatConformance runs the shared Workspace and WorkspaceNamespace
// conformance tables against one live sandbox. Every subtest gets its own
// working directory inside it, so the tables stay isolated without paying for
// a sandbox per case.
func TestLiveBoatConformance(t *testing.T) {
	apiKey := os.Getenv("BOAT_API_KEY")
	if apiKey == "" {
		t.Skip("set BOAT_API_KEY to run the live Boat conformance tables")
	}
	provider, err := New(Config{APIKey: apiKey, Scope: "live-conformance", MachineType: "small", Workdir: "mecatl-conformance", TTLSeconds: 1800, ReadyTimeout: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	binding, err := provider.Bind(ctx, server.PlacementBindRequest{
		Selector: server.DefaultPlacement(), Scope: "live-conformance", Operation: server.PlacementOperationCreate,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := provider.client.stopSandbox(context.Background(), binding.Ref.ID); err != nil {
			t.Errorf("archive live sandbox %s: %v", binding.Ref.ID, err)
		}
	})
	var next int
	factory := func(t *testing.T) tool.Workspace {
		t.Helper()
		next++
		// One sandbox, one working directory per case: the handle creates the
		// directory on first use, exactly as a non-default Config.Workdir does.
		handle := newSandbox(provider, binding.Ref.ID, false)
		handle.workdir = fmt.Sprintf("mecatl-conformance/%03d", next)
		return &workspace{sandbox: handle, lockDir: provider.lockDir}
	}
	t.Run("workspace", func(t *testing.T) { fsconformance.Run(t, factory) })
	t.Run("namespace", func(t *testing.T) { fsconformance.RunNamespace(t, factory) })
}

// TestLiveBoatComposition drives a scripted session through the real
// app.Build composition against the live API: startup validation, one
// provisioned sandbox, authority-checked file tools and Shell all hitting it.
func TestLiveBoatComposition(t *testing.T) {
	apiKey := os.Getenv("BOAT_API_KEY")
	if apiKey == "" {
		t.Skip("set BOAT_API_KEY to run the live Boat composition smoke")
	}
	provider, err := New(Config{APIKey: apiKey, Scope: "live-composition", MachineType: "small", Workdir: "mecatl-composition", TTLSeconds: 900, ReadyTimeout: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	host := t.TempDir()
	_, results := composedRun(t, provider, app.Config{Workspace: host, Shell: "/bin/sh"},
		call("write", "Write", `{"path":"notes/composed.txt","content":"alpha\nneedle\n"}`),
		call("edit", "Edit", `{"path":"notes/composed.txt","old_string":"alpha","new_string":"omega"}`),
		call("grep", "Grep", `{"pattern":"needle"}`),
		call("shell", "Shell", `{"command":"cat notes/composed.txt && uname -s && printf from-shell > shell.txt"}`),
		call("read", "Read", `{"path":"shell.txt"}`),
	)
	assertToolResults(t, results, map[session.ToolCallID]string{
		"write": "", "edit": "", "grep": "notes/composed.txt", "shell": "omega\nneedle\nLinux", "read": "from-shell",
	})
	for _, name := range []string{"notes/composed.txt", "shell.txt"} {
		if _, err := os.Stat(filepath.Join(host, name)); !os.IsNotExist(err) {
			t.Fatalf("%s reached the host workspace: %v", name, err)
		}
	}
}

// checkReviewFixesLive proves the review fixes against the real service: a
// command longer than the old 60s cap, a signal-killed command, a file larger
// than one command line and one files-API write, symlink-safe Remove, brace
// globs, and RE2 POSIX classes in Grep.
func checkReviewFixesLive(ctx context.Context, t *testing.T, binding server.PlacementBinding) {
	t.Helper()
	ws := binding.Environment.Workspace()
	runner := binding.Environment.CommandRunner()

	long, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	if res, err := runner.Run(long, "sleep 70; echo survived"); err != nil || res.ExitCode != 0 || res.Stdout != "survived\n" {
		t.Fatalf("70s command under a 150s deadline = %+v, %v", res, err)
	}
	if res, err := runner.Run(ctx, "kill -9 $$"); err != nil || res.ExitCode != -1 || !strings.Contains(res.Stderr, "SIGKILL") {
		t.Fatalf("signal-killed command = %+v, %v", res, err)
	}

	data := make([]byte, 6<<20+17)
	for i := range data {
		data[i] = byte(i*31 + i/7)
	}
	v1, err := ws.CreateFile(ctx, "big/blob.bin", data)
	if err != nil {
		t.Fatalf("CreateFile(6 MiB): %v", err)
	}
	got, v2, err := ws.ReadVersion(ctx, "big/blob.bin")
	if err != nil || !bytes.Equal(got, data) || !v1.Equal(v2) {
		t.Fatalf("ReadVersion(6 MiB) equal=%v versionEqual=%v err=%v", bytes.Equal(got, data), v1.Equal(v2), err)
	}

	if res, err := runner.Run(ctx, "printf keep > kept.txt && ln -s kept.txt alias.txt && printf '\treturn 1\n' > code.ts && printf 'x\n' > view.tsx"); err != nil || res.ExitCode != 0 {
		t.Fatalf("fixture setup = %+v, %v", res, err)
	}
	if err := ws.(tool.WorkspaceNamespace).Remove(ctx, "alias.txt"); err != nil {
		t.Fatal(err)
	}
	if kept, err := ws.Read(ctx, "kept.txt"); err != nil || string(kept) != "keep" {
		t.Fatalf("Remove(symlink) touched its target: %q, %v", kept, err)
	}
	if paths, err := ws.Glob(ctx, "**/*.{ts,tsx}"); err != nil || strings.Join(paths, ",") != "code.ts,view.tsx" {
		t.Fatalf("brace Glob = %v, %v", paths, err)
	}
	if hits, err := ws.Grep(ctx, `[[:space:]]+return`, "*.ts"); err != nil || len(hits) != 1 || hits[0].Line != 1 {
		t.Fatalf("POSIX-class Grep = %+v, %v", hits, err)
	}
}
