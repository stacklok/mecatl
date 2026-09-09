package control_test

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/control/controltest"
)

func TestMicroVMEnvironments_Scenario2_LocalPeerCredentialsBindOwner(t *testing.T) {
	binding := control.Binding{
		Owner:         "caller:alice",
		SessionID:     "session-1",
		EnvironmentID: "environment-1",
		Ref:           "microvm:environment-1",
		Generation:    7,
	}
	service, err := control.NewService(control.ServiceConfig{
		AccountUID: uint32(os.Getuid()),
		Bindings:   []control.Binding{binding},
	})
	if err != nil {
		t.Fatalf("new control service: %v", err)
	}

	socketPath := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatalf("listen on local control socket: %v", err)
	}
	defer listener.Close()

	accepted := make(chan *net.UnixConn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, acceptErrValue := listener.AcceptUnix()
		if acceptErrValue != nil {
			acceptErr <- acceptErrValue
			return
		}
		accepted <- conn
	}()

	client, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatalf("dial local control socket: %v", err)
	}
	defer client.Close()

	var serverConn *net.UnixConn
	select {
	case serverConn = <-accepted:
		defer serverConn.Close()
	case err = <-acceptErr:
		t.Fatalf("accept local control socket: %v", err)
	}

	if err := service.Authorize(serverConn, binding); err != nil {
		t.Fatalf("authorize configured local account: %v", err)
	}

	for name, mutate := range map[string]func(*control.Binding){
		"owner":      func(got *control.Binding) { got.Owner = "caller:mallory" },
		"session":    func(got *control.Binding) { got.SessionID = "session-guessed" },
		"ref":        func(got *control.Binding) { got.Ref = "microvm:stale" },
		"generation": func(got *control.Binding) { got.Generation++ },
	} {
		t.Run(name+" mismatch", func(t *testing.T) {
			claim := binding
			mutate(&claim)
			if err := service.Authorize(serverConn, claim); !errors.Is(err, control.ErrBindingMismatch) {
				t.Fatalf("authorize mismatched %s: got %v, want ErrBindingMismatch", name, err)
			}
		})
	}

	foreignService, err := control.NewService(control.ServiceConfig{
		AccountUID:        2000,
		Bindings:          []control.Binding{binding},
		PeerAuthenticator: controltest.StaticPeerAuthenticator{UID: 2001},
	})
	if err != nil {
		t.Fatalf("new foreign-peer control service: %v", err)
	}
	if err := foreignService.Authorize(serverConn, binding); !errors.Is(err, control.ErrUnauthenticatedPeer) {
		t.Fatalf("authorize guessed identifiers from foreign peer: got %v, want ErrUnauthenticatedPeer", err)
	}
}
