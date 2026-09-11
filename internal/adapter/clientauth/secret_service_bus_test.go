//go:build linux || darwin

package clientauth

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// This child calls the real library path, not a fake status selector. On Linux
// the separate early-init test also exercises the reserved executable argument.
func TestSecretServiceBusChild(_ *testing.T) {
	if os.Getenv("MECATL_TEST_BUS_CHILD") != "1" {
		return
	}
	os.Exit(secretServiceHelper(context.Background()))
}

func busChildCommand(ctx context.Context, executable, address, runtimeDir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestSecretServiceBusChild$")
	cmd.Dir = runtimeDir
	cmd.Env = []string{"MECATL_TEST_BUS_CHILD=1", "DBUS_SESSION_BUS_ADDRESS=" + address, "XDG_RUNTIME_DIR=" + runtimeDir, "GORACE=atexit_sleep_ms=0"}
	return cmd
}

// A private wire-level bus fixture: accepts only Auth, Hello and NameHasOwner.
// No daemon, keyring, Secret Service implementation, or subprocess is launched.
func fixtureBus(t *testing.T, mode string) (string, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		done <- serveFixtureBus(conn, mode)
	}()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return "tcp:host=" + host + ",port=" + port, done
}

func serveFixtureBus(conn net.Conn, mode string) error {
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "\x00AUTH") {
		return fmt.Errorf("unexpected auth command")
	}
	if mode == "Auth" {
		_, err = io.Copy(io.Discard, reader)
		return err
	}
	if mode == "denied" {
		_, err = io.WriteString(conn, "REJECTED\r\n")
		return err
	}
	if _, err = io.WriteString(conn, "REJECTED EXTERNAL\r\n"); err != nil {
		return err
	}
	line, err = reader.ReadString('\n')
	if err != nil {
		return err
	}
	if line != "AUTH EXTERNAL\r\n" {
		return fmt.Errorf("unexpected auth mechanism")
	}
	if _, err = io.WriteString(conn, "OK 0123456789abcdef0123456789abcdef\r\n"); err != nil {
		return err
	}
	for {
		line, err = reader.ReadString('\n')
		if err != nil {
			return err
		}
		if line == "BEGIN\r\n" {
			break
		}
		if line != "NEGOTIATE_UNIX_FD\r\n" {
			return fmt.Errorf("unexpected post-auth command")
		}
		if _, err = io.WriteString(conn, "ERROR unsupported\r\n"); err != nil {
			return err
		}
	}
	for _, member := range []string{"Hello", "NameHasOwner"} {
		msg, err := dbus.DecodeMessage(reader)
		if err != nil {
			return err
		}
		if msg.Type != dbus.TypeMethodCall || msg.Headers[dbus.FieldDestination].Value() != "org.freedesktop.DBus" || msg.Headers[dbus.FieldInterface].Value() != "org.freedesktop.DBus" || msg.Headers[dbus.FieldMember].Value() != member || msg.Headers[dbus.FieldPath].Value() != dbus.ObjectPath("/org/freedesktop/DBus") {
			return fmt.Errorf("unexpected bus call")
		}
		body := []any{":1.42"}
		if member == "NameHasOwner" {
			if len(msg.Body) != 1 || msg.Body[0] != "org.freedesktop.secrets" || msg.Flags&dbus.FlagNoAutoStart == 0 {
				return fmt.Errorf("ownership check broadened authority")
			}
			body = []any{mode == "present"}
		}
		if mode == member {
			_, err = io.Copy(io.Discard, reader)
			return err
		}
		reply := &dbus.Message{Type: dbus.TypeMethodReply, Headers: map[dbus.HeaderField]dbus.Variant{dbus.FieldReplySerial: dbus.MakeVariant(msg.Serial()), dbus.FieldSignature: dbus.MakeVariant(dbus.SignatureOf(body...))}, Body: body}
		if mode == "malformed" && member == "NameHasOwner" {
			reply.Body = []any{"not-a-boolean"}
			reply.Headers[dbus.FieldSignature] = dbus.MakeVariant(dbus.SignatureOf(reply.Body...))
		}
		var encoded bytes.Buffer
		if err := reply.EncodeTo(&encoded, binary.LittleEndian); err != nil {
			return err
		}
		data := encoded.Bytes()
		binary.LittleEndian.PutUint32(data[8:12], 1)
		if _, err := conn.Write(data); err != nil {
			return err
		}
	}
	// Any further traffic (including a Secret Service call) violates the protocol.
	if _, err := reader.ReadByte(); err != io.EOF {
		return fmt.Errorf("unexpected post-detection traffic: %v", err)
	}
	return nil
}

