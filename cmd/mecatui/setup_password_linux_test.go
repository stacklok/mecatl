//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
)

func openPasswordPTY(t *testing.T) (master, reader, observer *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("Linux PTY fixture unavailable: open /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	if err := unix.IoctlSetInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Skipf("Linux PTY fixture unavailable: unlock ptmx: %v", err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Skipf("Linux PTY fixture unavailable: resolve slave: %v", err)
	}
	path := fmt.Sprintf("/dev/pts/%d", n)
	reader, err = os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("Linux PTY fixture unavailable: open slave: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	observer, err = os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("Linux PTY fixture unavailable: open observer: %v", err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	state, err := unix.IoctlGetTermios(int(observer.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	state.Lflag |= unix.ECHO
	if err := unix.IoctlSetTermios(int(observer.Fd()), unix.TCSETS, state); err != nil {
		t.Fatal(err)
	}
	return master, reader, observer
}

func waitForPasswordEcho(t *testing.T, observer *os.File, enabled bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		state, err := unix.IoctlGetTermios(int(observer.Fd()), unix.TCGETS)
		if err != nil {
			t.Fatal(err)
		}
		if (state.Lflag&unix.ECHO != 0) == enabled {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("terminal echo did not become enabled=%v", enabled)
		default:
			runtime.Gosched()
		}
	}
}

func TestSetupDeclinedSecretUsesRealPasswordReaderWithoutLeakOrWrite(t *testing.T) {
	const secret = "SYNTHETIC-PTY-SECRET"
	master, reader, observer := openPasswordPTY(t)
	var output bytes.Buffer
	var updated atomic.Bool
	runner := setupRunner{
		inFD:  int(reader.Fd()),
		outFD: int(observer.Fd()),
		out:   &output,
		deps: setupDeps{
			isTerminal: func(int) bool { return true },
			readSecret: func(ctx context.Context) ([]byte, error) { return readPasswordContext(ctx, reader) },
			updateKey: func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
				updated.Store(true)
				return authfile.CommitDurable, nil
			},
		},
		readLine: scriptedLines("1", "openai", "n", "4"),
		snapshot: setupSnapshot{Rows: []providerStatus{{
			ID: "openai", CredentialSource: credentialSourceMissing, Mutable: true,
		}}},
	}
	done := make(chan error, 1)
	go func() { done <- runner.run(t.Context()) }()
	waitForPasswordEcho(t, observer, false)
	if _, err := master.WriteString(secret + "\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("declined setup did not join")
	}
	waitForPasswordEcho(t, observer, true)
	if updated.Load() || strings.Contains(output.String(), secret) {
		t.Fatalf("declined real password read: updated=%v output=%q", updated.Load(), output.String())
	}
}

func TestReadPasswordContextCancellationRestoresTerminalAndJoins(t *testing.T) {
	_, reader, observer := openPasswordPTY(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := readPasswordContext(ctx, reader)
		done <- err
	}()
	waitForPasswordEcho(t, observer, false)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("readPasswordContext = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled password reader did not join")
	}
	waitForPasswordEcho(t, observer, true)
}

func TestReadPasswordContextTerminalErrorRestoresTerminalAndJoins(t *testing.T) {
	master, reader, observer := openPasswordPTY(t)
	done := make(chan error, 1)
	go func() {
		_, err := readPasswordContext(t.Context(), reader)
		done <- err
	}()
	waitForPasswordEcho(t, observer, false)
	if err := master.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("PTY EOF/error unexpectedly succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("failed password reader did not join")
	}
	waitForPasswordEcho(t, observer, true)
}
