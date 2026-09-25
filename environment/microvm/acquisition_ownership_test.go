package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/control/controltest"
	"github.com/stacklok/mecatl/environment/microvm/guestagent"
)

type ownershipTestGuest struct{ unregisters atomic.Int32 }

func (*ownershipTestGuest) Register(context.Context, RepositoryVMRecord, control.Binding, RepositoryGuestMount) (*guestagent.Services, error) {
	return nil, errors.New("unexpected registration")
}
func (g *ownershipTestGuest) Unregister(context.Context, RepositoryVMRecord, control.Binding) error {
	g.unregisters.Add(1)
	return nil
}

type ownershipFixture struct {
	daemon   *Daemon
	binding  control.Binding
	guest    *ownershipTestGuest
	checkout string
}

func newOwnershipFixture(t *testing.T) ownershipFixture {
	t.Helper()
	binding := control.Binding{Owner: "operator", SessionID: "session", EnvironmentID: "logical-owner", Ref: "logical-owner@7", Generation: 7}
	ref := session.EnvironmentRef{Kind: session.EnvironmentKind(Kind), ID: binding.Ref}
	guest := &ownershipTestGuest{}
	logical := &LogicalEnvironment{Binding: binding, Ref: EnvironmentRef{Kind: Kind, ID: binding.Ref}, guest: guest}
	env := tool.MustEnvironment(ref, nofs.New(), memledger.New(), nil)
	attachment := &RepositoryAttachment{Logical: logical, Environment: env}
	manager := &RepositoryAttachmentManager{
		active:  map[session.EnvironmentRef]*repositoryChildAttachment{ref: {attachment: attachment}},
		records: make(map[string]*repositoryAttachmentRecord),
	}
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(DaemonConfig{Control: auth, RepositoryAttachments: manager})
	if err != nil {
		t.Fatal(err)
	}
	daemon.repositoryBindings[binding.Ref] = repositoryDaemonBinding{binding: binding}
	return ownershipFixture{daemon: daemon, binding: binding, guest: guest}
}

type retainedTestOwner struct {
	conn net.Conn
	id   string
}

func acquireTestOwner(t *testing.T, fixture ownershipFixture) retainedTestOwner {
	t.Helper()
	server, client := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- fixture.daemon.ServeConn(t.Context(), server)
		_ = server.Close()
	}()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	checkout := fixture.checkout
	if checkout == "" {
		checkout = "/repository"
	}
	request := LifecycleRequest{
		Version: LifecycleProtocolVersion, Operation: LifecycleResolve, Binding: fixture.binding,
		Provision: &ProvisionRequest{Owner: fixture.binding.Owner, SessionID: fixture.binding.SessionID, Profile: "microvm-local", SourceCheckout: checkout},
	}
	if err := codec.Write(client, request); err != nil {
		t.Fatal(err)
	}
	var response LifecycleResponse
	if err := codec.Read(client, &response); err != nil {
		t.Fatal(err)
	}
	if response.Err != nil || response.ErrorCode != "" || response.Binding != fixture.binding {
		t.Fatalf("acquire response = %+v", response)
	}
	if len(response.AcquisitionID) != 32 {
		t.Fatalf("acquisition ID = %q, want 32 lowercase hex characters", response.AcquisitionID)
	}
	select {
	case err := <-done:
		t.Fatalf("retained acquisition handler exited before owner release: %v", err)
	default:
	}
	return retainedTestOwner{conn: client, id: response.AcquisitionID}
}

func closeTestOwner(t *testing.T, fixture ownershipFixture, owner retainedTestOwner) LifecycleResponse {
	t.Helper()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(owner.conn, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDetach, Binding: fixture.binding, AcquisitionID: owner.id}); err != nil {
		t.Fatal(err)
	}
	var response LifecycleResponse
	if err := codec.Read(owner.conn, &response); err != nil {
		t.Fatal(err)
	}
	_ = owner.conn.Close()
	return response
}

