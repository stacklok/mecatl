package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestPlacementBorrowReleasesOnlyItsOwnOwnership(t *testing.T) {
	client, err := New("unix:///unused")
	if err != nil {
		t.Fatal(err)
	}
	ref := session.EnvironmentRef{Kind: kindMicroVM, ID: "placement.logical", Revision: "7"}
	claim := binding{Owner: "local", SessionID: "placement", EnvironmentID: "logical", Ref: "logical@7", Generation: 7}
	detaches := 0
	detach := func() error { detaches++; return nil }
	execution := client.placementBinding(ref, claim, detach, nil)
	source := client.placementBinding(ref, claim, detach, nil)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if detaches != 0 {
		t.Fatalf("source release detached execution owner %d times", detaches)
	}
	if err := execution.Close(); err != nil {
		t.Fatal(err)
	}
	if detaches != 1 {
		t.Fatalf("final owner detached %d times, want 1", detaches)
	}
}

func TestReattachSerializesWithLastDetach(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	listener, err := net.Listen("unix", testUnixSocketPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var active, firstDetach atomic.Bool
	var resolves atomic.Int32
	detaching, finishDetach := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-finishDetach:
		default:
			close(finishDetach)
		}
	}()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var req lifecycleRequest
				if readFrame(conn, &req) != nil {
					return
				}
				resp := lifecycleResponse{Binding: req.Binding}
				switch req.Operation {
				case "resolve":
					resolves.Add(1)
					active.Store(true)
				case "detach":
					if firstDetach.CompareAndSwap(false, true) {
						close(detaching)
						<-finishDetach
					}
					active.Store(false)
				case "workspace":
					if !active.Load() {
						resp.ErrorCode = "not_found"
					} else {
						resp.Payload, _ = json.Marshal(workspaceResponse{Data: []byte("retained")})
					}
				}
				_ = writeFrame(conn, resp)
			}()
		}
	}()
	client, err := NewPlacementProvider("unix://"+listener.Addr().String(), "/source", "microvm-local", "deployment", nil)
	if err != nil {
		t.Fatal(err)
	}
	req := server.PlacementReattachRequest{Ref: session.EnvironmentRef{Kind: kindMicroVM, ID: "placement.logical", Revision: "7"}, Scope: "deployment"}
	first, err := client.Reattach(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- first.Close() }()
	select {
	case <-detaching:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	acquired := make(chan server.PlacementBinding, 1)
	errs := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		binding, err := client.Reattach(ctx, req)
		if err != nil {
			errs <- err
			return
		}
		acquired <- binding
	}()
	<-started
	select {
	case <-acquired:
		t.Fatal("reattach published while old detach can still invalidate it")
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(50 * time.Millisecond):
	}
	if resolves.Load() != 1 {
		t.Fatal("backend resolve crossed an in-flight final detach")
	}
	close(finishDetach)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	select {
	case next := <-acquired:
		defer next.Close()
		data, err := next.Environment.Workspace().Read(ctx, "marker")
		if err != nil || string(data) != "retained" {
			t.Fatalf("new borrow invalidated: %q, %v", data, err)
		}
	case err := <-errs:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestPlacementCleanupCachesFailureAndRollbackPreservesBorrowers(t *testing.T) {
	client, err := New("unix:///unused")
	if err != nil {
		t.Fatal(err)
	}
	claim := binding{Owner: "local", SessionID: "placement", EnvironmentID: "logical", Ref: "logical@7", Generation: 7}
	ref := refForBinding(claim)
	failure := errors.New("detach failed")
	calls := 0
	detach := func() error { calls++; return failure }
	first := client.placementBinding(ref, claim, detach, nil)
	if !errors.Is(first.Close(), failure) || !errors.Is(first.Close(), failure) || calls != 1 {
		t.Fatal("cleanup error was lost or cleanup repeated")
	}
	deleted := false
	owner := client.placementBinding(ref, claim, detach, func() error { deleted = true; return nil })
	borrow := client.placementBinding(ref, claim, detach, nil)
	if owner.Rollback() == nil || deleted {
		t.Fatal("rollback destroyed live source borrower")
	}
	if owner.Close() == nil {
		t.Fatal("rollback failure was not retained")
	}
	if !errors.Is(borrow.Close(), failure) || calls != 2 {
		t.Fatal("remaining owner did not retain final cleanup")
	}
}

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
