//go:build linux

package clientauth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func detectorNamespace() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS, UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}, GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}, GidMappingsEnableSetgroups: false}
}

// exitChrootUnsupported signals the parent that the sandbox's own namespace
// grants full capabilities but the runner's LSM/kernel policy still denies
// chroot(2) inside it (observed on GH-hosted ubuntu-24.04 runners) - a
// fixture-unsupported environment, distinct from a real detection failure.
const exitChrootUnsupported = 83

func TestSecretServiceDiscoveryChild(_ *testing.T) {
	root := os.Getenv("MECATL_TEST_DISCOVERY_ROOT")
	if root == "" {
		return
	}
	// godbus discovers /run/user/<uid>, ignoring XDG_RUNTIME_DIR. Isolate that
	// actual path rather than accidentally inspecting the operator's session bus.
	if _, err := user.Current(); err != nil {
		os.Exit(82)
	}
	if err := syscall.Chroot(root); err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) {
			os.Exit(exitChrootUnsupported)
		}
		os.Exit(82)
	}
	if os.Chdir("/") != nil {
		os.Exit(82)
	}
	os.Exit(secretServiceHelper(context.Background()))
}

func TestHeadlessCredentialStorage_Scenario1_LinuxDiscoveryBudget(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	probe := exec.Command(executable, "-test.run=^TestSecretServiceDiscoveryChild$")
	probe.Env = []string{"GORACE=atexit_sleep_ms=0"}
	probe.SysProcAttr = detectorNamespace()
	if err := probe.Run(); err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) {
			t.Skip("kernel disables isolated user/mount namespaces required for /run/user fixture")
		}
		t.Fatal(err)
	}
	for _, stalled := range []bool{false, true} {
		t.Run(fmt.Sprint(stalled), func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "run", "user", "0")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if stalled {
				fifo := filepath.Join(dir, "dbus-session")
				if err := syscall.Mkfifo(fifo, 0600); err != nil {
					t.Fatal(err)
				}
				// An open writer with no bytes makes the real discovery ReadFile block.
				keeper, err := os.OpenFile(fifo, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = keeper.Close() }()
			}
			var cmd *exec.Cmd
			state, err := runSecretServiceDetector(t.Context(), 500*time.Millisecond, func(ctx context.Context, executable string) *exec.Cmd {
				cmd = exec.CommandContext(ctx, executable, "-test.run=^TestSecretServiceDiscoveryChild$")
				cmd.Env = []string{"MECATL_TEST_DISCOVERY_ROOT=" + root, "GORACE=atexit_sleep_ms=0"}
				cmd.SysProcAttr = detectorNamespace()
				return cmd
			})
			if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == exitChrootUnsupported {
				t.Skip("kernel/LSM policy denies chroot(2) inside an unprivileged user+mount namespace on this runner")
			}
			if err != nil || state != secretServiceAbsent || cmd.ProcessState == nil {
				t.Fatalf("discovery state=%v err=%v", state, err)
			}
			status := cmd.ProcessState.Sys().(syscall.WaitStatus)
			if stalled && (!status.Signaled() || status.Signal() != syscall.SIGKILL) {
				t.Fatal("discovery did not reach killed blocking read")
			}
			if !stalled && cmd.ProcessState.ExitCode() != 81 {
				t.Fatal("no-address result was not explicit absence")
			}
		})
	}
}

func TestHeadlessCredentialStorage_Scenario1_LinuxDialBudget(t *testing.T) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Listen(fd, 1); err != nil {
		t.Fatal(err)
	}
	bound, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	address := fmt.Sprintf("127.0.0.1:%d", bound.(*syscall.SockaddrInet4).Port)
	saturated := false
	for range 8 {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err != nil {
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatal(err)
			}
			saturated = true
			break
		}
		defer func() { _ = conn.Close() }()
	}
	if !saturated {
		t.Fatal("could not saturate private listener backlog")
	}
	var cmd *exec.Cmd
	state, err := runSecretServiceDetector(t.Context(), 500*time.Millisecond, func(ctx context.Context, executable string) *exec.Cmd {
		cmd = busChildCommand(ctx, executable, fmt.Sprintf("tcp:host=127.0.0.1,port=%d", bound.(*syscall.SockaddrInet4).Port), t.TempDir())
		return cmd
	})
	if err != nil || state != secretServiceAbsent || cmd.ProcessState == nil {
		t.Fatalf("dial state=%v err=%v", state, err)
	}
	status := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatal("dial stall was not killed and reaped")
	}
}

func TestHeadlessCredentialStorage_Scenario1_LinuxAutoSelectionRealBus(t *testing.T) {
	for _, mode := range []string{"present", "absent", "denied", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			address, done := fixtureBus(t, mode)
			t.Setenv("DBUS_SESSION_BUS_ADDRESS", address)
			t.Setenv("GORACE", "atexit_sleep_ms=0")
			root := filepath.Join(t.TempDir(), "root")
			selection, err := ResolveCredentialStore(t.Context(), root, CredentialStoreAuto)
			if fixtureErr := <-done; fixtureErr != nil {
				t.Fatal(fixtureErr)
			}
			if mode == "denied" || mode == "malformed" {
				if err == nil {
					t.Fatal("detection failure selected a backend")
				}
				if _, err := os.Stat(filepath.Join(root, backendMarker)); !os.IsNotExist(err) {
					t.Fatal("failed detection wrote pin")
				}
				return
			}
			expected := CredentialBackendFile
			if mode == "present" {
				expected = CredentialBackendKeyring
			}
			if err != nil || selection.Backend != expected || !selection.NewlyPinned {
				t.Fatalf("unexpected selection: %#v %v", selection, err)
			}
			pinned, err := ResolveCredentialStore(t.Context(), root, CredentialStoreAuto)
			if err != nil || pinned.Backend != expected || pinned.NewlyPinned {
				t.Fatal("pin not reused without detection")
			}
		})
	}
}

func TestSecretServiceEarlyPrivateInvocation(t *testing.T) {
	address, done := fixtureBus(t, "present")
	var cmd *exec.Cmd
	state, err := runSecretServiceDetector(t.Context(), 500*time.Millisecond, func(ctx context.Context, executable string) *exec.Cmd {
		cmd = exec.CommandContext(ctx, executable, secretServiceHelperArg)
		cmd.Env = []string{"DBUS_SESSION_BUS_ADDRESS=" + address, "GORACE=atexit_sleep_ms=0"}
		return cmd
	})
	if fixtureErr := <-done; fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	if err != nil || state != secretServicePresent || cmd.ProcessState == nil {
		t.Fatalf("private invocation state=%v err=%v", state, err)
	}
}
