//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func providerPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close() })
	slave, err := os.OpenFile(providerPTYName(t, int(master.Fd())), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}

func providerPTYDescriptors(t *testing.T, originalFD int) int {
	t.Helper()
	var original unix.Stat_t
	if err := unix.Fstat(originalFD, &original); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	// Darwin's descriptor directory includes entries that cannot be statted.
	// Read names only; Fstat below determines which descriptors are still live.
	entries, err := dir.Readdirnames(-1)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry)
		if err != nil {
			continue
		}
		var current unix.Stat_t
		if unix.Fstat(fd, &current) == nil && current.Dev == original.Dev && current.Ino == original.Ino {
			count++
		}
	}
	if count == 0 {
		t.Fatal("PTY descriptor inventory missed the original")
	}
	return count
}

func providerPTYWrite(t *testing.T, master *os.File, input string) {
	t.Helper()
	fd := master.Fd()
	flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.SetNonblock(int(fd), true); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := unix.FcntlInt(fd, unix.F_SETFL, flags); err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for len(input) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("terminal input did not drain")
		}
		// A pasted line can exceed Darwin's PTY queue. Preserve partial writes
		// and wait for the reader instead of treating backpressure as failure.
		n, err := unix.Write(int(fd), []byte(input))
		if n > 0 {
			input = input[n:]
		}
		if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			t.Fatal(err)
		}
		if len(input) > 0 {
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
			if _, err := unix.Poll(poll, 50); err != nil && !errors.Is(err, unix.EINTR) {
				t.Fatal(err)
			}
		}
	}
}

func providerPTYOutput(t *testing.T, master *os.File, until string) string {
	t.Helper()
	var out strings.Builder
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), until) {
		if time.Now().After(deadline) {
			t.Fatal("terminal prompt did not arrive")
		}
		poll := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
		if _, err := unix.Poll(poll, 50); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			t.Fatal(err)
		}
		if poll[0].Revents&unix.POLLIN != 0 {
			var buf [4096]byte
			n, err := master.Read(buf[:])
			if err != nil {
				t.Fatal(err)
			}
			out.Write(buf[:n])
		}
	}
	return out.String()
}

