package microvm

import (
	"net"
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
