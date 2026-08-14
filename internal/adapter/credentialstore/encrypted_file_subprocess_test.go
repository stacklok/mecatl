//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package credentialstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var subprocessKey = bytes.Repeat([]byte{0x71}, 32)

func TestEncryptedFileSubprocessCAS(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	store := openFileStore(t, root, "subprocess", subprocessKey)
	if _, err := store.Put(context.Background(), []byte("key"), []byte("base"), nil); err != nil {
		t.Fatal(err)
	}

	first := startCredentialHelper(t, root, "put", "first")
	second := startCredentialHelper(t, root, "put", "second")
	first.waitReady(t)
	second.waitReady(t)
	first.release(t)
	second.release(t)
	outputs := []string{first.wait(t), second.wait(t)}
	successes, conflicts := 0, 0
	for _, output := range outputs {
		switch {
		case strings.HasPrefix(output, "ok"):
			successes++
		case strings.HasPrefix(output, "conflict"):
			conflicts++
		default:
			t.Fatalf("unexpected helper output %q", output)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	got, err := store.Get(context.Background(), []byte("key"))
	if err != nil || (string(got.Value) != "first" && string(got.Value) != "second") {
		t.Fatalf("final record = %q, %v", got.Value, err)
	}
}

func TestEncryptedFileSubprocessPutDeleteCAS(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	store := openFileStore(t, root, "subprocess", subprocessKey)
	if _, err := store.Put(context.Background(), []byte("key"), []byte("base"), nil); err != nil {
		t.Fatal(err)
	}
	put := startCredentialHelper(t, root, "put", "new")
	remove := startCredentialHelper(t, root, "delete", "")
	put.waitReady(t)
	remove.waitReady(t)
	put.release(t)
	remove.release(t)
	outputs := []string{put.wait(t), remove.wait(t)}
	putWon := strings.HasPrefix(outputs[0], "ok") && strings.HasPrefix(outputs[1], "conflict")
	deleteWon := strings.HasPrefix(outputs[0], "conflict") && strings.HasPrefix(outputs[1], "ok")
	if !putWon && !deleteWon {
		t.Fatalf("outputs = %q", outputs)
	}
}

func TestEncryptedFileSubprocessCrashBoundaries(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation string
		wantValue string
		wantTemp  bool
	}{
		{name: "before rename", operation: "crash-before-rename", wantValue: "base", wantTemp: true},
		{name: "after rename", operation: "crash-after-rename", wantValue: "new"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "credentials")
			store := openFileStore(t, root, "subprocess", subprocessKey)
			if _, err := store.Put(context.Background(), []byte("key"), []byte("base"), nil); err != nil {
				t.Fatal(err)
			}
			helper := startCredentialHelper(t, root, test.operation, "new")
			helper.waitReady(t)
			helper.release(t)
			if err := helper.cmd.Wait(); err == nil {
				t.Fatal("crash helper exited successfully")
			}
			got, err := store.Get(context.Background(), []byte("key"))
			if err != nil || string(got.Value) != test.wantValue {
				t.Fatalf("record after crash = %q, %v", got.Value, err)
			}
			temps, err := filepath.Glob(filepath.Join(store.nsPath, ".*.tmp"))
			if err != nil {
				t.Fatal(err)
			}
			if test.wantTemp && len(temps) == 0 {
				t.Fatal("pre-rename crash left no encrypted temporary")
			}
			for _, temp := range temps {
				data, err := os.ReadFile(temp)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(data, []byte("new")) {
					t.Fatal("crash temporary exposed plaintext")
				}
			}
		})
	}
}

func TestEncryptedFileSubprocessHelper(_ *testing.T) {
	if os.Getenv("MECATL_CREDENTIALSTORE_HELPER") != "1" {
		return
	}
	root := os.Getenv("MECATL_CREDENTIALSTORE_ROOT")
	operation := os.Getenv("MECATL_CREDENTIALSTORE_OPERATION")
	value := os.Getenv("MECATL_CREDENTIALSTORE_VALUE")
	store, err := NewEncryptedFile(root, "subprocess", subprocessKey)
	if err != nil {
		fmt.Fprintf(os.Stdout, "error:%v", err)
		return
	}
	defer store.Close()
	record, err := store.Get(context.Background(), []byte("key"))
	if err != nil {
		fmt.Fprintf(os.Stdout, "error:%v", err)
		return
	}
	switch operation {
	case "crash-before-rename":
		store.ops.beforeCommit = func(context.Context, string) error { os.Exit(42); return nil }
	case "crash-after-rename":
		store.ops.syncDir = func(*os.File) error { os.Exit(43); return nil }
	}
	ready := os.NewFile(3, "ready")
	gate := os.NewFile(4, "gate")
	if _, err := ready.Write([]byte{1}); err != nil {
		fmt.Fprintf(os.Stdout, "error:%v", err)
		return
	}
	_ = ready.Close()
	var signal [1]byte
	if _, err := io.ReadFull(gate, signal[:]); err != nil {
		fmt.Fprintf(os.Stdout, "error:%v", err)
		return
	}
	_ = gate.Close()
	switch operation {
	case "put", "crash-before-rename", "crash-after-rename":
		_, err = store.Put(context.Background(), []byte("key"), []byte(value), &record.Version)
	case "delete":
		err = store.Delete(context.Background(), []byte("key"), record.Version)
	default:
		err = errors.New("unknown operation")
	}
	switch {
	case err == nil:
		fmt.Fprint(os.Stdout, "ok")
	case errors.Is(err, ErrConflict), errors.Is(err, ErrNotFound):
		fmt.Fprint(os.Stdout, "conflict")
	default:
		fmt.Fprintf(os.Stdout, "error:%v", err)
	}
}

type helperProcess struct {
	cmd         *exec.Cmd
	readyReader *os.File
	gateWriter  *os.File
	output      bytes.Buffer
}

func startCredentialHelper(t *testing.T, root, operation, value string) *helperProcess {
	t.Helper()
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	gateReader, gateWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEncryptedFileSubprocessHelper$")
	cmd.Env = append(os.Environ(),
		"MECATL_CREDENTIALSTORE_HELPER=1",
		"MECATL_CREDENTIALSTORE_ROOT="+root,
		"MECATL_CREDENTIALSTORE_OPERATION="+operation,
		"MECATL_CREDENTIALSTORE_VALUE="+value,
	)
	cmd.ExtraFiles = []*os.File{readyWriter, gateReader}
	h := &helperProcess{cmd: cmd, readyReader: readyReader, gateWriter: gateWriter}
	cmd.Stdout = &h.output
	cmd.Stderr = &h.output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = readyWriter.Close()
	_ = gateReader.Close()
	return h
}

func (h *helperProcess) waitReady(t *testing.T) {
	t.Helper()
	var signal [1]byte
	if _, err := io.ReadFull(h.readyReader, signal[:]); err != nil {
		t.Fatalf("wait helper ready: %v (%s)", err, h.output.String())
	}
	_ = h.readyReader.Close()
}

func (h *helperProcess) release(t *testing.T) {
	t.Helper()
	if _, err := h.gateWriter.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	_ = h.gateWriter.Close()
}

func (h *helperProcess) wait(t *testing.T) string {
	t.Helper()
	if err := h.cmd.Wait(); err != nil {
		t.Fatalf("helper: %v (%s)", err, h.output.String())
	}
	return h.output.String()
}
