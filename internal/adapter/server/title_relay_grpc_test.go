package server

import (
	"context"
	"io"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func dialTitleGRPC(t *testing.T, svc *Service) (mecatlv1.HarnessServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(server, NewHarnessServer(svc))
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///title-test", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	return mecatlv1.NewHarnessServiceClient(conn), func() {
		_ = conn.Close()
		server.Stop()
		_ = listener.Close()
	}
}

func drainTitleConverse(t *testing.T, stream mecatlv1.HarnessService_ConverseClient) {
	t.Helper()
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}
