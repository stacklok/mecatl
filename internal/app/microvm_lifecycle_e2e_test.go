package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	microvmadapter "github.com/stacklok/mecatl/internal/adapter/microvm"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type placementTestDaemon struct {
	listener net.Listener
	mu       sync.Mutex
	ops      []string
}

func startPlacementTestDaemon(t *testing.T) *placementTestDaemon {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mecatl-microvm-")
	if err != nil {
		t.Fatalf("create short private socket directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove short private socket directory: %v", err)
		}
	})
	listener, err := net.Listen("unix", filepath.Join(dir, "microvmd.sock"))
	if err != nil {
		t.Fatal(err)
	}
	d := &placementTestDaemon{listener: listener}
	t.Cleanup(func() { _ = listener.Close() })
	go d.serve()
	return d
}

func (d *placementTestDaemon) endpoint() string { return "unix://" + d.listener.Addr().String() }

func (d *placementTestDaemon) serve() {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = conn.Close() }()
			var request map[string]any
			if readPlacementFrame(conn, &request) != nil {
				return
			}
			op, _ := request["operation"].(string)
			d.mu.Lock()
			d.ops = append(d.ops, op)
			d.mu.Unlock()
			binding, _ := request["binding"].(map[string]any)
			response := map[string]any{"binding": binding}
			if op == "create" {
				provision, _ := request["provision"].(map[string]any)
				sessionID, _ := provision["session_id"].(string)
				binding = map[string]any{"owner": "local", "session_id": sessionID, "environment_id": "env-fixed", "ref": "env-fixed@7", "generation": 7}
				response["binding"] = binding
				response["created"] = map[string]any{
					"ref": map[string]any{"Kind": "microvm", "ID": "env-fixed@7"}, "generation": 7,
					"host_worktree": "/private/host/path", "guest_root": "/workspace", "profile": "microvm-local",
					"guest_egress": "deny-all", "host_egress": "not constrained",
				}
			}
			_ = writePlacementFrame(conn, response)
		}()
	}
}

func (d *placementTestDaemon) operations() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.ops...)
}

func TestMicroVMDefaultPlacementUsesNormalCreateSessionAndExactReattach(t *testing.T) {
	ctx := context.Background()
	daemon := startPlacementTestDaemon(t)
	const scope server.PlacementScope = "deployment"
	provider, err := microvmadapter.NewPlacementProvider(daemon.endpoint(), t.TempDir(), "microvm-local", scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	built, err := Build(ctx, Config{
		Workspace: t.TempDir(), StoreDir: t.TempDir(), UseMock: true,
		PlacementProvider: provider, PlacementScope: scope,
		EnvironmentForkers: map[session.EnvironmentKind]tool.EnvironmentForker{session.EnvironmentKind("microvm"): provider},
		EnvironmentMergers: map[session.EnvironmentKind]tool.EnvironmentMerger{session.EnvironmentKind("microvm"): provider},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.EnvironmentRef.Kind != "microvm" || sess.EnvironmentRef.ID == "" || sess.EnvironmentRef.Revision != "7" {
		t.Fatalf("exact environment ref = %+v", sess.EnvironmentRef)
	}
	if sess.Placement.Kind != "microvm" || sess.Placement.Label != "Local microVM" || sess.Placement.Revision != "7" {
		t.Fatalf("public placement metadata = %+v", sess.Placement)
	}

	run, err := built.Service.StartRunContent(ctx, sess.ID, "continue", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	for range run.Events() {
	}
	ops := daemon.operations()
	foundResolve := false
	for _, op := range ops {
		foundResolve = foundResolve || op == "resolve"
	}
	if !foundResolve {
		t.Fatalf("daemon operations = %v; exact reattach was not exercised", ops)
	}
}

func readPlacementFrame(r io.Reader, value any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	payload := make([]byte, binary.BigEndian.Uint32(header[:]))
	if _, err := io.ReadFull(r, payload); err != nil {
		return err
	}
	return json.Unmarshal(payload, value)
}

func writePlacementFrame(w io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var frame bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	frame.Write(header[:])
	frame.Write(payload)
	_, err = w.Write(frame.Bytes())
	return err
}