func TestInvariant_ProviderSetupFollowup_TerminalCancellationAndSecretSafety(t *testing.T) {
	for _, mode := range []string{"success", "limit", "oversized", "cancel", "ctrl-c", "eof", "control", "read-failure", "echo-off", "nonblocking", "field-cancel", "field-success", "invalid-text"} {
		t.Run(mode, func(t *testing.T) {
			master, slave := providerPTY(t)
			// Input and output share this descriptor. Prime output before the
			// flag snapshot: Darwin adds its immutable FWASWRITTEN bit on the
			// first write, independently of the reader's restorable flags.
			if _, err := io.WriteString(slave, "\n"); err != nil {
				t.Fatal(err)
			}
			providerPTYOutput(t, master, "\r\n")
			if mode == "echo-off" {
				state, err := term.MakeRaw(int(slave.Fd()))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = term.Restore(int(slave.Fd()), state) }()
			}
			original, err := term.GetState(int(slave.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			originalFD := slave.Fd()
			if mode == "nonblocking" {
				if err := unix.SetNonblock(int(originalFD), true); err != nil {
					t.Fatal(err)
				}
			}
			flags, err := unix.FcntlInt(originalFD, unix.F_GETFL, 0)
			if err != nil {
				t.Fatal(err)
			}
			readInput := slave
			if mode == "read-failure" {
				readInput, err = os.OpenFile(slave.Name(), os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = readInput.Close() }()
			}
			descriptors := providerPTYDescriptors(t, int(originalFD))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				value string
				err   error
			}
			done := make(chan result, 1)
			go func() {
				value, err := readProviderTerminalLine(ctx, readInput, slave, "Key", mode != "field-cancel" && mode != "field-success")
				done <- result{value, err}
			}()
			output := providerPTYOutput(t, master, "Key: ")
			key := "sensitive-sentinel"
			input := key + "x\x7f\r"
			switch mode {
			case "field-success":
				key = "visible-value"
				input = key + "x\x7f\r"
			case "invalid-text":
				input = "\xff\r"
			case "limit":
				key = strings.Repeat("s", 8192)
				input = key + "\r"
			case "oversized":
				input = strings.Repeat("s", 9000) + "sensitive-sentinel\r"
			case "ctrl-c":
				input = key + "\x03"
			case "eof":
				input = key + "\x04"
			case "control":
				input = key + "\x1b[31m\r"
			case "cancel", "field-cancel":
				input = ""
				cancel()
			case "read-failure":
				input = "sensitive-sentinel"
			}
			if input != "" {
				providerPTYWrite(t, master, input)
			}
			var got result
			select {
			case got = <-done:
			case <-time.After(10 * time.Second):
				cancel()
				<-done
				t.Fatal("terminal reader did not stop")
			}
			if mode == "success" || mode == "limit" || mode == "echo-off" || mode == "nonblocking" || mode == "field-success" {
				if got.err != nil || got.value != key {
					t.Fatal("hidden entry did not round-trip")
				}
			} else if got.err == nil || got.value != "" {
				t.Fatal("unsafe input was accepted")
			}
			if mode == "cancel" || mode == "ctrl-c" || mode == "field-cancel" {
				if !errors.Is(got.err, context.Canceled) {
					t.Fatal("cancellation not preserved")
				}
			}
			if providerPTYDescriptors(t, int(originalFD)) != descriptors {
				t.Fatal("terminal reader descriptor leaked after return")
			}
			output += providerPTYOutput(t, master, "\r\n")
			if mode == "field-success" && !strings.Contains(output, "visible-valuex\b \b") {
				t.Fatal("visible field or backspace was not echoed")
			}
			if mode == "invalid-text" && (got.err == nil || !strings.Contains(got.err.Error(), "valid text")) {
				t.Fatal("invalid text lacks safe guidance")
			}
			if mode == "eof" && !errors.Is(got.err, io.EOF) {
				t.Fatal("EOF lost its distinct cancellation input")
			}
			if mode == "read-failure" && (got.err == nil || errors.Is(got.err, io.EOF) || errors.Is(got.err, context.Canceled)) {
				t.Fatal("read failure mislabeled as cancellation")
			}
			if strings.Contains(output, "sensitive") || (got.err != nil && strings.Contains(got.err.Error(), "sensitive")) {
				t.Fatal("secret leaked")
			}
			restored, err := term.GetState(int(originalFD))
			if err != nil || !reflect.DeepEqual(restored, original) {
				t.Fatal("terminal state not restored before return")
			}
			afterFlags, err := unix.FcntlInt(originalFD, unix.F_GETFL, 0)
			if err != nil || afterFlags != flags {
				t.Fatalf("stdin flags not restored: before=%#x after=%#x nonblock=%#x err=%v", flags, afterFlags, unix.O_NONBLOCK, err)
			}
		})
	}
	t.Run("requires-both-terminals", func(t *testing.T) {
		_, slave := providerPTY(t)
		file, err := os.CreateTemp(t.TempDir(), "non-terminal")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		for _, pair := range [][2]*os.File{{file, slave}, {slave, file}} {
			if _, err := readProviderTerminalLine(context.Background(), pair[0], pair[1], "Key", true); err == nil {
				t.Fatal("nonterminal accepted")
			}
		}
	})
	t.Run("command-sigint-exit", func(t *testing.T) {
		_, auth := followupHome(t, "{}\n", "")
		master, slave := providerPTY(t)
		original, err := term.GetState(int(slave.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProviderTerminalCommandProcess$")
		cmd.Env = append(os.Environ(), "MECATL_TEST_PROVIDER_TERMINAL=1", "MECATL_TEST_PROVIDER_CONFIG_HOME="+os.Getenv("XDG_CONFIG_HOME"))
		cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		output := providerPTYOutput(t, master, "API key for openai: ")
		if _, err := io.WriteString(master, "sensitive-sentinel"); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		err = cmd.Wait()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 130 {
			t.Fatalf("signal exit = %v", err)
		}
		output += providerPTYOutput(t, master, "Cancelled; no changes made.")
		if strings.Contains(output, "sensitive") {
			t.Fatal("signal leaked key")
		}
		state, err := term.GetState(int(slave.Fd()))
		if err != nil || !reflect.DeepEqual(state, original) {
			t.Fatal("signal did not restore terminal")
		}
		if _, err := os.Stat(auth); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("cancelled command wrote credentials")
		}
	})
	t.Run("declined-save", func(t *testing.T) {
		_, auth := followupHome(t, "{}\n", "")
		master, slave := providerPTY(t)
		original, err := term.GetState(int(slave.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProviderTerminalCommandProcess$")
		cmd.Env = append(os.Environ(), "MECATL_TEST_PROVIDER_TERMINAL=1", "MECATL_TEST_PROVIDER_CONFIG_HOME="+os.Getenv("XDG_CONFIG_HOME"))
		cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		output := providerPTYOutput(t, master, "API key for openai: ")
		if _, err := io.WriteString(master, "sensitive-sentinel\r"); err != nil {
			t.Fatal(err)
		}
		output += providerPTYOutput(t, master, "[y/N]: ")
		if _, err := io.WriteString(master, "no\r"); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
		output += providerPTYOutput(t, master, "API key not saved;")
		if strings.Contains(output, "sensitive") {
			t.Fatal("declined save leaked key")
		}
		state, err := term.GetState(int(slave.Fd()))
		if err != nil || !reflect.DeepEqual(state, original) {
			t.Fatal("declined save did not restore terminal")
		}
		if _, err := os.Stat(auth); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("declined command saved key")
		}
	})
}

func TestProviderReviewTerminalCommandErrors(t *testing.T) {
	for _, mode := range []string{"eof-key", "eof-default", "control", "invalid-text", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			_, auth := followupHome(t, "{}\n", "")
			master, slave := providerPTY(t)
			original, err := term.GetState(int(slave.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProviderTerminalCommandProcess$")
			cmd.Env = append(os.Environ(), "MECATL_TEST_PROVIDER_TERMINAL=1", "MECATL_TEST_PROVIDER_CONFIG_HOME="+os.Getenv("XDG_CONFIG_HOME"), "MECATL_TEST_PROVIDER_SETUP="+mode)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			output := providerPTYOutput(t, master, "API key for openai: ")
			input, want, exitCode := "sensitive-sentinel\x04", "Cancelled; no changes made.", 130
			switch mode {
			case "eof-default":
				input, want = "sensitive-sentinel\r", "API key remains saved"
			case "control":
				input, want, exitCode = "sensitive-sentinel\x1b[31m\r", "unsupported control characters", 1
			case "invalid-text":
				input, want, exitCode = "sensitive-sentinel\xff\r", "valid text", 1
			case "oversized":
				input, want, exitCode = strings.Repeat("s", 9000)+"sensitive-sentinel\r", "8 KiB acceptance limit", 1
			}
			providerPTYWrite(t, master, input)
			if mode == "eof-default" {
				output += providerPTYOutput(t, master, "[y/N]: ")
				if _, err := io.WriteString(master, "yes\r"); err != nil {
					t.Fatal(err)
				}
				output += providerPTYOutput(t, master, "deployment default? [y/N]: ")
				if _, err := io.WriteString(master, "\x04"); err != nil {
					t.Fatal(err)
				}
			}
			err = cmd.Wait()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != exitCode {
				t.Fatalf("exit = %v", err)
			}
			output += providerPTYOutput(t, master, want)
			if strings.Contains(output, "sensitive-sentinel") || strings.Contains(output, "could not read") {
				t.Fatal("secret leaked or actionable reader error lost")
			}
			if mode == "eof-default" {
				if strings.Contains(output, "no changes made") || !strings.Contains(readProviderCredentialTestFile(t, auth), "sensitive-sentinel") {
					t.Fatal("prior commit misreported")
				}
			} else if _, err := os.Stat(auth); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed input saved key")
			}
			state, err := term.GetState(int(slave.Fd()))
			if err != nil || !reflect.DeepEqual(state, original) {
				t.Fatal("terminal not restored")
			}
		})
	}
}

func TestProviderTerminalCommandProcess(t *testing.T) {
	if os.Getenv("MECATL_TEST_PROVIDER_TERMINAL") != "1" {
		return
	}
	// TestMain isolates subprocess homes too; restore the parent's explicit test
	// custody so its post-command filesystem assertions observe the actual writes.
	home := os.Getenv("MECATL_TEST_PROVIDER_CONFIG_HOME")
	if home == "" {
		t.Fatal("missing subprocess test custody")
	}
	t.Setenv("XDG_CONFIG_HOME", home)
	os.Args = []string{"mecatui", "providers", "login", "openai"}
	if os.Getenv("MECATL_TEST_PROVIDER_SETUP") == "eof-default" {
		os.Args[2] = "setup"
	}
	main()
}
