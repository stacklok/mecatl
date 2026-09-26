//go:build linux

package microvmmanager

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDefaultOperationsStartReusesPreSocketDaemonOwnership(t *testing.T) {
	stateDir := t.TempDir()
	lockPath := filepath.Join(stateDir, "microvmd.service.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	paths := Paths{StateDir: stateDir, DaemonBinary: filepath.Join(stateDir, "not-yet-needed")}
	if err := (&DefaultOperations{}).Start(context.Background(), paths); err != nil {
		t.Fatalf("Start with live pre-socket owner = %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "microvmd.pid")); !os.IsNotExist(err) {
		t.Fatalf("Start launched a competing daemon: %v", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := (&DefaultOperations{}).Start(context.Background(), paths); err == nil {
		t.Fatal("Start treated a free ownership lock as a live daemon")
	}
}

func TestEnsureNoLiveManagedDaemonFailsClosedWhenInstalledExecutableDisappears(t *testing.T) {
	paths := testPaths(t.TempDir())
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	helper := exec.Command("sleep", "1")
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = helper.Wait() })
	identity, err := processStartIdentity(helper.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	record := managedProcessRecord{
		Schema:          managedProcessSchema,
		PID:             helper.Process.Pid,
		ProcessIdentity: identity,
		Args:            []string{paths.DaemonBinary, "--state-dir", paths.StateDir, "--socket", paths.Socket, "--config", paths.ConfigFile},
		Socket:          paths.Socket,
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.StateDir, "microvmd.process.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	err = ensureNoLiveManagedDaemon(paths)
	if err == nil || !strings.Contains(err.Error(), "refusing to start a possible duplicate") {
		t.Fatalf("live process with missing installed executable error = %v", err)
	}
}

func TestEnsureNoLiveManagedDaemonAllowsZombieRecordedProcess(t *testing.T) {
	paths := testPaths(t.TempDir())
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	helper := exec.Command("sh", "-c", "exit 0")
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = helper.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, _, err := linuxProcessStat(helper.Process.Pid)
		if err != nil {
			t.Fatalf("inspect unreaped helper: %v", err)
		}
		if state == 'Z' {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not become a zombie")
		}
		time.Sleep(time.Millisecond)
	}

	record := managedProcessRecord{Schema: managedProcessSchema, PID: helper.Process.Pid, ProcessIdentity: "recorded-before-exit"}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.StateDir, "microvmd.process.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureNoLiveManagedDaemon(paths); err != nil {
		t.Fatalf("zombie recorded process error = %v", err)
	}
}

func TestEnsureNoLiveManagedDaemonAllowsVanishedRecordedProcess(t *testing.T) {
	paths := testPaths(t.TempDir())
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	helper := exec.Command("true")
	if err := helper.Run(); err != nil {
		t.Fatal(err)
	}
	record := managedProcessRecord{Schema: managedProcessSchema, PID: helper.ProcessState.Pid(), ProcessIdentity: "vanished"}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.StateDir, "microvmd.process.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ensureNoLiveManagedDaemon(paths); err != nil {
		t.Fatalf("vanished recorded process error = %v", err)
	}
}