func TestMicroVMAcquisitionOwnershipIsExactAndIdempotent(t *testing.T) {
	fixture := newOwnershipFixture(t)
	first := acquireTestOwner(t, fixture)
	second := acquireTestOwner(t, fixture)
	if first.id == second.id {
		t.Fatal("distinct acquisitions reused one ID")
	}

	wrongServer, wrongClient := net.Pipe()
	wrongDone := make(chan error, 1)
	go func() { wrongDone <- fixture.daemon.ServeConn(t.Context(), wrongServer) }()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(wrongClient, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDetach, Binding: fixture.binding, AcquisitionID: first.id}); err != nil {
		t.Fatal(err)
	}
	var rejected LifecycleResponse
	if err := codec.Read(wrongClient, &rejected); err != nil {
		t.Fatal(err)
	}
	_ = wrongClient.Close()
	if rejected.ErrorCode != "binding_mismatch" {
		t.Fatalf("wrong-connection release = %+v, want binding_mismatch", rejected)
	}
	if err := <-wrongDone; err == nil {
		t.Fatal("wrong-connection release unexpectedly succeeded")
	}

	if response := closeTestOwner(t, fixture, first); response.ErrorCode != "" || fixture.guest.unregisters.Load() != 0 {
		t.Fatalf("first release response=%+v unregisters=%d", response, fixture.guest.unregisters.Load())
	}
	if response := closeTestOwner(t, fixture, second); response.ErrorCode != "" || fixture.guest.unregisters.Load() != 1 {
		t.Fatalf("final release response=%+v unregisters=%d", response, fixture.guest.unregisters.Load())
	}
	_ = second.conn.Close()
	if fixture.guest.unregisters.Load() != 1 {
		t.Fatal("duplicate local release repeated final detach")
	}

	for _, tc := range []struct {
		name   string
		mutate func(*LifecycleRequest)
	}{
		{name: "unknown ID", mutate: func(request *LifecycleRequest) { request.AcquisitionID = "ffffffffffffffffffffffffffffffff" }},
		{name: "wrong ref", mutate: func(request *LifecycleRequest) { request.Binding.Ref = "logical-other@7" }},
		{name: "wrong owner", mutate: func(request *LifecycleRequest) { request.Binding.Owner = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newOwnershipFixture(t)
			bad := acquireTestOwner(t, fixture)
			survivor := acquireTestOwner(t, fixture)
			request := LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDetach, Binding: fixture.binding, AcquisitionID: bad.id}
			tc.mutate(&request)
			codec := control.NewCodec(control.DefaultMaxMessageBytes)
			if err := codec.Write(bad.conn, request); err != nil {
				t.Fatal(err)
			}
			var response LifecycleResponse
			if err := codec.Read(bad.conn, &response); err != nil {
				t.Fatal(err)
			}
			_ = bad.conn.Close()
			if response.ErrorCode != "binding_mismatch" || fixture.guest.unregisters.Load() != 0 {
				t.Fatalf("malformed release response=%+v unregisters=%d", response, fixture.guest.unregisters.Load())
			}
			if response := closeTestOwner(t, fixture, survivor); response.ErrorCode != "" || fixture.guest.unregisters.Load() != 1 {
				t.Fatalf("survivor response=%+v unregisters=%d", response, fixture.guest.unregisters.Load())
			}
		})
	}

	pinFixture := newOwnershipFixture(t)
	pinned := acquireTestOwner(t, pinFixture)
	unpin, err := pinFixture.daemon.pinAcquisition(pinFixture.binding, pinned.id)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan LifecycleResponse, 1)
	go func() {
		codec := control.NewCodec(control.DefaultMaxMessageBytes)
		_ = codec.Write(pinned.conn, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDetach, Binding: pinFixture.binding, AcquisitionID: pinned.id})
		var response LifecycleResponse
		_ = codec.Read(pinned.conn, &response)
		closed <- response
	}()
	deadline := time.After(time.Second)
	for {
		pinFixture.daemon.ownershipMu.Lock()
		phase := pinFixture.daemon.refOwnership[pinFixture.binding].phase
		pinFixture.daemon.ownershipMu.Unlock()
		if phase == refPhaseDetaching {
			break
		}
		select {
		case <-deadline:
			t.Fatal("owner release did not begin while operation was pinned")
		default:
		}
	}
	if pinFixture.guest.unregisters.Load() != 0 {
		t.Fatal("owner release cancelled or detached a pinned operation")
	}
	unpin()
	if response := <-closed; response.ErrorCode != "" || pinFixture.guest.unregisters.Load() != 1 {
		t.Fatalf("pinned final release response=%+v unregisters=%d", response, pinFixture.guest.unregisters.Load())
	}
	_ = pinned.conn.Close()
}

