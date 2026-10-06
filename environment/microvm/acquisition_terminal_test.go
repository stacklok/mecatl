package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/control"
)

// Pause a real workspace operation after daemon admission, before guest dispatch.
// Retirement must not cancel that operation or unregister its guest.
type terminalWorkspace struct {
	tool.Workspace
	entered chan struct{}
	resume  chan struct{}
}

func (w *terminalWorkspace) ReadVersion(ctx context.Context, path string) ([]byte, tool.FileVersion, error) {
	close(w.entered)
	select {
	case <-w.resume:
		return w.Workspace.ReadVersion(ctx, path)
	case <-ctx.Done():
		return nil, tool.FileVersion{}, ctx.Err()
	}
}

func assertRetiredOwner(t *testing.T, o ownershipFixture, owner retainedTestOwner) {
	t.Helper()
	var extra LifecycleResponse
	if err := control.NewCodec(control.DefaultMaxMessageBytes).Read(owner.conn, &extra); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal owner socket did not close: %v", err)
	}
	_ = owner.conn.Close()
	o.daemon.ownershipMu.Lock()
	_, retained := o.daemon.acquisitions[owner.id]
	o.daemon.ownershipMu.Unlock()
	if retained {
		t.Fatal("terminal acquisition ID remained in daemon ownership table")
	}
	got := repairExchange(t, o.daemon, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleWorkspace, Binding: o.binding, AcquisitionID: owner.id, Payload: []byte(`{"operation":"read","path":"tracked.txt"}`)})
	if got.ErrorCode != "binding_mismatch" {
		t.Fatalf("terminal acquisition still authorizes operations: %+v", got)
	}
}

func testOwnedDeleteWithOperationPin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, a, o := repairOwnershipFixture(t)
		defer a.Logical.Detach()
		ws := &terminalWorkspace{Workspace: a.Environment.Workspace(), entered: make(chan struct{}), resume: make(chan struct{})}
		a.Environment = tool.MustEnvironment(a.Environment.Ref(), ws, a.Environment.ReadLedger(), a.Environment.CommandRunner())
		resume := sync.OnceFunc(func() { close(ws.resume) })
		defer resume()
		owner := acquireTestOwner(t, o)
		defer owner.conn.Close()
		operation := make(chan LifecycleResponse, 1)
		go func() {
			operation <- repairExchange(t, o.daemon, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleWorkspace, Binding: o.binding, AcquisitionID: owner.id, Payload: []byte(`{"operation":"read","path":"tracked.txt"}`)})
		}()
		<-ws.entered
		terminal := make(chan LifecycleResponse, 1)
		go func() {
			codec := control.NewCodec(control.DefaultMaxMessageBytes)
			if err := codec.Write(owner.conn, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDelete, Binding: o.binding, AcquisitionID: owner.id}); err != nil {
				t.Error(err)
			}
			var response LifecycleResponse
			if err := codec.Read(owner.conn, &response); err != nil {
				t.Error(err)
			}
			terminal <- response
		}()
		synctest.Wait()
		got := repairExchange(t, o.daemon, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleWorkspace, Binding: o.binding, AcquisitionID: owner.id, Payload: []byte(`{"operation":"read","path":"tracked.txt"}`)})
		if got.ErrorCode != "binding_mismatch" {
			t.Fatalf("pinned delete did not retire requesting ID: %+v", got)
		}
		if err := f.backend.server.Probe(a.Logical.Binding); err != nil {
			t.Fatalf("pinned delete unregistered the still-live operation: %v", err)
		}
		resume()
		got = <-operation
		var read proxyWorkspaceResponse
		if err := json.Unmarshal(got.Payload, &read); err != nil || got.ErrorCode != "" || string(read.Data) != "source\n" {
			t.Fatalf("retirement broke admitted workspace read: %+v, %v", got, err)
		}
		response := <-terminal
		if response.ErrorCode != "in_use" || response.Binding != o.binding || response.AcquisitionID != owner.id {
			t.Fatalf("sole owner delete with live pin = %+v, want exact terminal in_use", response)
		}
		assertRetiredOwner(t, o, owner)
		if err := f.backend.server.Probe(a.Logical.Binding); !errors.Is(err, control.ErrBindingMismatch) {
			t.Fatalf("pin drain did not unregister final owner: %v", err)
		}
		for _, path := range []string{a.Logical.MetadataPath, a.Logical.WorktreePath} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("in_use/EOF implicitly deleted durable state: %v", err)
			}
		}
	})
}

