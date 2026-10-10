//go:build darwin

package microvmmanager

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDarwinStopUsesAuthenticatedControlWithoutPIDSignal(t *testing.T) {
	paths := testPaths(t.TempDir())
	if err := preparePaths(paths); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.DaemonBinary, []byte("darwin-daemon-binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	binaryIdentity, err := fileSHA256(paths.DaemonBinary)
	if err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{
		"release_identity": "release-v1", "binary_identity": binaryIdentity, "policy_revision": "policy-v1",
		"profiles": map[string]any{"microvm-local": map[string]any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	config = append(config, '\n')
	if err := os.WriteFile(paths.ConfigFile, config, 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := expectedDaemonInfo(paths)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(paths.Socket, 0o600); err != nil {
		t.Fatal(err)
	}
	operations := make(chan string, 2)
	serverErr := make(chan error, 1)
	go func() {
		for i := 0; i < 2; i++ {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				serverErr <- acceptErr
				return
			}
			var request struct {
				Version   uint16          `json:"version"`
				Operation string          `json:"operation"`
				Payload   json.RawMessage `json:"payload,omitempty"`
			}
			if readManagerTestFrame(conn, &request) != nil {
				_ = conn.Close()
				serverErr <- io.ErrUnexpectedEOF
				return
			}
			operations <- request.Operation
			if request.Operation == "info" {
				payload, _ := json.Marshal(expected)
				_ = writeManagerTestFrame(conn, map[string]any{"payload": json.RawMessage(payload)})
			} else {
				var got DaemonInfo
				if json.Unmarshal(request.Payload, &got) != nil || !got.Equal(expected) {
					_ = conn.Close()
					serverErr <- io.ErrUnexpectedEOF
					return
				}
				_ = writeManagerTestFrame(conn, map[string]any{})
			}
			_ = conn.Close()
		}
		serverErr <- listener.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := (&DefaultOperations{}).Stop(ctx, paths); err != nil {
		t.Fatalf("Darwin authenticated stop: %v", err)
	}
	if first, second := <-operations, <-operations; first != "info" || second != "shutdown" {
		t.Fatalf("Darwin stop operations = %q, %q; want info, shutdown", first, second)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if err := (&DefaultOperations{}).Stop(ctx, paths); err != nil {
		t.Fatalf("repeated stop after socket removal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.StateDir, "microvmd.pid")); !os.IsNotExist(err) {
		t.Fatalf("test unexpectedly depended on PID state: %v", err)
	}
}

func TestDarwinProcessStartIdentityTreatsReapedChildAsAbsent(t *testing.T) {
	child := exec.Command("/bin/sleep", "0")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	pid := child.ProcessState.Pid()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Skipf("reaped child PID %d was reused before inspection: %v", pid, err)
	}
	if _, err := processStartIdentity(pid); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("processStartIdentity(%d) error = %v, want fs.ErrNotExist", pid, err)
	}
}

func TestDarwinStartReplacesConfirmedAbsentDaemon(t *testing.T) {
	paths := testPaths(t.TempDir())
	if err := preparePaths(paths); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.DaemonBinary, []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	departed := exec.Command("/bin/sleep", "0")
	if err := departed.Run(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(departed.ProcessState.Pid(), 0); !errors.Is(err, syscall.ESRCH) {
		t.Skipf("reaped child PID %d was reused before restart: %v", departed.ProcessState.Pid(), err)
	}
	writeDarwinManagedProcessRecord(t, paths, managedProcessRecord{
		Schema: managedProcessSchema, PID: departed.ProcessState.Pid(), ProcessIdentity: "departed",
	})

	if err := (&DefaultOperations{}).Start(t.Context(), paths); err != nil {
		t.Fatalf("Start with departed recorded daemon: %v", err)
	}
	t.Cleanup(func() {
		data, err := os.ReadFile(filepath.Join(paths.StateDir, "microvmd.process.json"))
		if err != nil {
			t.Errorf("read replacement daemon record during cleanup: %v", err)
			return
		}
		var record managedProcessRecord
		if err := json.Unmarshal(data, &record); err != nil {
			t.Errorf("decode replacement daemon record during cleanup: %v", err)
			return
		}
		if record.Schema != managedProcessSchema || record.PID <= 1 || record.ProcessIdentity == "" {
			t.Errorf("invalid replacement daemon record during cleanup")
			return
		}
		identity, err := processStartIdentity(record.PID)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Errorf("inspect replacement daemon during cleanup: %v", err)
			return
		}
		if identity != record.ProcessIdentity {
			t.Errorf("replacement daemon PID %d was reused during cleanup", record.PID)
			return
		}
		if err := syscall.Kill(-record.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("stop replacement daemon process group: %v", err)
		}
	})
	var replacement managedProcessRecord
	data, err := os.ReadFile(filepath.Join(paths.StateDir, "microvmd.process.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &replacement); err != nil {
		t.Fatal(err)
	}
	if replacement.PID == departed.ProcessState.Pid() {
		t.Fatal("Start retained departed daemon PID")
	}
	if err := syscall.Kill(replacement.PID, 0); err != nil {
		t.Fatalf("replacement daemon is not live: %v", err)
	}
}

func TestDarwinEnsureNoLiveManagedDaemonFailsClosedForLiveProcess(t *testing.T) {
	paths := testPaths(t.TempDir())
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	live := exec.Command("/bin/sleep", "30")
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = live.Process.Kill()
		_ = live.Wait()
	})
	identity, err := processStartIdentity(live.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	writeDarwinManagedProcessRecord(t, paths, managedProcessRecord{
		Schema: managedProcessSchema, PID: live.Process.Pid, ProcessIdentity: identity,
		Args: []string{paths.DaemonBinary, "--state-dir", paths.StateDir, "--socket", paths.Socket, "--config", paths.ConfigFile}, Socket: paths.Socket,
	})
	if err := ensureNoLiveManagedDaemon(paths); err == nil || !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("live recorded process error = %v, want fail-closed refusal", err)
	}
}

func TestDarwinProcessStartIdentityPreservesUncertainLiveProcess(t *testing.T) {
	live := exec.Command("/bin/sleep", "30")
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = live.Process.Kill()
		_ = live.Wait()
	})
	t.Setenv("PATH", t.TempDir())
	if _, err := processStartIdentity(live.Process.Pid); err == nil || errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		t.Fatalf("processStartIdentity(%d) error = %v, want uncertain error", live.Process.Pid, err)
	}
}

func writeDarwinManagedProcessRecord(t *testing.T, paths Paths, record managedProcessRecord) {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.StateDir, "microvmd.process.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeManagerTestFrame(dst io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	_, err = dst.Write(append(header[:], payload...))
	return err
}

func readManagerTestFrame(src io.Reader, value any) error {
	var header [4]byte
	if _, err := io.ReadFull(src, header[:]); err != nil {
		return err
	}
	payload := make([]byte, binary.BigEndian.Uint32(header[:]))
	if _, err := io.ReadFull(src, payload); err != nil {
		return err
	}
	return json.NewDecoder(bytes.NewReader(payload)).Decode(value)
}