func TestMicroVMOwnerDisconnectReclaimsRegistration(t *testing.T) {
	if endpoint := os.Getenv("MECATL_TEST_MICROVM_OWNER_ENDPOINT"); endpoint != "" {
		conn, err := net.Dial("unix", endpoint)
		if err != nil {
			t.Fatal(err)
		}
		codec := control.NewCodec(control.DefaultMaxMessageBytes)
		binding := control.Binding{Owner: "operator", SessionID: "session", EnvironmentID: "logical-owner", Ref: "logical-owner@7", Generation: 7}
		if err := codec.Write(conn, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleResolve, Binding: binding, Provision: &ProvisionRequest{Owner: "operator", SessionID: "session", SourceCheckout: "/repository"}}); err != nil {
			t.Fatal(err)
		}
		var response LifecycleResponse
		if err := codec.Read(conn, &response); err != nil || len(response.AcquisitionID) != 32 {
			t.Fatalf("child acquisition response=%+v err=%v", response, err)
		}
		return
	}

	fixture := newOwnershipFixture(t)
	first := acquireTestOwner(t, fixture)
	second := acquireTestOwner(t, fixture)
	if err := first.conn.Close(); err != nil {
		t.Fatal(err)
	}
	if fixture.guest.unregisters.Load() != 0 {
		t.Fatal("owner EOF detached a sibling acquisition")
	}
	if response := closeTestOwner(t, fixture, second); response.ErrorCode != "" {
		t.Fatalf("surviving owner release = %+v", response)
	}
	deadline := time.After(time.Second)
	for fixture.guest.unregisters.Load() != 1 {
		select {
		case <-deadline:
			t.Fatalf("final registration was not reclaimed: %d", fixture.guest.unregisters.Load())
		default:
		}
	}

	processFixture := newOwnershipFixture(t)
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	fdRoot := "/proc/self/fd"
	if runtime.GOOS == "darwin" {
		fdRoot = "/dev/fd"
	}
	parentSocket := filepath.Join(fdRoot, strconv.Itoa(int(dir.Fd())), "owner.sock")
	listener, err := net.Listen("unix", parentSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serveDone := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serveDone <- acceptErr
			return
		}
		defer conn.Close()
		serveDone <- processFixture.daemon.ServeConn(t.Context(), conn)
	}()
	childSocket := filepath.Join(fdRoot, "3", "owner.sock")
	command := exec.Command(os.Args[0], "-test.run=^TestMicroVMOwnerDisconnectReclaimsRegistration$")
	command.ExtraFiles = []*os.File{dir}
	command.Env = append(os.Environ(), "MECATL_TEST_MICROVM_OWNER_ENDPOINT="+childSocket)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("owner subprocess: %v\n%s", err, output)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("owner process disconnect: %v", err)
	}
	if processFixture.guest.unregisters.Load() != 1 {
		t.Fatalf("owner process exit left registration: %d", processFixture.guest.unregisters.Load())
	}
}

func TestMicroVMDeleteRejectsLiveOtherAcquisitions(t *testing.T) {
	fixture := newOwnershipFixture(t)
	first := acquireTestOwner(t, fixture)
	second := acquireTestOwner(t, fixture)
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(first.conn, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDelete, Binding: fixture.binding, AcquisitionID: first.id}); err != nil {
		t.Fatal(err)
	}
	var response LifecycleResponse
	if err := codec.Read(first.conn, &response); err != nil {
		t.Fatal(err)
	}
	if response.ErrorCode != "in_use" {
		t.Fatalf("delete with sibling acquisition = %+v, want in_use", response)
	}
	if fixture.guest.unregisters.Load() != 0 {
		t.Fatal("in-use delete detached the surviving owner")
	}
	if response := closeTestOwner(t, fixture, second); response.ErrorCode != "" || fixture.guest.unregisters.Load() != 1 {
		t.Fatalf("survivor response=%+v unregisters=%d", response, fixture.guest.unregisters.Load())
	}
}

