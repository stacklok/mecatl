package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/internal/testutil/recoveryhost"
)

func TestServerProviderRecovery_Scenario7_HostLogDeliveryPolicy(t *testing.T) {
	if args, child := recoveryhost.ChildArgs(t); child {
		// TestMain has installed a synthetic HOME. Configure the actual host's
		// operator settings to select an empty broker inventory (no container I/O).
		xdg.Reload()
		configDir := filepath.Join(xdg.ConfigHome, "mecatl")
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "settings.yaml"), []byte("mcp:\n  mode: broker\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "linux" {
			t.Setenv("XDG_RUNTIME_DIR", "/proc/self/cwd")
		}
		cfg, err := parseFlags(args)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
		defer cancel()
		target, _, cleanup, err := resolveTransport(ctx, cfg)
		defer cleanup()
		if err != nil {
			t.Fatal(err)
		}
		conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		svc := mecatlv1.NewHarnessServiceClient(conn)
		created, err := svc.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
		if err != nil {
			t.Fatal(err)
		}
		stream, err := svc.Converse(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: created.GetSessionId(), Text: "recover"}}}); err != nil {
			t.Fatal(err)
		}
		result := false
		for {
			ev, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if terminal := ev.GetEvent().GetResult(); terminal != nil {
				result = terminal.GetStop() == "end_turn" && terminal.GetText() == "host recovered"
			}
		}
		if !result {
			t.Fatal("embedded host did not recover before terminal")
		}
		return
	}
	for _, mode := range []string{"file", "quiet", "open failure"} {
		t.Run(mode, func(t *testing.T) {
			f := recoveryhost.New(t)
			root := t.TempDir()
			logPath := filepath.Join(root, "mecatui.log")
			if mode == "open failure" {
				// A directory cannot be opened as the diagnostics regular file.
				if err := os.Mkdir(logPath, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			args := append(f.Flags(), "--workspace="+root, "--user-model-dir="+filepath.Join(root, "usermodel"),
				"--no-user-model", "--no-memory", "--no-store", "--no-soul", "--no-shell", "--diagnostics-log="+logPath)
			if mode == "quiet" {
				args = append(args, "--quiet")
			}
			cmd := recoveryhost.Command(t, root, mode, args)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("embedded root: %v stdout=%s stderr=%s", err, &stdout, &stderr)
			}
			if f.Calls.Load() != 2 {
				t.Fatalf("actual provider calls=%d want 2", f.Calls.Load())
			}
			if strings.Contains(stderr.String(), "llm provider recovery") || strings.Contains(stdout.String(), "llm provider recovery") {
				t.Fatal("embedded recovery log leaked into terminal")
			}
			switch mode {
			case "file":
				body, err := os.ReadFile(logPath)
				if err != nil {
					t.Fatal(err)
				}
				recoveryhost.AssertLog(t, string(body))
			case "quiet":
				if _, err := os.Stat(logPath); !os.IsNotExist(err) {
					t.Fatalf("quiet host created diagnostics log: %v", err)
				}
			case "open failure":
				entries, err := os.ReadDir(logPath)
				if err != nil || len(entries) != 0 {
					t.Fatalf("failed log destination changed: %v %v", entries, err)
				}
			}
		})
	}
}
