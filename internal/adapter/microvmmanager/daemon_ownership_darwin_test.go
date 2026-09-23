//go:build darwin

package microvmmanager

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDarwinDaemonOwnershipHeldTracksServiceLock(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, "microvmd.service.lock")
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	held, err := daemonOwnershipHeld(stateDir)
	if err != nil || !held {
		t.Fatalf("daemonOwnershipHeld() = %v, %v; want true", held, err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	held, err = daemonOwnershipHeld(stateDir)
	if err != nil || held {
		t.Fatalf("daemonOwnershipHeld() = %v, %v; want false", held, err)
	}
}