func TestMicroVMAcquisitionCancellationAndLostRepliesReleaseOnlyOwner(t *testing.T) {
	fixture := newOwnershipFixture(t)
	lostReply := acquireTestOwner(t, fixture)
	survivor := acquireTestOwner(t, fixture)
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(lostReply.conn, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDetach, Binding: fixture.binding, AcquisitionID: lostReply.id}); err != nil {
		t.Fatal(err)
	}
	_ = lostReply.conn.Close()
	if fixture.guest.unregisters.Load() != 0 {
		t.Fatal("lost release reply detached another acquisition")
	}
	if response := closeTestOwner(t, fixture, survivor); response.ErrorCode != "" {
		t.Fatalf("surviving acquisition release = %+v", response)
	}
	deadline := time.After(time.Second)
	for fixture.guest.unregisters.Load() != 1 {
		select {
		case <-deadline:
			t.Fatalf("lost-reply owner was not retired: unregisters=%d", fixture.guest.unregisters.Load())
		default:
		}
	}

	for _, tc := range []struct {
		name      string
		premature bool
	}{
		{name: "failed acquisition reply"},
		{name: "premature terminal frame", premature: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newOwnershipFixture(t)
			survivor := acquireTestOwner(t, fixture)
			server, client := net.Pipe()
			done := make(chan error, 1)
			go func() { done <- fixture.daemon.ServeConn(t.Context(), server) }()
			codec := control.NewCodec(control.DefaultMaxMessageBytes)
			request := LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleResolve, Binding: fixture.binding, Provision: &ProvisionRequest{Owner: fixture.binding.Owner, SessionID: fixture.binding.SessionID, SourceCheckout: "/repository"}}
			if err := codec.Write(client, request); err != nil {
				t.Fatal(err)
			}
			if tc.premature {
				if err := codec.Write(client, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDelete, Binding: fixture.binding}); err != nil {
					t.Fatal(err)
				}
				var response LifecycleResponse
				_ = codec.Read(client, &response)
			}
			_ = client.Close()
			<-done
			if fixture.guest.unregisters.Load() != 0 {
				t.Fatal("failed acquisition exchange detached the surviving owner")
			}
			if response := closeTestOwner(t, fixture, survivor); response.ErrorCode != "" || fixture.guest.unregisters.Load() != 1 {
				t.Fatalf("survivor response=%+v unregisters=%d", response, fixture.guest.unregisters.Load())
			}
		})
	}
}

