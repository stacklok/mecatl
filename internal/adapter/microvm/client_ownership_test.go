package microvm

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestReattachReturnsExactDetachOwner(t *testing.T) {
	socket := testUnixSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	claim := binding{Owner: `["","alice"]`, SessionID: "placement", EnvironmentID: "logical", Ref: "logical@7", Generation: 7}
	requests := make(chan lifecycleRequest, 2)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		var request lifecycleRequest
		if readFrame(conn, &request) != nil {
			return
		}
		requests <- request
		if writeFrame(conn, lifecycleResponse{Binding: claim, AcquisitionID: "0123456789abcdef0123456789abcdef"}) != nil {
			return
		}
		if readFrame(conn, &request) != nil {
			return
		}
		requests <- request
		_ = writeFrame(conn, lifecycleResponse{Binding: claim, AcquisitionID: request.AcquisitionID})
	}()
	client, err := NewPlacementProvider("unix://"+socket, "/source", "microvm-local", "deployment", nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := session.EnvironmentRef{Kind: kindMicroVM, ID: "placement.logical", Revision: "7"}
	placement, err := client.Reattach(t.Context(), server.PlacementReattachRequest{Ref: ref, Principal: &session.Principal{Subject: "alice"}, Scope: "deployment"})
	if err != nil {
		t.Fatal(err)
	}
	if placement.Close == nil {
		t.Fatal("Reattach returned no detach owner")
	}
	if err := placement.Close(); err != nil {
		t.Fatal(err)
	}
	resolve, detach := <-requests, <-requests
	if resolve.Operation != "resolve" || detach.Operation != "detach" || detach.Binding != claim || detach.AcquisitionID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("operations = %q/%q, detach = %+v", resolve.Operation, detach.Operation, detach)
	}
}

func TestReattachCachesTerminalCloseFailure(t *testing.T) {
	socket := testUnixSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	claim := binding{Owner: "local", SessionID: "placement", EnvironmentID: "logical", Ref: "logical@7", Generation: 7}
	terminals := make(chan lifecycleRequest, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		var request lifecycleRequest
		if readFrame(conn, &request) != nil {
			return
		}
		if writeFrame(conn, lifecycleResponse{Binding: claim, AcquisitionID: "0123456789abcdef0123456789abcdef"}) != nil {
			return
		}
		if readFrame(conn, &request) != nil {
			return
		}
		terminals <- request
		_ = writeFrame(conn, lifecycleResponse{Binding: claim, AcquisitionID: request.AcquisitionID, ErrorCode: "failed_precondition", ErrorText: "detach failed"})
	}()
	client, err := NewPlacementProvider("unix://"+socket, "/source", "microvm-local", "deployment", nil)
	if err != nil {
		t.Fatal(err)
	}
	placement, err := client.Reattach(t.Context(), server.PlacementReattachRequest{Ref: session.EnvironmentRef{Kind: kindMicroVM, ID: "placement.logical", Revision: "7"}, Scope: "deployment"})
	if err != nil {
		t.Fatal(err)
	}
	first := placement.Close()
	second := placement.Close()
	if first == nil || second == nil || first.Error() != second.Error() {
		t.Fatalf("cached close errors = %v / %v", first, second)
	}
	if terminal := <-terminals; terminal.Operation != "detach" {
		t.Fatalf("terminal operation = %+v", terminal)
	}
}

func TestOwnedRollbackReportsRetainedAndMalformedResultsIdempotently(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload json.RawMessage
		want    string
	}{
		{name: "retained", payload: mustJSON(t, DeleteResult{WorktreeRetained: true}), want: errRollbackRetained.Error()},
		{name: "missing", want: "omitted result"},
		{name: "malformed", payload: json.RawMessage(`"bad"`), want: "decode microvmd terminal cleanup result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverConn, clientConn := net.Pipe()
			claim := binding{Owner: "local", SessionID: "placement", EnvironmentID: "logical", Ref: "logical@7", Generation: 7}
			id := "0123456789abcdef0123456789abcdef"
			done := make(chan struct{})
			go func() {
				defer close(done)
				var request lifecycleRequest
				if readFrame(serverConn, &request) == nil {
					_ = writeFrame(serverConn, lifecycleResponse{Binding: claim, AcquisitionID: id, Payload: tc.payload})
				}
				_ = serverConn.Close()
			}()
			owner := &acquisition{client: &Client{cleanupTimeout: cleanupPhaseTimeout}, conn: clientConn, binding: claim, id: id}
			first := owner.terminal("delete")
			second := owner.terminal("delete")
			if first == nil || second == nil || first.Error() != second.Error() || !strings.Contains(first.Error(), tc.want) {
				t.Fatalf("terminal errors = %v / %v, want %q", first, second, tc.want)
			}
			<-done
		})
	}
}

func TestCreateCancellationAfterValidatedResponseKeepsExactRollbackAttribution(t *testing.T) {
	socket := testUnixSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	deleted := make(chan binding, 1)
	go func() {
		ownerConn, _ := listener.Accept()
		var request lifecycleRequest
		_ = readFrame(ownerConn, &request)
		claim := binding{Owner: request.Binding.Owner, SessionID: request.Binding.SessionID, EnvironmentID: "logical-created", Ref: "logical-created@7", Generation: 7}
		_ = writeFrame(ownerConn, lifecycleResponse{Binding: claim, AcquisitionID: "0123456789abcdef0123456789abcdef"})
		if readFrame(ownerConn, &request) == nil {
			deleted <- request.Binding
			_ = writeFrame(ownerConn, lifecycleResponse{Binding: request.Binding, AcquisitionID: request.AcquisitionID, Payload: mustJSON(t, DeleteResult{})})
			_ = ownerConn.Close()
			return
		}
		_ = ownerConn.Close()
		cleanupConn, _ := listener.Accept()
		_ = readFrame(cleanupConn, &request)
		deleted <- request.Binding
		_ = writeFrame(cleanupConn, lifecycleResponse{Binding: request.Binding, Payload: mustJSON(t, DeleteResult{})})
		_ = cleanupConn.Close()
	}()
	client, err := NewPlacementProvider("unix://"+socket, "/source", "microvm-local", "deployment", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	client.acquisitionResponseDecoded = cancel
	_, err = client.Bind(ctx, server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "deployment", Operation: server.PlacementOperationCreate})
	if err == nil || !strings.Contains(err.Error(), "automatic cleanup of the exact new placement completed") {
		t.Fatalf("Bind cancellation error = %v", err)
	}
	if got := <-deleted; got.EnvironmentID != "logical-created" || got.Generation != 7 {
		t.Fatalf("rollback target = %+v", got)
	}
}
