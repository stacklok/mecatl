//go:build linux || darwin

package clientauth

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHeadlessCredentialStorage_Scenario1_HelperFailuresFailClosed(t *testing.T) {
	for _, mode := range []string{"80", "81", "82", "0", "unknown", "crash"} {
		t.Run(mode, func(t *testing.T) {
			var command *exec.Cmd
			factory := func(ctx context.Context, executable string) *exec.Cmd {
				command = exec.CommandContext(ctx, executable, "-test.run=^TestCredentialDetectorProcess$")
				command.Env = []string{"MECATL_TEST_DETECTOR=" + mode, "GORACE=atexit_sleep_ms=0"}
				return command
			}
			state, err := runSecretServiceDetector(t.Context(), 3*time.Second, factory)
			switch mode {
			case "80":
				if err != nil || state != secretServicePresent {
					t.Fatalf("state=%v err=%v", state, err)
				}
			case "81":
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
		})
	}
}

func TestCredentialDetectorDeadlineAndParentCancellation(t *testing.T) {
	for _, mode := range []string{"stall", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var command *exec.Cmd
			state, err := runSecretServiceDetector(ctx, 100*time.Millisecond, func(ctx context.Context, executable string) *exec.Cmd {
				command = exec.CommandContext(ctx, executable, "-test.run=^TestCredentialDetectorProcess$")
				command.Env = []string{"MECATL_TEST_DETECTOR=" + mode, "GORACE=atexit_sleep_ms=0"}
				if mode == "cancel" {
					cancel()
				}
				return command
			})
			if mode == "stall" {
				if err != nil || state != secretServiceAbsent {
					t.Fatalf("state=%v err=%v", state, err)
				}
			} else if !errors.Is(err, context.Canceled) {
				t.Fatalf("parent cancellation lost: %v", err)
			}
			if command != nil && command.Process != nil && command.ProcessState == nil {
				t.Fatal("started helper not reaped")
			}
		})
	}
}

func TestDetectorStartAndDeadlineFailures(t *testing.T) {
	for _, kind := range []string{"missing-executable", "not-executable", "parent-deadline", "unknown-after-expiry", "crash-after-expiry", "zero-after-expiry"} {
		t.Run(kind, func(t *testing.T) {
			ctx := t.Context()
			if kind == "parent-deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			}
			var cmd *exec.Cmd
			state, err := runSecretServiceDetector(ctx, 100*time.Millisecond, func(deadline context.Context, executable string) *exec.Cmd {
				mode := "stall"
				if strings.HasSuffix(kind, "after-expiry") {
					<-deadline.Done()
					// Force an observable non-timeout exit AFTER expiry, without deadline Kill.
					// Elapsed time alone must never turn this crash/status into absence.
					deadline = context.Background()
					mode = strings.TrimSuffix(kind, "-after-expiry")
					if mode == "zero" {
						mode = "0"
					}
				}
				if kind == "missing-executable" || kind == "not-executable" {
					executable = filepath.Join(t.TempDir(), "helper")
					if kind == "not-executable" {
						if err := os.WriteFile(executable, []byte("not executable"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				cmd = exec.CommandContext(deadline, executable, "-test.run=^TestCredentialDetectorProcess$")
				cmd.Env = []string{"MECATL_TEST_DETECTOR=" + mode, "GORACE=atexit_sleep_ms=0"}
				return cmd
			})
			if err == nil || state != 0 {
				t.Fatal("failure laundered into absence")
			}
			if cmd.Process != nil && cmd.ProcessState == nil {
				t.Fatal("started helper not reaped")
			}
			if kind == "parent-deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("parent deadline lost")
			}
			if kind != "parent-deadline" && err != errSecretServiceDetection {
				t.Fatal("raw helper error escaped")
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
