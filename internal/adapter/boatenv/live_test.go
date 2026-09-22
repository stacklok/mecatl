package boatenv

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
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

	rebound, err := provider.Reattach(ctx, server.PlacementReattachRequest{Ref: binding.Ref, Scope: "live-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := provider.client.stopSandbox(context.Background(), binding.Ref.ID); err != nil {
			t.Errorf("archive live sandbox %s: %v", binding.Ref.ID, err)
		}
	}()
	if rebound.Ref != binding.Ref {
		t.Fatalf("reattach returned a different sandbox: %+v vs %+v", rebound.Ref, binding.Ref)
	}
	if survived, err := rebound.Environment.Workspace().Read(ctx, "shell.txt"); err != nil || string(survived) != "shell-ok" {
		t.Fatalf("content after archive/resume = %q, %v", survived, err)
	}

	stale := binding.Ref
	stale.Revision = "boat-v1-stale"
	if _, err := provider.Reattach(ctx, server.PlacementReattachRequest{Ref: stale, Scope: "live-test"}); err == nil {
		t.Fatal("stale revision reattached to a live sandbox")
	}
}
