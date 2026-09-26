package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestForkMalformedResponseNeverDeletesUnprovenChild(t *testing.T) {
	for _, tc := range []struct {
		name   string
		safe   bool
		mutate func(*binding, *lifecycleResponse)
	}{
		{
			name: "canonical child with malformed payload",
			safe: true,
			mutate: func(_ *binding, response *lifecycleResponse) {
				response.Payload = json.RawMessage(`{"malformed":true}`)
			},
		},
		{name: "foreign owner", mutate: func(b *binding, _ *lifecycleResponse) { b.Owner = "foreign" }},
		{name: "wrong expected session", safe: true, mutate: func(b *binding, _ *lifecycleResponse) { b.SessionID = "session:other-label" }},
		{name: "parent ref", mutate: func(b *binding, _ *lifecycleResponse) { b.Ref = "logical-parent@7"; b.EnvironmentID = "logical-parent" }},
		{name: "foreign generation", mutate: func(b *binding, _ *lifecycleResponse) { b.Generation = 8; b.Ref = "logical-child@8" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket := testUnixSocketPath(t)
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			requests := make(chan lifecycleRequest, 2)
			go func() {
				conn, acceptErr := listener.Accept()
				if acceptErr != nil {
					return
				}
				var request lifecycleRequest
				_ = readFrame(conn, &request)
				requests <- request
				child := binding{Owner: request.Binding.Owner, SessionID: request.Binding.SessionID + ":branch", EnvironmentID: "logical-existing-sibling", Ref: "logical-existing-sibling@7", Generation: 7}
				response := lifecycleResponse{Binding: child, AcquisitionID: "0123456789abcdef0123456789abcdef"}
				tc.mutate(&response.Binding, &response)
				_ = writeFrame(conn, response)
				if readFrame(conn, &request) == nil {
					requests <- request
					terminal := lifecycleResponse{Binding: response.Binding, AcquisitionID: request.AcquisitionID}
					if request.Operation == "child-delete" {
						terminal.Payload = mustJSON(t, DeleteResult{})
					}
					_ = writeFrame(conn, terminal)
				}
				_ = conn.Close()
			}()
			client, err := New("unix://" + socket)
			if err != nil {
				t.Fatal(err)
			}
			parentClaim := binding{Owner: "local", SessionID: "session", EnvironmentID: "logical-parent", Ref: "logical-parent@7", Generation: 7}
			_, _, _, forkErr := client.Fork(t.Context(), client.environment(refForBinding(parentClaim), parentClaim), "branch")
			if forkErr == nil || !strings.Contains(forkErr.Error(), "microvmd fork returned an invalid child binding") {
				t.Fatalf("Fork error = %v", forkErr)
			}
			if tc.safe {
				if !strings.Contains(forkErr.Error(), "automatic cleanup of the exact new placement completed") {
					t.Fatalf("Fork error = %v, want exact child cleanup", forkErr)
				}
			} else if !strings.Contains(forkErr.Error(), "automatic cleanup was not authorized") || !strings.Contains(forkErr.Error(), "mecated microvm status") {
				t.Fatalf("Fork error = %v, want conservative recovery guidance", forkErr)
			}
			first := <-requests
			if first.Operation != "fork" {
				t.Fatalf("first operation = %q, want fork", first.Operation)
			}
			select {
			case request := <-requests:
				want := "detach"
				if tc.safe {
					want = "child-delete"
				}
				if request.Operation != want {
					t.Fatalf("malformed response terminal operation = %q, want %q for %+v", request.Operation, want, request.Binding)
				}
			case <-time.After(100 * time.Millisecond):
				t.Fatal("malformed retained acquisition was not released")
			}
		})
	}
}

