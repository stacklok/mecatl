package microvm

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/control/controltest"
)

func repairOwnershipFixture(t *testing.T) (*repositoryAttachmentFixture, *RepositoryAttachment, ownershipFixture) {
	t.Helper()
	f := newRepositoryAttachmentFixture(t)
	a := f.attachPersisted(t)
	b := f.composition.Attachments.records[a.Environment.Ref().ID].binding
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDaemon(DaemonConfig{Control: auth, RepositoryAttachments: f.composition.Attachments})
	if err != nil {
		t.Fatal(err)
	}
	d.repositoryBindings[b.Ref] = repositoryDaemonBinding{binding: b}
	return f, a, ownershipFixture{daemon: d, binding: b, checkout: f.repository}
}

func repairExchange(t *testing.T, d *Daemon, request LifecycleRequest) LifecycleResponse {
	t.Helper()
	s, c := net.Pipe()
	defer c.Close()
	done := make(chan error, 1)
	go func() { defer s.Close(); done <- d.ServeConn(t.Context(), s) }()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(c, request); err != nil {
		t.Fatal(err)
	}
	var response LifecycleResponse
	var output []byte
	for {
		if err := codec.Read(c, &response); err != nil {
			t.Fatal(err)
		}
		if response.Stream == nil {
			break
		}
		output = append(output, response.Stream.Data...)
		response = LifecycleResponse{}
	}
	if len(output) != 0 {
		response.Stream = &LifecycleExecStream{Data: output}
	}
	c.Close()
	<-done
	return response
}

func TestResolveRejectsCompleteBindingBeforeMutation(t *testing.T) {
	for _, detached := range []bool{false, true} {
		for _, field := range []string{"session", "root"} {
			t.Run(field+map[bool]string{false: "/active", true: "/detached"}[detached], func(t *testing.T) {
				f, _, o := repairOwnershipFixture(t)
				dir, id, err := attachmentStoreLocation(f.composition.Attachments.records[o.binding.Ref], false)
				if err != nil {
					t.Fatal(err)
				}
				metadata := filepath.Join(dir, id+".json")
				before, err := os.ReadFile(metadata)
				if err != nil {
					t.Fatal(err)
				}
				var owner retainedTestOwner
				if _, err := o.daemon.repositoryOperation(t.Context(), LifecycleRequest{Operation: LifecycleDetach, Binding: o.binding}); err != nil {
					t.Fatal(err)
				}
				if !detached {
					owner = acquireTestOwner(t, o)
					defer owner.conn.Close()
				}
				wrong := o.binding
				if field == "session" {
					wrong.SessionID += "-forged"
				} else {
					wrong.AssignedRoot = "/workspace/forged"
				}
				response := repairExchange(t, o.daemon, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleResolve, Binding: wrong, Provision: &ProvisionRequest{Owner: wrong.Owner, SessionID: wrong.SessionID, SourceCheckout: f.repository}})
				if response.ErrorCode != "binding_mismatch" {
					t.Errorf("wrong complete binding response = %+v", response)
				}
				after, err := os.ReadFile(metadata)
				if err != nil || !bytes.Equal(before, after) {
					t.Errorf("resolve changed durable metadata: %v", err)
				}
				if !detached {
					response = repairExchange(t, o.daemon, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleExec, Binding: o.binding, AcquisitionID: owner.id, Payload: []byte(`{"command":"printf owner-still-works"}`)})
					if response.ErrorCode != "" || response.Stream == nil || !bytes.Contains(response.Stream.Data, []byte("owner-still-works")) {
						t.Errorf("legitimate owner stopped working: %+v", response)
					}
					if closed := closeTestOwner(t, o, owner); closed.ErrorCode != "" {
						t.Errorf("legitimate owner cleanup failed: %+v", closed)
					}
				}
			})
		}
	}
}

func TestActiveResolveReservationSurvivesLastOwnerClose(t *testing.T) {
	_, _, o := repairOwnershipFixture(t)
	old := acquireTestOwner(t, o)
	prepared, resume := make(chan struct{}), make(chan struct{})
	o.daemon.beforeAcquisitionPublish = func(context.Context) { close(prepared); <-resume }
	s, c := net.Pipe()
	defer c.Close()
	done := make(chan error, 1)
	go func() { done <- o.daemon.ServeConn(t.Context(), s) }()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(c, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleResolve, Binding: o.binding}); err != nil {
		t.Fatal(err)
	}
	<-prepared
	// Pause exactly after preparing the response, before publication.
	if got := closeTestOwner(t, o, old); got.ErrorCode != "" {
		t.Fatal(got.ErrorText)
	}
	registered := o.daemon.repositoryAttachment(o.binding) != nil
	close(resume)
	var response LifecycleResponse
	if err := codec.Read(c, &response); err != nil {
		t.Fatal(err)
	}
	if !registered {
		t.Error("active resolve was not reserved through publication; last owner detached registration")
	}
	got := repairExchange(t, o.daemon, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleExec, Binding: o.binding, AcquisitionID: response.AcquisitionID, Payload: []byte(`{"command":"printf new-owner-works"}`)})
	if got.ErrorCode != "" || got.Stream == nil || !bytes.Contains(got.Stream.Data, []byte("new-owner-works")) {
		t.Errorf("published unusable binding: %+v", got)
	}
	closeTestOwner(t, o, retainedTestOwner{conn: c, id: response.AcquisitionID})
	<-done
}

