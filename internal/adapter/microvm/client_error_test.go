package microvm

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestClientClassifiesRepositoryLogicalRootUnavailableWithoutDaemonDetail(t *testing.T) {
	const secret = "token=daemon-secret /private/repository errno=13"
	endpoint := testUnixSocketPath(t)
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serveErr := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serveErr <- acceptErr
			return
		}
		defer conn.Close()
		var request lifecycleRequest
		if readErr := readFrame(conn, &request); readErr != nil {
			serveErr <- readErr
			return
		}
		serveErr <- writeFrame(conn, lifecycleResponse{
			ErrorCode: repositoryLogicalRootUnavailableCategory,
			ErrorText: secret,
		})
	}()

	client, err := New("unix://" + endpoint)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.call(context.Background(), lifecycleRequest{Version: protocolVersion, Operation: "create"})
	if !errors.Is(err, ErrRepositoryLogicalRootUnavailable) {
		t.Fatalf("error=%v, want ErrRepositoryLogicalRootUnavailable", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "/private/repository") {
		t.Fatalf("client error leaked daemon detail: %v", err)
	}
	var categorized interface{ EnvironmentLifecycleCategory() string }
	if !errors.As(err, &categorized) || categorized.EnvironmentLifecycleCategory() != repositoryLogicalRootUnavailableCategory {
		t.Fatalf("category=%v, want %q", categorized, repositoryLogicalRootUnavailableCategory)
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}
}
