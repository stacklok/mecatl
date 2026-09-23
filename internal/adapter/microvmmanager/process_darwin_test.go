//go:build darwin

package microvmmanager

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
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
		_ = listener.Close()
		serverErr <- os.Remove(paths.Socket)
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
