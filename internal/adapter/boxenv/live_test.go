//go:build box_live

package boxenv

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestLiveBoxEnvironment is an opt-in contract smoke test against the real Ascii
// Box API. It is excluded from ordinary CI and never persists the credential.
//
// Run with:
//
//   BOX_API_KEY=... go test -tags=box_live ./internal/adapter/boxenv -run TestLiveBoxEnvironment -v
func TestLiveBoxEnvironment(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("BOX_API_KEY"))
	if apiKey == "" {
		t.Skip("BOX_API_KEY is required for the opt-in live test")
	}

	const scope server.PlacementScope = "box-live-test"
	provider, err := NewProvider(Config{
		APIKey:       apiKey,
		Scope:        scope,
		TTLSeconds:   90,
		ReadyTimeout: 3 * time.Minute,
		PollInterval: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	binding, err := provider.Bind(ctx, server.PlacementBindRequest{
		Selector:  server.DefaultPlacement(),
		Scope:     scope,
		Operation: server.PlacementOperationCreate,
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := provider.client.stopBox(cleanupCtx, binding.Ref.ID); err != nil {
			t.Logf("cleanup stop Box: %v", err)
		}
	}()

	ws := binding.Environment.Workspace()
	const file = "mecatl-live/hello.txt"
	const want = "hello from mecatl box\n"
	if _, err := ws.CreateFile(ctx, file, []byte(want)); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}

	read, _, err := ws.ReadVersion(ctx, file)
	if err != nil || string(read) != want {
		t.Fatalf("ReadVersion = %q, %v; want %q", read, err, want)
	}

	run, err := binding.Environment.CommandRunner().Run(ctx, "cat mecatl-live/hello.txt && printf shell-visible > mecatl-live/from-shell.txt")
	if err != nil || run.ExitCode != 0 || run.Stdout != want {
		t.Fatalf("Shell = %#v, %v", run, err)
	}
	fromShell, err := ws.Read(ctx, "mecatl-live/from-shell.txt")
	if err != nil || string(fromShell) != "shell-visible" {
		t.Fatalf("workspace did not observe Shell write: %q, %v", fromShell, err)
	}

	reattached, err := provider.Reattach(ctx, server.PlacementReattachRequest{
		Ref:   binding.Ref,
		Scope: scope,
	})
	if err != nil {
		t.Fatalf("Reattach: %v", err)
	}
	if reattached.Ref != binding.Ref {
		t.Fatalf("reattached ref = %#v, want %#v", reattached.Ref, binding.Ref)
	}
	read, err = reattached.Environment.Workspace().Read(ctx, file)
	if err != nil || string(read) != want {
		t.Fatalf("reattached Read = %q, %v; want %q", read, err, want)
	}
}
