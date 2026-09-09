//go:build linux || darwin

package clientauth

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestHeadlessCredentialStorage_Scenario1_HelperFailuresFailClosed(t *testing.T) {
	for _, mode := range []string{"80", "81", "82", "0", "unknown", "crash", "stall", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var command *exec.Cmd
			factory := func(ctx context.Context, executable string) *exec.Cmd {
				command = exec.CommandContext(ctx, executable, "-test.run=TestCredentialDetectorProcess")
				command.Env = append(os.Environ(), "MECATL_TEST_DETECTOR="+mode, "GORACE=atexit_sleep_ms=0")
				if mode == "cancel" {
					cancel()
				}
				return command
			}
			state, err := runSecretServiceDetector(ctx, 100*time.Millisecond, factory)
			switch mode {
			case "80":
				if err != nil || state != secretServicePresent {
					t.Fatalf("state=%v err=%v", state, err)
				}
			case "81", "stall":
				if err != nil || state != secretServiceAbsent {
					t.Fatalf("state=%v err=%v", state, err)
				}
			default:
				if err == nil {
					t.Fatal("failure accepted as absence")
				}
			}
			if command != nil && command.Process != nil && command.ProcessState == nil {
				t.Fatal("started helper not reaped")
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("parent cancellation lost")
			}
		})
	}
}

func TestCredentialDetectorProcess(_ *testing.T) {
	switch os.Getenv("MECATL_TEST_DETECTOR") {
	case "":
		return
	case "80":
		os.Exit(80)
	case "81":
		os.Exit(81)
	case "82":
		os.Exit(82)
	case "0":
		os.Exit(0)
	case "unknown":
		os.Exit(83)
	case "crash":
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	default:
		time.Sleep(time.Hour)
	}
	os.Exit(82)
}
