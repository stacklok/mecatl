//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSDKServerEnablers_Scenario8_LifetimeSocketpairEOFStops covers the
// descriptor shape Node and Bun create for child_process stdio:"pipe".
func TestSDKServerEnablers_Scenario8_LifetimeSocketpairEOFStops(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("create UNIX-domain stream socketpair: %v", err)
	}
	parent := os.NewFile(uintptr(fds[1]), "parent-lifetime-socket")
	t.Cleanup(func() { _ = parent.Close() })

	p, err := openLifetimePipe(fds[0])
	if err != nil {
		_ = syscall.Close(fds[0])
		t.Fatalf("openLifetimePipe(socketpair endpoint): %v", err)
	}
	t.Cleanup(p.Close)

	if err := parent.Close(); err != nil {
		t.Fatalf("close parent socketpair endpoint: %v", err)
	}
	select {
	case <-p.Closed():
	case <-time.After(10 * time.Second):
		t.Fatal("the lifetime socketpair endpoint did not report EOF after its parent-held peer closed")
	}
}

// TestSDKServerEnablers_Scenario8_LifetimeFDStillRejectsFilesAndDevices pins
// the non-liveness descriptor classes while socketpair endpoints are admitted.
// Terminals are character devices too, so the /dev/null case covers their mode.
func TestSDKServerEnablers_Scenario8_LifetimeFDStillRejectsFilesAndDevices(t *testing.T) {
	regular, err := os.Create(filepath.Join(t.TempDir(), "regular"))
	if err != nil {
		t.Fatalf("create regular file: %v", err)
	}
	t.Cleanup(func() { _ = regular.Close() })

	device, err := os.Open("/dev/null")
	if err != nil {
		t.Fatalf("open character device: %v", err)
	}
	t.Cleanup(func() { _ = device.Close() })

	for _, tc := range []struct {
		name string
		file *os.File
		want string
	}{
		{name: "regular file", file: regular, want: "regular file"},
		{name: "character device", file: device, want: "character device"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkLifetimePipeFD(int(tc.file.Fd()))
			if err == nil {
				t.Fatalf("checkLifetimePipeFD(%s) = nil, want a startup error", tc.name)
			}
			if !strings.Contains(err.Error(), "--lifetime-pipe-fd") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q must name the flag and the rejected descriptor type %q", err, tc.want)
			}
		})
	}
}