func TestAbandonedResolveUnregistersWithoutDeletingWorktree(t *testing.T) {
	_, a, o := repairOwnershipFixture(t)
	if _, err := o.daemon.repositoryOperation(t.Context(), LifecycleRequest{Operation: LifecycleDetach, Binding: o.binding}); err != nil {
		t.Fatal(err)
	}
	prepared := make(chan struct{})
	o.daemon.beforeAcquisitionPublish = func(ctx context.Context) { close(prepared); <-ctx.Done() }
	s, c := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- o.daemon.ServeConn(t.Context(), s) }()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(c, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleResolve, Binding: o.binding, Provision: &ProvisionRequest{Owner: o.binding.Owner, SessionID: o.binding.SessionID, SourceCheckout: o.checkout}}); err != nil {
		t.Fatal(err)
	}
	<-prepared
	c.Close()
	if err := <-done; err == nil {
		t.Error("abandoned exchange unexpectedly succeeded")
	}
	o.daemon.ownershipMu.Lock()
	owners, refs := len(o.daemon.acquisitions), len(o.daemon.refOwnership)
	o.daemon.ownershipMu.Unlock()
	if owners != 0 || refs != 0 {
		t.Errorf("abandoned acquisition retained ownership state: %d/%d", owners, refs)
	}
	if o.daemon.repositoryAttachment(o.binding) != nil || o.daemon.repositoryAttachments.lookup(a.Environment.Ref()) != nil {
		t.Fatal("abandoned resolve left an ownerless live registration")
	}
	if _, err := os.Stat(a.Logical.MetadataPath); err != nil {
		t.Fatalf("abandon removed durable metadata: %v", err)
	}
	if _, err := os.Stat(a.Logical.WorktreePath); err != nil {
		t.Fatalf("abandon deleted worktree: %v", err)
	}
}

func TestAbandonedActiveBorrowPreservesExecutionOwner(t *testing.T) {
	_, _, o := repairOwnershipFixture(t)
	owner := acquireTestOwner(t, o)
	defer owner.conn.Close()
	prepared := make(chan struct{})
	o.daemon.beforeAcquisitionPublish = func(ctx context.Context) { close(prepared); <-ctx.Done() }
	s, c := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- o.daemon.ServeConn(t.Context(), s) }()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(c, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleResolve, Binding: o.binding}); err != nil {
		t.Fatal(err)
	}
	<-prepared
	c.Close()
	<-done
	o.daemon.ownershipMu.Lock()
	state := o.daemon.refOwnership[o.binding.Ref]
	preserved := state != nil && len(state.owners) == 1 && state.resolves == 0 && len(o.daemon.acquisitions) == 1
	o.daemon.ownershipMu.Unlock()
	if !preserved {
		t.Error("failed source borrow changed execution ownership")
	}
	got := repairExchange(t, o.daemon, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleExec, Binding: o.binding, AcquisitionID: owner.id, Payload: []byte(`{"command":"printf execution-owner-works"}`)})
	if got.ErrorCode != "" || got.Stream == nil || !bytes.Contains(got.Stream.Data, []byte("execution-owner-works")) {
		t.Errorf("failed source borrow revoked execution: %+v", got)
	}
	if closed := closeTestOwner(t, o, owner); closed.ErrorCode != "" {
		t.Errorf("execution owner cleanup: %+v", closed)
	}
}

func TestResolveGateWaitCancelledByOwnerEOF(t *testing.T) {
	_, a, o := repairOwnershipFixture(t)
	defer a.Logical.Detach()
	if err := o.daemon.beginUnownedDeletion(o.binding); err != nil {
		t.Fatal(err)
	}
	s, c := net.Pipe()
	defer s.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- o.daemon.ServeConn(ctx, s) }()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(c, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleResolve, Binding: o.binding}); err != nil {
		t.Fatal(err)
	}
	c.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("EOF did not cancel resolve admission waiter while delete gate remained held")
	}
	o.daemon.ownershipMu.Lock()
	state := o.daemon.refOwnership[o.binding.Ref]
	deleting := state != nil && state.phase == refPhaseDeleting
	o.daemon.ownershipMu.Unlock()
	if !deleting {
		t.Fatal("dead waiter changed the other operation's deletion gate")
	}
}

func TestPublicationRejectsRemovedRegistration(t *testing.T) {
	_, _, o := repairOwnershipFixture(t)
	// A new ref is inventory-visible before its create/fork reply is published.
	if got := repairExchange(t, o.daemon, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDelete, Binding: o.binding}); got.ErrorCode != "" {
		t.Fatal(got.ErrorText)
	}
	s, c := net.Pipe()
	defer s.Close()
	defer c.Close()
	if _, err := o.daemon.publishAcquisition(s, o.binding); !errors.Is(err, control.ErrBindingMismatch) {
		t.Fatalf("published ownership for an already removed registration: %v", err)
	}
	if len(o.daemon.acquisitions) != 0 || len(o.daemon.refOwnership) != 0 {
		t.Fatal("failed publication left ownership state")
	}
}

func TestPendingDeleteExactRetry(t *testing.T) {
	f, a, o := repairOwnershipFixture(t)
	f.backend.mu.Lock()
	f.backend.dropNextControlResponse = true
	f.backend.mu.Unlock()
	request := LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDelete, Binding: o.binding}
	if got := repairExchange(t, o.daemon, request); got.ErrorCode == "" {
		t.Fatal("injected lost unregister acknowledgement succeeded")
	}
	if _, err := o.daemon.beginResolve(t.Context(), o.binding); !errors.Is(err, control.ErrBindingMismatch) {
		t.Fatalf("pending destructive outcome admitted resolve: %v", err)
	}
	if got := repairExchange(t, o.daemon, request); got.ErrorCode != "" {
		t.Fatalf("explicit exact retry rejected: %+v", got)
	}
	if _, err := os.Stat(a.Logical.MetadataPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful retry retained metadata: %v", err)
	}
	if got := repairExchange(t, o.daemon, request); got.ErrorCode != "" {
		t.Fatalf("idempotent retry: %+v", got)
	}
}