func testOwnedDeleteTerminal(t *testing.T, cleanupError bool) {
	f, a, o := repairOwnershipFixture(t)
	defer a.Logical.Detach()
	marker := filepath.Join(a.Logical.WorktreePath, "dirty-marker")
	if err := os.WriteFile(marker, []byte("retain me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := acquireTestOwner(t, o)
	defer owner.conn.Close()
	if cleanupError {
		f.backend.mu.Lock()
		f.backend.dropNextControlResponse = true
		f.backend.mu.Unlock()
	}
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(owner.conn, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDelete, Binding: o.binding, AcquisitionID: owner.id}); err != nil {
		t.Fatal(err)
	}
	var response LifecycleResponse
	if err := codec.Read(owner.conn, &response); err != nil {
		t.Fatal(err)
	}
	if response.Binding != o.binding || response.AcquisitionID != owner.id || (response.ErrorCode != "") != cleanupError {
		t.Fatalf("owned delete terminal = %+v (cleanup error=%v)", response, cleanupError)
	}
	if !cleanupError {
		var deleted LifecycleDeleteResult
		if err := json.Unmarshal(response.Payload, &deleted); err != nil || !deleted.WorktreeRetained || deleted.WorktreePath != a.Logical.WorktreePath {
			t.Fatalf("dirty outcome = %+v, %v", deleted, err)
		}
	}
	assertRetiredOwner(t, o, owner)
	if cleanupError {
		got := repairExchange(t, o.daemon, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleResolve, Binding: o.binding, Provision: &ProvisionRequest{Owner: o.binding.Owner, SessionID: o.binding.SessionID, SourceCheckout: o.checkout}})
		if got.ErrorCode != "binding_mismatch" || got.AcquisitionID != "" {
			t.Fatalf("uncertain destructive outcome admitted replacement: %+v", got)
		}
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "retain me\n" {
		t.Fatalf("terminal close/EOF removed retained work: %q, %v", data, err)
	}
	if _, err := os.Stat(a.Logical.MetadataPath); err != nil {
		t.Fatalf("terminal close/EOF removed durable metadata: %v", err)
	}
}

func testPrematureAcquisitionTerminal(t *testing.T) {
	f, a, o := repairOwnershipFixture(t)
	survivor := acquireTestOwner(t, o)
	defer survivor.conn.Close()
	o.daemon.repositoryProvisioner = func(_ context.Context, request ProvisionRequest) (RepositoryPlacement, error) {
		return RepositoryPlacement{Request: LogicalEnvironmentRequest{Owner: request.Owner, Checkout: f.repository, Artifacts: testArtifactSnapshot(f.verified)}, Status: EnforcedProfileStatus{Profile: "microvm-local"}}, nil
	}
	prepared := make(chan struct{})
	o.daemon.beforeAcquisitionPublish = func(ctx context.Context) { close(prepared); <-ctx.Done() }
	server, client := net.Pipe()
	defer client.Close()
	done := make(chan error, 1)
	go func() { done <- o.daemon.ServeConn(t.Context(), server) }()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	request := LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleCreate, Binding: control.Binding{Owner: o.binding.Owner, SessionID: "unpublished"}, Provision: &ProvisionRequest{Owner: o.binding.Owner, SessionID: "unpublished", SourceCheckout: f.repository, Profile: "microvm-local"}}
	if err := codec.Write(client, request); err != nil {
		t.Fatal(err)
	}
	<-prepared
	var provisional *RepositoryAttachment
	for _, record := range f.composition.Attachments.inventory(o.binding.Owner) {
		if record.binding.SessionID == "unpublished" {
			provisional = record.attachment
		}
	}
	if provisional == nil {
		t.Fatal("create did not reach unpublished provisional attachment")
	}
	// A real, live sibling ID cannot authorize any mutation on this acquiring
	// connection. The reader cancels the frozen attempt before publication.
	if err := codec.Write(client, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDelete, Binding: o.binding, AcquisitionID: survivor.id}); err != nil {
		t.Fatal(err)
	}
	var response LifecycleResponse
	if err := codec.Read(client, &response); err != nil {
		t.Fatal(err)
	}
	if response.ErrorCode != lifecycleErrorCode(errLifecycleProtocol) || response.AcquisitionID != "" || response.Created != nil {
		t.Fatalf("premature terminal was treated as an owned request: %+v", response)
	}
	if err := <-done; !errors.Is(err, errLifecycleProtocol) {
		t.Fatalf("acquiring exchange error = %v, want protocol violation", err)
	}
	for _, path := range []string{provisional.Logical.WorktreePath, provisional.Logical.MetadataPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("safely attributed unpublished create was not cleaned: %v", err)
		}
	}
	if err := f.backend.server.Probe(provisional.Logical.Binding); !errors.Is(err, control.ErrBindingMismatch) {
		t.Fatalf("provisional guest registration retained: %v", err)
	}
	got := repairExchange(t, o.daemon, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleWorkspace, Binding: o.binding, AcquisitionID: survivor.id, Payload: []byte(`{"operation":"read","path":"tracked.txt"}`)})
	var read proxyWorkspaceResponse
	if err := json.Unmarshal(got.Payload, &read); err != nil || got.ErrorCode != "" || string(read.Data) != "source\n" {
		t.Fatalf("premature requested deletion touched sibling: %+v, %v", got, err)
	}
	if err := f.backend.server.Probe(a.Logical.Binding); err != nil {
		t.Fatal(err)
	}
	if response := closeTestOwner(t, o, survivor); response.ErrorCode != "" {
		t.Fatal(response.ErrorText)
	}
}