func TestMicroVMOwnershipRestartPreservesExactWorktree(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attachPersisted(t)
	binding := fixture.composition.Attachments.records[attachment.Environment.Ref().ID].binding
	guestRoot := attachment.Logical.WorktreePath
	if err := os.WriteFile(filepath.Join(guestRoot, "ownership-restart"), []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fixture.composition.Attachments.Detach(attachment.Environment.Ref()); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewRepositoryComposition(fixture.stateRoot, fixture.runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	reattached, err := restarted.Attachments.Reattach(t.Context(), LogicalEnvironmentRequest{Owner: binding.Owner, Checkout: fixture.repository}, attachment.Environment.Ref())
	if err != nil {
		t.Fatal(err)
	}
	defer reattached.Logical.Detach()
	if reattached.Environment.Ref() != attachment.Environment.Ref() || reattached.Logical.WorktreePath != guestRoot {
		t.Fatalf("restart replaced exact placement: ref=%+v path=%q", reattached.Environment.Ref(), reattached.Logical.WorktreePath)
	}
	if got, err := os.ReadFile(filepath.Join(reattached.Logical.WorktreePath, "ownership-restart")); err != nil || string(got) != "durable" {
		t.Fatalf("restart lost durable worktree: %q, %v", got, err)
	}
}

func TestMicroVMFinalDetachFailureCannotCloseReplacement(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attachPersisted(t)
	binding := fixture.composition.Attachments.records[attachment.Environment.Ref().ID].binding
	marker := filepath.Join(attachment.Logical.WorktreePath, "pending-teardown-marker")
	if err := os.WriteFile(marker, []byte("replacement-usable"), 0o600); err != nil {
		t.Fatal(err)
	}
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(DaemonConfig{Control: auth, RepositoryAttachments: fixture.composition.Attachments})
	if err != nil {
		t.Fatal(err)
	}
	daemon.repositoryBindings[binding.Ref] = repositoryDaemonBinding{binding: binding}
	owned := ownershipFixture{daemon: daemon, binding: binding, checkout: fixture.repository}
	owner := acquireTestOwner(t, owned)

	fixture.backend.mu.Lock()
	fixture.backend.dropNextControlResponse = true
	fixture.backend.mu.Unlock()
	if response := closeTestOwner(t, owned, owner); response.ErrorCode == "" {
		t.Fatalf("lost final unregister acknowledgement reported success: %+v", response)
	}

	// The first replacement resolve reconciles the pending teardown before it can
	// publish a new owner. The guest replay cache makes the retry authoritative.
	replacement := acquireTestOwner(t, owned)
	payload, err := json.Marshal(struct {
		Operation string `json:"operation"`
		Path      string `json:"path"`
	}{Operation: "read", Path: "pending-teardown-marker"})
	if err != nil {
		t.Fatal(err)
	}
	serverConn, clientConn := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- daemon.ServeConn(t.Context(), serverConn) }()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(clientConn, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleWorkspace, Binding: binding, AcquisitionID: replacement.id, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	var response LifecycleResponse
	if err := codec.Read(clientConn, &response); err != nil {
		t.Fatal(err)
	}
	_ = clientConn.Close()
	if err := <-done; err != nil || response.ErrorCode != "" {
		t.Fatalf("replacement workspace operation = %+v, %v", response, err)
	}
	var read struct {
		Data []byte `json:"data"`
	}
	if err := json.Unmarshal(response.Payload, &read); err != nil || string(read.Data) != "replacement-usable" {
		t.Fatalf("replacement workspace payload = %q, %v", read.Data, err)
	}
	if response := closeTestOwner(t, owned, replacement); response.ErrorCode != "" {
		t.Fatalf("replacement final close = %+v", response)
	}
}

func TestForkOperationPinEndsBeforeChildOwnerLifetime(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attachPersisted(t)
	binding := fixture.composition.Attachments.records[attachment.Environment.Ref().ID].binding
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(DaemonConfig{Control: auth, RepositoryAttachments: fixture.composition.Attachments})
	if err != nil {
		t.Fatal(err)
	}
	daemon.repositoryBindings[binding.Ref] = repositoryDaemonBinding{binding: binding}
	ownedFixture := ownershipFixture{daemon: daemon, binding: binding}
	parent := acquireTestOwner(t, ownedFixture)

	serverConn, childConn := net.Pipe()
	childDone := make(chan error, 1)
	go func() {
		childDone <- daemon.ServeConn(t.Context(), serverConn)
		_ = serverConn.Close()
	}()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	payload, err := json.Marshal(ChildForkPayload{Label: "pin-scope"})
	if err != nil {
		t.Fatal(err)
	}
	if err := codec.Write(childConn, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleFork, Binding: binding, AcquisitionID: parent.id, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	var child LifecycleResponse
	if err := codec.Read(childConn, &child); err != nil || child.ErrorCode != "" || len(child.AcquisitionID) != 32 {
		t.Fatalf("fork response = %+v, %v", child, err)
	}
	daemon.ownershipMu.Lock()
	parentPins := daemon.refOwnership[binding].pins
	daemon.ownershipMu.Unlock()
	if parentPins != 0 {
		t.Fatalf("parent remained pinned for child owner lifetime: %d", parentPins)
	}
	if response := closeTestOwner(t, ownedFixture, parent); response.ErrorCode != "" {
		t.Fatalf("parent close while child retained = %+v", response)
	}
	if err := codec.Write(childConn, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleChildDelete, Binding: child.Binding, AcquisitionID: child.AcquisitionID}); err != nil {
		t.Fatal(err)
	}
	var deleted LifecycleResponse
	if err := codec.Read(childConn, &deleted); err != nil || deleted.ErrorCode != "" {
		t.Fatalf("child cleanup = %+v, %v", deleted, err)
	}
	_ = childConn.Close()
	if err := <-childDone; err != nil {
		t.Fatalf("child owner handler = %v", err)
	}
}

func TestConcurrentFirstResolvesSerializeRuntimeRegistration(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attachPersisted(t)
	binding := fixture.composition.Attachments.records[attachment.Environment.Ref().ID].binding
	if err := fixture.composition.Attachments.Detach(attachment.Environment.Ref()); err != nil {
		t.Fatal(err)
	}
	auth, err := control.NewService(control.ServiceConfig{AccountUID: 1000, PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(DaemonConfig{Control: auth, RepositoryAttachments: fixture.composition.Attachments})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		conn     net.Conn
		response LifecycleResponse
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var handlers sync.WaitGroup
	for range 2 {
		serverConn, clientConn := net.Pipe()
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			_ = daemon.ServeConn(t.Context(), serverConn)
			_ = serverConn.Close()
		}()
		go func() {
			<-start
			codec := control.NewCodec(control.DefaultMaxMessageBytes)
			request := LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleResolve, Binding: binding, Provision: &ProvisionRequest{Owner: binding.Owner, SessionID: binding.SessionID, SourceCheckout: fixture.repository}}
			if writeErr := codec.Write(clientConn, request); writeErr != nil {
				results <- result{conn: clientConn, err: writeErr}
				return
			}
			var response LifecycleResponse
			readErr := codec.Read(clientConn, &response)
			results <- result{conn: clientConn, response: response, err: readErr}
		}()
	}
	close(start)
	owners := make([]retainedTestOwner, 0, 2)
	for range 2 {
		got := <-results
		if got.err != nil || got.response.ErrorCode != "" || got.response.Binding != binding || len(got.response.AcquisitionID) != 32 {
			t.Fatalf("concurrent first resolve = %+v, %v", got.response, got.err)
		}
		owners = append(owners, retainedTestOwner{conn: got.conn, id: got.response.AcquisitionID})
	}
	ownedFixture := ownershipFixture{daemon: daemon, binding: binding}
	if response := closeTestOwner(t, ownedFixture, owners[0]); response.ErrorCode != "" {
		t.Fatalf("first concurrent owner release = %+v", response)
	}
	if response := closeTestOwner(t, ownedFixture, owners[1]); response.ErrorCode != "" {
		t.Fatalf("second concurrent owner release = %+v", response)
	}
	handlers.Wait()
}

func TestMicroVMLifecycleV4RejectsUnsafeLegacyOwnership(t *testing.T) {
	fixture := newOwnershipFixture(t)
	server, client := net.Pipe()
	stateless := fixture.daemon.Handle(t.Context(), server, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleResolve, Binding: fixture.binding})
	_ = server.Close()
	_ = client.Close()
	if stateless.ErrorCode == "" {
		t.Fatal("stateless Handle fabricated a retained acquisition")
	}
	for _, request := range []LifecycleRequest{
		{Version: 3, Operation: LifecycleResolve, Binding: fixture.binding, Provision: &ProvisionRequest{Owner: "operator", SessionID: "session", SourceCheckout: "/repository"}},
		{Version: LifecycleProtocolVersion, Operation: LifecycleDetach, Binding: fixture.binding},
	} {
		server, client := net.Pipe()
		done := make(chan error, 1)
		go func() { done <- fixture.daemon.ServeConn(t.Context(), server) }()
		codec := control.NewCodec(control.DefaultMaxMessageBytes)
		if err := codec.Write(client, request); err != nil {
			t.Fatal(err)
		}
		var response LifecycleResponse
		if err := codec.Read(client, &response); err != nil && !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
		_ = client.Close()
		if response.ErrorCode == "" {
			t.Fatalf("unsafe legacy request succeeded: %+v", request)
		}
		if err := <-done; err == nil {
			t.Fatalf("unsafe legacy request returned nil: %+v", request)
		}
	}
	if fixture.guest.unregisters.Load() != 0 {
		t.Fatal("malformed ownership request mutated registration")
	}
}
