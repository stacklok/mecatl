package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/testutil/recoveryhost"
)

func TestServerProviderRecovery_Scenario7_HostLogDeliveryPolicy(t *testing.T) {
	if args, child := recoveryhost.ChildArgs(t); child {
		// realMain chooses its diagnostics sink; injected human trace is separate.
		if code := realMain(args, os.Stdout, io.Discard); code != 0 {
			t.Fatalf("realMain exit=%d", code)
		}
		return
	}
	for _, mode := range []string{"stderr", "failed writer"} {
		t.Run(mode, func(t *testing.T) {
			f := recoveryhost.New(t)
			root := t.TempDir()
			initTestRepo(t, root)
			args := append(f.Flags(), "--workspace="+root, "--prompt=recover", "--timeout=5s")
			cmd := recoveryhost.Command(t, root, mode, args)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("realMain: %v stderr=%s stdout=%s", err, &stderr, &stdout)
			}
			if !strings.Contains(stdout.String(), "end_turn") || f.Calls.Load() != 2 {
				t.Fatalf("outcome=%s actual calls=%d", &stdout, f.Calls.Load())
			}
			if mode == "stderr" {
				recoveryhost.AssertLog(t, stderr.String())
			}
			if strings.Contains(stdout.String(), "llm provider recovery") {
				t.Fatal("diagnostics leaked into summary stdout")
			}
		})
	}
}
