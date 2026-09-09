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

	"github.com/godbus/dbus/v5"
)

// TEMPORARY CI diagnostic: dump what the chrooted child actually saw before
// exiting, so we can see why it isn't reaching the expected 81 (absent) exit
// on the GH-hosted runner. Remove once the CI failure is understood.
func debugDiscoveryChild() {
	f, err := os.Create("/debug.log")
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	fmt.Fprintf(f, "uid=%d gid=%d\n", os.Getuid(), os.Getgid())
	entries, lsErr := os.ReadDir("/run/user/0")
	fmt.Fprintf(f, "ls /run/user/0 err=%v\n", lsErr)
	for _, e := range entries {
		fi, _ := e.Info()
		fmt.Fprintf(f, "  entry=%q mode=%v\n", e.Name(), fi.Mode())
	}
	fmt.Fprintf(f, "DBUS_SESSION_BUS_ADDRESS=%q\n", os.Getenv("DBUS_SESSION_BUS_ADDRESS"))
	conn, dialErr := dbus.SessionBusPrivateNoAutoStartup()
	fmt.Fprintf(f, "dial err=%v (%T)\n", dialErr, dialErr)
	if dialErr == nil {
		_ = conn.Close()
	}
}

func detectorNamespace() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS, UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}, GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}, GidMappingsEnableSetgroups: false}
}

func TestSecretServiceDiscoveryChild(_ *testing.T) {
	root := os.Getenv("MECATL_TEST_DISCOVERY_ROOT")
	if root == "" {
		return
	}
	debugf := func(format string, args ...any) {
		f, err := os.OpenFile(filepath.Join(root, "debug.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return
		}
		defer func() { _ = f.Close() }()
		fmt.Fprintf(f, format, args...)
	}
	// godbus discovers /run/user/<uid>, ignoring XDG_RUNTIME_DIR. Isolate that
	// actual path rather than accidentally inspecting the operator's session bus.
	u, err := user.Current()
	debugf("user.Current()=%+v err=%v\n", u, err)
	if err != nil {
		os.Exit(82)
	}
	chrootErr := syscall.Chroot(root)
	chdirErr := os.Chdir("/")
	debugf("chroot(%q)=%v chdir(/)=%v\n", root, chrootErr, chdirErr)
	if chrootErr != nil || chdirErr != nil {
		os.Exit(82)
	}
	debugDiscoveryChild()
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
			if err != nil || state != secretServiceAbsent || cmd.ProcessState == nil {
				if b, rErr := os.ReadFile(filepath.Join(root, "debug.log")); rErr == nil {
					t.Logf("child debug.log:\n%s", b)
				} else {
					t.Logf("no child debug.log: %v", rErr)
				}
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
