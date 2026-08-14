package main

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/embed"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/app"
)

func TestSessionContinuityUX_Scenario6_EmbeddedAndConnectE2E(t *testing.T) {
	starters := []struct {
		name  string
		start func(*testing.T, app.Config) (string, func())
	}{
		{name: "embedded", start: startStartupResumeEmbedded},
		{name: "connect", start: startStartupResumeConnect},
	}
	for _, tc := range starters {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			workspace, storeDir := t.TempDir(), t.TempDir()
			cfg := app.Config{Workspace: workspace, StoreDir: storeDir, Model: "mock-model", UseMock: true, Shell: "/bin/sh", Compaction: "heuristic", Tokenizer: "heuristic"}

			target, stop := tc.start(t, cfg)
			id := seedStartupResumeSession(ctx, t, target, workspace)
			stop()

			target, stop = tc.start(t, cfg)
			defer stop()
			cl, err := client.Dial(client.DialConfig{Server: target})
			if err != nil {
				t.Fatalf("dial restarted server: %v", err)
			}
			defer func() { _ = cl.Close() }()

			for _, selector := range []struct {
				name   string
				id     string
				latest bool
			}{{name: "exact", id: id}, {name: "latest", latest: true}} {
				t.Run(selector.name, func(t *testing.T) {
					selection, err := resolveStartupResume(ctx, cl, selector.id, selector.latest)
					if err != nil {
						t.Fatalf("resolve startup resume: %v", err)
					}
					if selection.Row.ID != id || selection.Transcript.SessionID != id || !selection.Transcript.Complete || len(selection.Transcript.Messages) == 0 {
						t.Fatalf("selection = %#v, want complete transcript for %q", selection, id)
					}
					rows, err := cl.ListSessions(ctx)
					if err != nil {
						t.Fatalf("list after adoption: %v", err)
					}
					if len(rows) != 1 || rows[0].ID != id {
						t.Fatalf("rows after adoption = %#v, want only %q", rows, id)
					}
				})
			}
		})
	}
}

func startStartupResumeEmbedded(t *testing.T, cfg app.Config) (string, func()) {
	t.Helper()
	srv, err := embed.Start(t.Context(), cfg, embed.PerfConfig{})
	if err != nil {
		t.Fatalf("start embedded server: %v", err)
	}
	return srv.Target(), func() { _ = srv.Close() }
}

func startStartupResumeConnect(t *testing.T, cfg app.Config) (string, func()) {
	t.Helper()
	built, err := app.Build(t.Context(), cfg)
	if err != nil {
		t.Fatalf("build connect server: %v", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		built.Close()
		t.Fatalf("listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(grpcServer, server.NewHarnessServer(built.Service))
	go func() { _ = grpcServer.Serve(lis) }()
	return lis.Addr().String(), func() {
		grpcServer.GracefulStop()
		_ = lis.Close()
		built.Close()
	}
}

func seedStartupResumeSession(ctx context.Context, t *testing.T, target, workspace string) string {
	t.Helper()
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial seed server: %v", err)
	}
	defer func() { _ = conn.Close() }()
	svc := mecatlv1.NewHarnessServiceClient(conn)
	created, err := svc.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Workspace: workspace})
	if err != nil {
		t.Fatalf("create seed session: %v", err)
	}
	stream, err := svc.Converse(ctx)
	if err != nil {
		t.Fatalf("open seed stream: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: created.GetSessionId(), Text: "resume this transcript"}}}); err != nil {
		t.Fatalf("send seed prompt: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close seed stream: %v", err)
	}
	for {
		_, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			return created.GetSessionId()
		}
		if err != nil {
			t.Fatalf("receive seed turn: %v", err)
		}
	}
}
