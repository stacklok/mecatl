package boxenv

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestLiveBox is an opt-in contract smoke against the public Box API. It never
// logs or persists BOX_API_KEY. Ordinary CI skips it.
func TestLiveBox(t *testing.T) {
	apiKey := os.Getenv("BOX_API_KEY")
	if apiKey == "" {
		t.Skip("set BOX_API_KEY to run the live Box contract smoke")
	}
	provider, err := New(Config{
		APIKey:       apiKey,
		Scope:        "live-test",
		TTLSeconds:   300,
		ReadyTimeout: 2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	binding, err := provider.Bind(ctx, server.PlacementBindRequest{
		Selector: server.DefaultPlacement(), Scope: "live-test", Operation: server.PlacementOperationCreate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding.Close != nil {
		defer func() {
			if err := binding.Close(); err != nil {
				t.Errorf("stop/archive live Box: %v", err)
			}
		}()
	}
	ws := binding.Environment.Workspace()
	if _, err := ws.CreateFile(ctx, "mecatl-box-live.txt", []byte("box-provider-ok\n")); err != nil {
		t.Fatal(err)
	}
	if got, err := ws.Read(ctx, "mecatl-box-live.txt"); err != nil || string(got) != "box-provider-ok\n" {
		t.Fatalf("read after write = %q, %v", got, err)
	}
	result, err := binding.Environment.CommandRunner().Run(ctx, "printf shell-ok")
	if err != nil || result.ExitCode != 0 || result.Stdout != "shell-ok" {
		t.Fatalf("shell result = %+v, %v", result, err)
	}
}