type blockedReplyConn struct {
	net.Conn
	delivered chan struct{}
	resume    chan struct{}
	writes    int
}

func (c *blockedReplyConn) Write(p []byte) (int, error) {
	c.writes++
	n, err := c.Conn.Write(p)
	if c.writes == 1 {
		close(c.delivered)
		<-c.resume
	}
	return n, err
}

func testFastAcquisitionDetach(t *testing.T) {
	f, a, o := repairOwnershipFixture(t)
	server, client := net.Pipe()
	defer client.Close()
	conn := &blockedReplyConn{Conn: server, delivered: make(chan struct{}), resume: make(chan struct{})}
	resume := sync.OnceFunc(func() { close(conn.resume) })
	defer resume()
	done := make(chan error, 1)
	go func() { done <- o.daemon.ServeConn(t.Context(), conn) }()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(client, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleResolve, Binding: o.binding}); err != nil {
		t.Fatal(err)
	}
	var acquired LifecycleResponse
	if err := codec.Read(client, &acquired); err != nil || acquired.ErrorCode != "" || len(acquired.AcquisitionID) != 32 {
		t.Fatalf("acquisition reply = %+v, %v", acquired, err)
	}
	<-conn.delivered // The client has the reply, but daemon Write has not returned.
	if err := codec.Write(client, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDetach, Binding: acquired.Binding, AcquisitionID: acquired.AcquisitionID}); err != nil {
		t.Fatal(err)
	}
	if err := f.backend.server.Probe(a.Logical.Binding); err != nil {
		t.Fatalf("queued terminal dispatched before successful reply write: %v", err)
	}
	resume()
	var released LifecycleResponse
	if err := codec.Read(client, &released); err != nil || released.ErrorCode != "" || released.Binding != acquired.Binding || released.AcquisitionID != acquired.AcquisitionID {
		t.Fatalf("fast retained detach = %+v, %v", released, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if conn.writes != 2 {
		t.Fatalf("reply writes = %d, want acquisition and one terminal", conn.writes)
	}
	assertRetiredOwner(t, o, retainedTestOwner{conn: client, id: acquired.AcquisitionID})
	if err := f.backend.server.Probe(a.Logical.Binding); !errors.Is(err, control.ErrBindingMismatch) {
		t.Fatalf("fast detach retained guest registration: %v", err)
	}
}
