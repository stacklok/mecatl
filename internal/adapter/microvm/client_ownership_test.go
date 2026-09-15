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
		for range 2 {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			var request lifecycleRequest
			if readFrame(conn, &request) == nil {
				requests <- request
				_ = writeFrame(conn, lifecycleResponse{Binding: claim})
			}
			_ = conn.Close()
		}
	}()
	client, err := NewPlacementProvider("unix://"+socket, "/source", "microvm-local", "deployment", nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := session.EnvironmentRef{Kind: kindMicroVM, ID: "placement.logical", Revision: "7"}
	binding, err := client.Reattach(t.Context(), server.PlacementReattachRequest{Ref: ref, Principal: &session.Principal{Subject: "alice"}, Scope: "deployment"})
	if err != nil {
		t.Fatal(err)
	}
	if binding.Close == nil {
		t.Fatal("Reattach returned no detach owner")
	}
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	resolve, detach := <-requests, <-requests
	if resolve.Operation != "resolve" || detach.Operation != "detach" || detach.Binding != claim {
		t.Fatalf("operations = %q/%q, detach binding = %+v", resolve.Operation, detach.Operation, detach.Binding)
	}
}