func TestHeadlessCredentialStorage_Scenario1_DetectionIsReadOnly(t *testing.T) {
	for _, mode := range []string{"present", "absent", "denied", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			address, done := fixtureBus(t, mode)
			var cmd *exec.Cmd
			state, err := runSecretServiceDetector(t.Context(), 500*time.Millisecond, func(ctx context.Context, executable string) *exec.Cmd {
				cmd = busChildCommand(ctx, executable, address, t.TempDir())
				return cmd
			})
			if fixtureErr := <-done; fixtureErr != nil {
				t.Fatal(fixtureErr)
			}
			if mode == "present" && (err != nil || state != secretServicePresent) {
				t.Fatalf("present: state=%v err=%v", state, err)
			}
			if mode == "absent" && (err != nil || state != secretServiceAbsent) {
				t.Fatalf("absent: state=%v err=%v", state, err)
			}
			if (mode == "denied" || mode == "malformed") && err == nil {
				t.Fatal("bus error downgraded to absence")
			}
			if cmd.ProcessState == nil || cmd.Stdin != nil || cmd.Stdout != nil || cmd.Stderr != nil {
				t.Fatal("helper not joined or streams not null")
			}
		})
	}
}

func TestHeadlessCredentialStorage_Scenario1_WholeDetectionBudget(t *testing.T) {
	for _, stage := range []string{"Auth", "Hello", "NameHasOwner"} {
		t.Run(stage, func(t *testing.T) {
			address, done := fixtureBus(t, stage)
			var cmd *exec.Cmd
			state, err := runSecretServiceDetector(t.Context(), 500*time.Millisecond, func(ctx context.Context, executable string) *exec.Cmd {
				cmd = busChildCommand(ctx, executable, address, t.TempDir())
				return cmd
			})
			if fixtureErr := <-done; fixtureErr != nil {
				t.Fatal(fixtureErr)
			}
			if err != nil || state != secretServiceAbsent || cmd.ProcessState == nil {
				t.Fatalf("stall not killed and joined: state=%v err=%v", state, err)
			}
			status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatal("stall did not terminate with joined SIGKILL")
			}
		})
	}
}

func TestSecretServiceDeniedDialFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory search permission; wire authentication denial is tested separately")
	}
	root := t.TempDir()
	private := root + "/private"
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(private, 0000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(private, 0700) }()
	_, err := runSecretServiceDetector(t.Context(), 500*time.Millisecond, func(ctx context.Context, executable string) *exec.Cmd {
		return busChildCommand(ctx, executable, "unix:path=private/bus", root)
	})
	if err != errSecretServiceDetection {
		t.Fatal("permission-denied dial did not fail closed with sanitized error")
	}
}

func TestSecretServiceMissingAndMalformedBus(t *testing.T) {
	for _, address := range []string{"unix:path=missing", "not-a-bus-address"} {
		_, err := runSecretServiceDetector(t.Context(), 500*time.Millisecond, func(ctx context.Context, executable string) *exec.Cmd {
			return busChildCommand(ctx, executable, address, t.TempDir())
		})
		if strings.HasPrefix(address, "unix:") && err != nil {
			t.Fatal("missing bus did not select absence")
		}
		if !strings.HasPrefix(address, "unix:") && err == nil {
			t.Fatal("malformed address selected absence")
		}
	}
}