func TestForkCleanupDetachesFromCancelledParent(t *testing.T) {
	socket := testUnixSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cleaned := make(chan binding, 1)
	go func() {
		conn, _ := listener.Accept()
		var request lifecycleRequest
		_ = readFrame(conn, &request)
		child := binding{Owner: request.Binding.Owner, SessionID: request.Binding.SessionID + ":branch", EnvironmentID: "logical-child", Ref: "logical-child@7", Generation: 7}
		_ = writeFrame(conn, lifecycleResponse{Binding: child, AcquisitionID: "0123456789abcdef0123456789abcdef"})
		_ = readFrame(conn, &request)
		cleaned <- request.Binding
		payload, _ := json.Marshal(DeleteResult{})
		_ = writeFrame(conn, lifecycleResponse{Binding: request.Binding, AcquisitionID: request.AcquisitionID, Payload: payload})
		_ = conn.Close()
	}()
	client, _ := New("unix://" + socket)
	claim := binding{Owner: "local", SessionID: "session", EnvironmentID: "logical-parent", Ref: "logical-parent@7", Generation: 7}
	ctx, cancel := context.WithCancel(t.Context())
	_, cleanup, _, err := client.Fork(ctx, client.environment(refForBinding(claim), claim), "branch")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup after parent cancellation: %v", err)
	}
	select {
	case got := <-cleaned:
		if got.Ref != "logical-child@7" {
			t.Fatalf("cleanup target = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("detached bounded cleanup did not reach daemon")
	}
}

func TestCreateRejectsInconsistentTupleAndRollsBackExactOwnedPlacement(t *testing.T) {
	socket := testUnixSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	rolledBack := make(chan binding, 1)
	go func() {
		conn, _ := listener.Accept()
		var request lifecycleRequest
		_ = readFrame(conn, &request)
		claim := binding{Owner: request.Binding.Owner, SessionID: request.Binding.SessionID, EnvironmentID: "logical-created", Ref: "logical-created@7", Generation: 7}
		_ = writeFrame(conn, lifecycleResponse{Binding: claim, AcquisitionID: "0123456789abcdef0123456789abcdef", Created: &created{Ref: environmentRef{Kind: "microvm", ID: "logical-other@7"}, Generation: 7, HostWorktree: "/private/worktree", GuestRoot: publicGuestRoot, Profile: "microvm-local", GuestEgress: "deny-all", HostEgress: "not constrained"}})
		_ = readFrame(conn, &request)
		rolledBack <- request.Binding
		payload, _ := json.Marshal(DeleteResult{})
		_ = writeFrame(conn, lifecycleResponse{Binding: claim, AcquisitionID: request.AcquisitionID, Payload: payload})
		_ = conn.Close()
	}()
	client, err := NewPlacementProvider("unix://"+socket, "/source", "microvm-local", "deployment", nil)
	if err != nil {
		t.Fatal(err)
	}
	placed, err := client.Bind(t.Context(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "deployment", Operation: server.PlacementOperationCreate})
	if err == nil || placed.Ref.Valid() {
		t.Fatalf("invalid create tuple returned placement %+v, err %v", placed, err)
	}
	if !strings.Contains(err.Error(), "microvmd create returned incomplete or inconsistent placement metadata") || !strings.Contains(err.Error(), "automatic cleanup of the exact new placement completed") {
		t.Fatalf("invalid create error did not preserve primary and cleanup outcome: %v", err)
	}
	if got := <-rolledBack; got.EnvironmentID != "logical-created" {
		t.Fatalf("rollback target = %+v", got)
	}
}

func TestWorkspaceRejectsInvalidVersionsWithoutReturningTokensOrMutating(t *testing.T) {
	for _, operation := range []string{"create", "replace"} {
		t.Run(operation+" response", func(t *testing.T) {
			socket := testUnixSocketPath(t)
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				conn, _ := listener.Accept()
				var request lifecycleRequest
				_ = readFrame(conn, &request)
				_ = writeFrame(conn, lifecycleResponse{Binding: request.Binding, Payload: mustJSON(t, workspaceResponse{Version: "untrusted", VersionValid: false})})
				_ = conn.Close()
			}()
			client, _ := New("unix://" + socket)
			claim := binding{Owner: "local", SessionID: "session", EnvironmentID: "logical", Ref: "logical@1", Generation: 1}
			ws := client.environment(refForBinding(claim), claim).Workspace()
			var version tool.FileVersion
			if operation == "create" {
				version, err = ws.CreateFile(t.Context(), "file", []byte("data"))
			} else {
				version, err = ws.ReplaceFile(t.Context(), "file", tool.NewFileVersion("old"), []byte("data"))
			}
			if err == nil {
				t.Fatal("invalid response version was accepted")
			}
			if version != (tool.FileVersion{}) {
				t.Fatalf("invalid response returned non-zero version: %#v", version)
			}
			if _, encodeErr := tool.EncodeFileVersion(version); !errors.Is(encodeErr, tool.ErrInvalidFileVersion) {
				t.Fatalf("returned version is usable: %v", encodeErr)
			}
		})
	}

	client, _ := New("unix://" + filepath.Join(t.TempDir(), "unused-microvmd.sock"))
	claim := binding{Owner: "local", SessionID: "session", EnvironmentID: "logical", Ref: "logical@1", Generation: 1}
	ws := client.environment(refForBinding(claim), claim).Workspace()
	if version, err := ws.ReplaceFile(context.Background(), "file", tool.FileVersion{}, []byte("data")); !errors.Is(err, tool.ErrInvalidFileVersion) {
		t.Fatalf("zero replace version = %+v, %v", version, err)
	}
}
