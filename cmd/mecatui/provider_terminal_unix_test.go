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
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
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
	for _, mode := range []string{"success", "limit", "oversized", "cancel", "ctrl-c", "eof", "control", "read-failure", "echo-off", "nonblocking", "field-cancel"} {
		t.Run(mode, func(t *testing.T) {
			master, slave := providerPTY(t)
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
				value, err := readProviderTerminalLine(ctx, readInput, slave, "Key", mode != "field-cancel")
				done <- result{value, err}
			}()
			output := providerPTYOutput(t, master, "Key: ")
			key := "sensitive-sentinel"
			input := key + "x\x7f\r"
			switch mode {
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
				if _, err := io.WriteString(master, input); err != nil {
					t.Fatal(err)
				}
			}
			var got result
			select {
			case got = <-done:
			case <-time.After(10 * time.Second):
				cancel()
				<-done
				t.Fatal("terminal reader did not stop")
			}
			if mode == "success" || mode == "limit" || mode == "echo-off" || mode == "nonblocking" {
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
			if strings.Contains(output, "sensitive") || (got.err != nil && strings.Contains(got.err.Error(), "sensitive")) {
				t.Fatal("secret leaked")
			}
			restored, err := term.GetState(int(originalFD))
			if err != nil || !reflect.DeepEqual(restored, original) {
				t.Fatal("terminal state not restored before return")
			}
			afterFlags, err := unix.FcntlInt(originalFD, unix.F_GETFL, 0)
			if err != nil || afterFlags != flags {
				t.Fatal("stdin flags not restored")
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
		cmd.Env = append(os.Environ(), "MECATL_TEST_PROVIDER_TERMINAL=1")
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
		cmd.Env = append(os.Environ(), "MECATL_TEST_PROVIDER_TERMINAL=1")
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

func TestProviderTerminalCommandProcess(_ *testing.T) {
	if os.Getenv("MECATL_TEST_PROVIDER_TERMINAL") != "1" {
		return
	}
	os.Args = []string{"mecatui", "providers", "login", "openai"}
	main()
}
