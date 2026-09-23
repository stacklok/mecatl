package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/control/controltest"
)

func TestDaemonShutdownAcknowledgesBeforeGracefulCancellation(t *testing.T) {
	daemon := newShutdownTestDaemon(t, 100, 100)
	request := shutdownTestRequest(t, daemon.info)

	server, client := net.Pipe()
	defer client.Close()
	done := make(chan error, 1)
	go func() { done <- daemon.ServeConn(context.Background(), server) }()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(client, request); err != nil {
		t.Fatal(err)
	}
	select {
	case <-daemon.shutdown:
		t.Fatal("daemon cancellation began before the shutdown acknowledgement was read")
	case <-time.After(25 * time.Millisecond):
	}
	var response LifecycleResponse
	if err := codec.Read(client, &response); err != nil {
		t.Fatalf("read shutdown acknowledgement: %v", err)
	}
	if response.ErrorCode != "" || len(response.Payload) != 0 {
		t.Fatalf("shutdown acknowledgement = %+v", response)
	}
	select {
	case <-daemon.shutdown:
	case <-time.After(time.Second):
		t.Fatal("acknowledged shutdown did not cancel the daemon")
	}
	if err := <-done; err != nil {
		t.Fatalf("serve shutdown request: %v", err)
	}

	// A repeated authenticated request is safe and receives the same acknowledgement.
	server, client = net.Pipe()
	defer client.Close()
	done = make(chan error, 1)
	go func() { done <- daemon.ServeConn(context.Background(), server) }()
	if err := codec.Write(client, request); err != nil {
		t.Fatal(err)
	}
	response = LifecycleResponse{}
	if err := codec.Read(client, &response); err != nil || response.ErrorCode != "" {
		t.Fatalf("repeated shutdown acknowledgement = %+v, %v", response, err)
	}
	if err := <-done; err != nil {
		t.Fatalf("serve repeated shutdown request: %v", err)
	}
}

func TestRuntimeDaemonShutdownAcknowledgementCancelsServer(t *testing.T) {
	daemon := newShutdownTestDaemon(t, 100, 100)
	runtimeDaemon := &RuntimeDaemon{
		Daemon: daemon,
		Repository: &RepositoryComposition{Runtime: &RepositoryRuntime{
			vms: make(map[string]*repositoryRuntimeGeneration), listeners: make(map[string]*net.UnixListener),
		}},
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- runtimeDaemon.Serve(context.Background(), listener) }()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(conn, shutdownTestRequest(t, daemon.info)); err != nil {
		t.Fatal(err)
	}
	var response LifecycleResponse
	if err := codec.Read(conn, &response); err != nil || response.ErrorCode != "" {
		t.Fatalf("shutdown acknowledgement = %+v, %v", response, err)
	}
	select {
	case err := <-serveDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RuntimeDaemon.Serve error = %v, want graceful cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("acknowledged shutdown did not stop RuntimeDaemon.Serve")
	}
}

func TestDaemonShutdownRejectsUnknownAndWrongIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request func(*testing.T, DaemonInfo) LifecycleRequest
	}{
		{name: "unknown operation", request: func(_ *testing.T, _ DaemonInfo) LifecycleRequest {
			return LifecycleRequest{Version: LifecycleProtocolVersion, Operation: "stop-now"}
		}},
		{name: "wrong daemon identity", request: func(t *testing.T, info DaemonInfo) LifecycleRequest {
			info.BinaryIdentity = "sha256:wrong"
			return shutdownTestRequest(t, info)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			daemon := newShutdownTestDaemon(t, 100, 100)
			server, client := net.Pipe()
			defer client.Close()
			done := make(chan error, 1)
			go func() { done <- daemon.ServeConn(context.Background(), server) }()
			codec := control.NewCodec(control.DefaultMaxMessageBytes)
			if err := codec.Write(client, tc.request(t, daemon.info)); err != nil {
				t.Fatal(err)
			}
			var response LifecycleResponse
			if err := codec.Read(client, &response); err != nil {
				t.Fatal(err)
			}
			if response.ErrorCode == "" {
				t.Fatalf("request unexpectedly succeeded: %+v", response)
			}
			if err := <-done; err == nil {
				t.Fatal("invalid shutdown request returned no serve error")
			}
			select {
			case <-daemon.shutdown:
				t.Fatal("invalid shutdown request cancelled daemon")
			default:
			}
		})
	}
}

func TestDaemonShutdownRejectsUnauthorizedPeer(t *testing.T) {
	daemon := newShutdownTestDaemon(t, 100, 101)
	server, client := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- daemon.ServeConn(context.Background(), server) }()
	if err := <-done; !errors.Is(err, control.ErrUnauthenticatedPeer) {
		t.Fatalf("unauthorized shutdown error = %v, want ErrUnauthenticatedPeer", err)
	}
	_ = client.Close()
	select {
	case <-daemon.shutdown:
		t.Fatal("unauthorized peer cancelled daemon")
	default:
	}
}

func newShutdownTestDaemon(t *testing.T, accountUID, peerUID uint32) *Daemon {
	t.Helper()
	service, err := control.NewService(control.ServiceConfig{
		AccountUID:        accountUID,
		PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: peerUID},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Daemon{
		control: service,
		info: DaemonInfo{
			ProtocolVersion: LifecycleProtocolVersion,
			ReleaseIdentity: "release-v1", BinaryIdentity: "sha256:binary", ConfigDigest: "sha256:config",
			PolicyRevision: "policy-v1", Profiles: []string{"microvm-local"}, Socket: "/private/microvmd.sock",
		},
		shutdown: make(chan struct{}),
	}
}

func shutdownTestRequest(t *testing.T, info DaemonInfo) LifecycleRequest {
	t.Helper()
	payload, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	return LifecycleRequest{Version: LifecycleProtocolVersion, Operation: lifecycleShutdown, Payload: payload}
}
