package microvm

import (
	"context"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stacklok/mecatl/environment/microvm/control"
)

type stallControlReply struct {
	net.Conn
	writes  int
	stalled chan struct{}
}

func (s *stallControlReply) Write(p []byte) (int, error) {
	s.writes++
	if s.writes == 2 {
		close(s.stalled)
		// Authentication already completed. A broken guest never sends the mutation reply.
		var probe [1]byte
		_, err := s.Conn.Read(probe[:])
		return 0, err
	}
	return s.Conn.Write(p)
}

func TestAuthenticatedUnregisterStallBoundsCleanupAndShutdown(t *testing.T) {
	f, a, o := repairOwnershipFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	daemon := &RuntimeDaemon{Daemon: o.daemon, Repository: f.composition}
	served := make(chan error, 1)
	go func() { served <- daemon.Serve(ctx, listener) }()
	owner, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(owner, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleResolve, Binding: o.binding}); err != nil {
		t.Fatal(err)
	}
	var acquired LifecycleResponse
	if err := codec.Read(owner, &acquired); err != nil || acquired.ErrorCode != "" {
		t.Fatalf("resolve: %+v %v", acquired, err)
	}
	stalled, guestDone := make(chan struct{}), make(chan struct{})
	hostSocket := make(chan net.Conn, 1)
	f.composition.Runtime.controlDial = func(context.Context, string) (io.ReadWriteCloser, error) {
		host, guest := net.Pipe()
		hostSocket <- host
		go func() {
			defer close(guestDone)
			defer guest.Close()
			_ = f.backend.server.ServeAuthenticated(context.Background(), &stallControlReply{Conn: guest, stalled: stalled})
		}()
		return host, nil
	}
	released := make(chan LifecycleResponse, 1)
	go func() {
		_ = codec.Write(owner, LifecycleRequest{Version: LifecycleProtocolVersion, Operation: LifecycleDetach, Binding: o.binding, AcquisitionID: acquired.AcquisitionID})
		var response LifecycleResponse
		_ = codec.Read(owner, &response)
		released <- response
	}()
	<-stalled
	host := <-hostSocket
	select {
	case response := <-released:
		if response.ErrorCode == "" {
			t.Error("stalled unregister reported successful cleanup")
		}
	case <-time.After(repositoryRollbackTimeout + 2*time.Second):
		host.Close()
		<-released
		cancel()
		<-served
		<-guestDone
		t.Fatal("authenticated control I/O ignored cleanup timeout; retained handler would block shutdown join")
	}
	<-guestDone
	o.daemon.ownershipMu.Lock()
	state := o.daemon.refOwnership[o.binding.Ref]
	pending := state != nil && state.phase == refPhasePendingDetach && len(state.owners) == 0
	owners := len(o.daemon.acquisitions)
	o.daemon.ownershipMu.Unlock()
	if !pending || owners != 0 {
		t.Errorf("failed final cleanup did not retain pending gate: pending=%v owners=%d", pending, owners)
	}
	if _, err := os.Stat(a.Logical.WorktreePath); err != nil {
		t.Errorf("failed unregister removed worktree: %v", err)
	}
	cancel()
	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Fatal("composition shutdown did not join retained handlers")
	}
}
