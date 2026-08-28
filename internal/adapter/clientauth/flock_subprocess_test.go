package clientauth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

const flockHelperMode = "MECATL_CLIENTAUTH_FLOCK_HELPER"

func TestClientauthFlockHelper(t *testing.T) {
	mode := os.Getenv(flockHelperMode)
	if mode == "" {
		t.Skip("helper process only")
	}
	root := os.Getenv("MECATL_CLIENTAUTH_FLOCK_ROOT")
	var unlock func()
	switch mode {
	case "root":
		provider, err := newKeyringProvider(root, newMemoryKeyring())
		if err != nil {
			t.Fatal(err)
		}
		locked, err := provider.lock.TryLockContext(context.Background(), keyringLockRetry)
		if err != nil || !locked {
			t.Fatalf("root lock: locked=%v err=%v", locked, err)
		}
		unlock = func() { _ = provider.lock.Unlock() }
	case "target":
		registry, err := OpenRegistry(root)
		if err != nil {
			t.Fatal(err)
		}
		unlock, err = registry.lockTarget(context.Background(), os.Getenv("MECATL_CLIENTAUTH_FLOCK_TARGET"))
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
	fmt.Fprintln(os.Stdout, "acquired")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	unlock()
	fmt.Fprintln(os.Stdout, "released")
}

func TestRootFlockContendsAcrossProcessesAndProgressesAfterRelease(t *testing.T) {
	root := t.TempDir()
	provider, err := newKeyringProvider(root, newMemoryKeyring())
	if err != nil {
		t.Fatal(err)
	}
	runFlockContentionTest(t, "root", root, "", func(ctx context.Context) (func(), error) {
		locked, err := provider.lock.TryLockContext(ctx, keyringLockRetry)
		if err != nil {
			return nil, err
		}
		if !locked {
			return nil, errors.New("root lock not acquired")
		}
		return func() { _ = provider.lock.Unlock() }, nil
	})
}

func TestTargetFlockContendsAcrossProcessesAndProgressesAfterRelease(t *testing.T) {
	root := t.TempDir()
	registry, err := OpenRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	const target = "helper-lock.example:443"
	runFlockContentionTest(t, "target", root, target, func(ctx context.Context) (func(), error) {
		return registry.lockTarget(ctx, target)
	})
}

func runFlockContentionTest(t *testing.T, mode, root, target string, acquire func(context.Context) (func(), error)) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestClientauthFlockHelper$") // #nosec G204 -- fixed current test binary and fixed argument.
	cmd.Env = append(os.Environ(), flockHelperMode+"="+mode, "MECATL_CLIENTAUTH_FLOCK_ROOT="+root, "MECATL_CLIENTAUTH_FLOCK_TARGET="+target)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "acquired" {
		_ = cmd.Process.Kill()
		t.Fatalf("helper acquisition handshake failed: line=%q err=%v", scanner.Text(), scanner.Err())
	}

	contended, cancel := context.WithTimeout(t.Context(), 75*time.Millisecond)
	defer cancel()
	if unlock, err := acquire(contended); !errors.Is(err, context.DeadlineExceeded) {
		if unlock != nil {
			unlock()
		}
		_ = cmd.Process.Kill()
		t.Fatalf("cross-process contention error = %v", err)
	}
	if _, err := fmt.Fprintln(stdin, "release"); err != nil {
		t.Fatal(err)
	}
	if !scanner.Scan() || scanner.Text() != "released" {
		_ = cmd.Process.Kill()
		t.Fatalf("helper release handshake failed: line=%q err=%v", scanner.Text(), scanner.Err())
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}

	progress, cancelProgress := context.WithTimeout(t.Context(), time.Second)
	defer cancelProgress()
	unlock, err := acquire(progress)
	if err != nil {
		t.Fatalf("lock made no progress after helper release: %v", err)
	}
	unlock()
}
