package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
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
		claim := binding{Owner: "local", SessionID: request.Provision.SessionID, EnvironmentID: "logical-1", Ref: "logical-1@7", Generation: 7}
		if writeFrame(conn, lifecycleResponse{
			Binding: claim, AcquisitionID: "0123456789abcdef0123456789abcdef",
			Created: &created{Ref: environmentRef{Kind: "microvm", ID: "logical-1@7"}, Generation: 7, HostWorktree: "/private/worktree", GuestRoot: "/workspace", Profile: "microvm-local", GuestEgress: "deny-all", HostEgress: "not constrained"},
		}) != nil {
			return
		}
		if readFrame(conn, &request) == nil {
			_ = writeFrame(conn, lifecycleResponse{Binding: claim, AcquisitionID: request.AcquisitionID})
		}
	}()

	client, err := NewPlacementProvider("unix://"+socket, "/source", "microvm-local", "deployment", nil)
	if err != nil {
		t.Fatal(err)
	}
	placed, err := client.Bind(context.Background(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "deployment", Operation: server.PlacementOperationCreate})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	defer func() {
		if err := placed.Close(); err != nil {
			t.Errorf("close placement: %v", err)
		}
	}()
	request := <-requestSeen
	if request.Provision == nil || request.Provision.Profile != "microvm-local" || request.Provision.SourceCheckout != "/source" || request.Provision.SessionID == "" {
		t.Fatalf("opaque provision request = %+v", request.Provision)
	}
	if placed.Ref.Kind != "microvm" || placed.Ref.Revision != "7" || !strings.HasSuffix(placed.Ref.ID, ".logical-1") {
		t.Fatalf("exact placement ref = %+v", placed.Ref)
	}
	if placed.Metadata.Label != "Local microVM" || strings.Contains(placed.Metadata.Label, "/private/") {
		t.Fatalf("public placement metadata = %+v", placed.Metadata)
	}
	if placed.GovernanceRoot != "/source" || placed.Environment.Workspace().Root() != "/workspace" {
		t.Fatalf("host/guest roots = %q/%q", placed.GovernanceRoot, placed.Environment.Workspace().Root())
	}
}

func TestMicroVMWorkspaceProvidesConfinedAuthorityResourceIdentity(t *testing.T) {
	var resolver tool.AuthorityResourceResolver = &workspace{}
	target, root, err := resolver.AuthorityResourcePath("proof/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if target != "/workspace/proof/file.txt" || root != "/workspace" {
		t.Fatalf("authority resource = %q, %q", target, root)
	}
	for _, escaped := range []string{"", "/etc/passwd", "../outside", "dir/../../outside", "bad\x00path"} {
		if _, _, err := resolver.AuthorityResourcePath(escaped); err == nil {
			t.Errorf("AuthorityResourcePath(%q) accepted an invalid path", escaped)
		}
	}
}

func TestMissingSourceCheckoutFailsBeforeReadinessOrDaemonIO(t *testing.T) {
	client, err := New("unix:///run/unused-microvmd.sock")
	if err != nil {
		t.Fatal(err)
	}
	client.profile = "microvm-local"
	client.scope = "deployment"
	readinessCalls := 0
	client.readiness = func(context.Context) error {
		readinessCalls++
		return nil
	}

	if _, err := client.Bind(t.Context(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "deployment", Operation: server.PlacementOperationCreate}); !errors.Is(err, server.ErrInvalidPlacementBinding) {
		t.Fatalf("Bind error = %v, want ErrInvalidPlacementBinding", err)
	}
	ref := session.EnvironmentRef{Kind: kindMicroVM, ID: "session.environment", Revision: "1"}
	if _, err := client.Reattach(t.Context(), server.PlacementReattachRequest{Ref: ref, Scope: "deployment"}); !errors.Is(err, server.ErrInvalidPlacementBinding) {
		t.Fatalf("Reattach error = %v, want ErrInvalidPlacementBinding", err)
	}
	if readinessCalls != 0 {
		t.Fatalf("readiness calls = %d, want 0", readinessCalls)
	}

	noFS, err := client.Bind(t.Context(), server.PlacementBindRequest{Selector: server.NoFSPlacement(), Scope: "deployment", Operation: server.PlacementOperationCreate})
	if err != nil {
		t.Fatalf("no-fs Bind: %v", err)
	}
	if _, err := client.Reattach(t.Context(), server.PlacementReattachRequest{Ref: noFS.Ref, Scope: "deployment"}); err != nil {
		t.Fatalf("no-fs Reattach: %v", err)
	}
}

func TestNoFSBindBypassesMicroVMReadiness(t *testing.T) {
	readinessCalls := 0
	client, err := NewPlacementProvider("unix:///run/unused-microvmd.sock", "/source", "microvm-local", "deployment", func(context.Context) error {
		readinessCalls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := client.Bind(context.Background(), server.PlacementBindRequest{Selector: server.NoFSPlacement(), Scope: "deployment", Operation: server.PlacementOperationCreate})
	if err != nil {
		t.Fatal(err)
	}
	if readinessCalls != 0 {
		t.Fatalf("no-fs readiness calls = %d, want 0", readinessCalls)
	}
	if binding.GovernanceRoot != "" || binding.Environment.Workspace().Root() != "" {
		t.Fatalf("no-fs host/guest roots = %q/%q, want empty", binding.GovernanceRoot, binding.Environment.Workspace().Root())
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

func TestDeletePlacementUsesExactBindingAndReturnsOnlyRetention(t *testing.T) {
	socket := testUnixSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	seen := make(chan lifecycleRequest, 1)
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
		seen <- request
		_ = writeFrame(conn, lifecycleResponse{Payload: mustJSON(t, DeleteResult{WorktreePath: "/private/retained", WorktreeRetained: true})})
	}()
	client, err := NewPlacementProvider("unix://"+socket, "/source", "microvm-local", "deployment", nil)
	if err != nil {
		t.Fatal(err)
	}
	principal := &session.Principal{Subject: "alice"}
	ref := session.EnvironmentRef{Kind: kindMicroVM, ID: "placement.logical", Revision: "7"}
	result, err := client.DeletePlacement(t.Context(), server.PlacementDeleteRequest{Ref: ref, Principal: principal, Scope: "deployment"})
	if err != nil {
		t.Fatal(err)
	}
	request := <-seen
	if request.Operation != "delete" || request.Binding.SessionID != "placement" || request.Binding.EnvironmentID != "logical" || request.Binding.Generation != 7 {
		t.Fatalf("delete request = %+v", request)
	}
	if !result.Retained {
		t.Fatal("dirty retention was not returned")
	}
}
