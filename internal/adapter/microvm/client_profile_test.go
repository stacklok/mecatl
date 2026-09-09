package microvm

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestPlacementProviderUsesOpaqueDaemonProfileAndExactRef(t *testing.T) {
	socket := testUnixSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	requestSeen := make(chan lifecycleRequest, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var request lifecycleRequest
		if readFrame(conn, &request) != nil {
			return
		}
		requestSeen <- request
		_ = writeFrame(conn, lifecycleResponse{
			Binding: binding{Owner: "local", SessionID: request.Provision.SessionID, EnvironmentID: "env-1", Ref: "env-1@7", Generation: 7},
			Created: &created{Ref: environmentRef{Kind: "microvm", ID: "env-1@7"}, Generation: 7, HostWorktree: "/private/worktree", GuestRoot: "/workspace", Profile: "microvm-local", GuestEgress: "deny-all", HostEgress: "not constrained"},
		})
	}()

	client, err := NewPlacementProvider("unix://"+socket, "/source", "microvm-local", "deployment", nil)
	if err != nil {
		t.Fatal(err)
	}
	placed, err := client.Bind(context.Background(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "deployment", Operation: server.PlacementOperationCreate})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	request := <-requestSeen
	if request.Provision == nil || request.Provision.Profile != "microvm-local" || request.Provision.SourceCheckout != "/source" || request.Provision.SessionID == "" {
		t.Fatalf("opaque provision request = %+v", request.Provision)
	}
	if placed.Ref.Kind != "microvm" || placed.Ref.Revision != "7" || !strings.HasSuffix(placed.Ref.ID, ".env-1") {
		t.Fatalf("exact placement ref = %+v", placed.Ref)
	}
	if placed.Metadata.Label != "Local microVM" || strings.Contains(placed.Metadata.Label, "/private/") {
		t.Fatalf("public placement metadata = %+v", placed.Metadata)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
