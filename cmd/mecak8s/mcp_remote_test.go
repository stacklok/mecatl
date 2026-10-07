package main

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestMecak8sSessionBrokerFlags(t *testing.T) {
	for _, args := range [][]string{{"--mcp-broker-session-api"}, {"--mcp-broker-address=broker:8443"}, {"--mcp-broker-tls-ca=fixture.pem"}, {"--mcp-broker-server-name=broker"}, {"--mcp-broker-token-file=fixture.jwt"}} {
		if _, err := parseFlags(args); err == nil {
			t.Fatalf("invalid broker selection admitted: %v", args)
		}
	}
	zero, err := parseFlags(nil)
	if err != nil || sessionBrokerFactory(zero) != nil {
		t.Fatal("zero-MCP command selected a broker")
	}
	complete, err := parseFlags([]string{"--mcp-broker-address=broker:8443", "--mcp-broker-tls-ca=fixture.pem", "--mcp-broker-server-name=broker", "--mcp-broker-token-file=fixture.jwt"})
	if err != nil || sessionBrokerFactory(complete) == nil {
		t.Fatalf("complete tuple did not select SessionService: %v", err)
	}
}

func TestMecak8sCanonicalSessionBrokerComposition(t *testing.T) {
	cfg := config{mcpBrokerAddress: "broker:8443", mcpBrokerTokenFile: "token", mcpBrokerTLSCAFile: "ca", mcpBrokerServerName: "broker"}
	if appConfig(cfg, port.NopDiagnostics{}, observability{}).SessionBrokerFactory == nil {
		t.Fatal("canonical factory missing")
	}
}

type rotationSessionService struct{ c.SessionService }

func (rotationSessionService) OpenSession(context.Context, *c.SessionRef) (c.SessionSnapshot, error) {
	cat, err := c.NewCatalogue(c.CatalogueRef(strings.Repeat("A", 43)), "", nil)
	return c.SessionSnapshot{Ref: c.SessionRef(strings.Repeat("A", 43)), Catalogue: cat, ExpiresAt: time.Now().Add(time.Hour)}, err
}

func TestCanonicalSessionBrokerStartupFailsClosed(t *testing.T) {
	for _, failure := range []string{"wrong-service", "workload-denied", "wrong-tls-name", "unavailable"} {
		t.Run(failure, func(t *testing.T) {
			certPEM, keyPEM := makeCompositionKeyPair(t)
			certificate, err := tls.X509KeyPair(certPEM, keyPEM)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})), grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
				if failure == "workload-denied" {
					return nil, status.Error(codes.Unauthenticated, "workload denied")
				}
				return handler(ctx, req)
			}))
			t.Cleanup(server.Stop)
			if failure != "wrong-service" {
				rpc, err := mcpbrokergrpc.NewSessionRPC(rotationSessionService{})
				if err != nil {
					t.Fatal(err)
				}
				p.RegisterSessionServiceServer(server, rpc)
			}
			if failure == "unavailable" {
				_ = listener.Close()
			} else {
				go func() { _ = server.Serve(listener) }()
			}
			dir := t.TempDir()
			ca, token := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "token")
			if err := os.WriteFile(ca, certPEM, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(token, []byte("synthetic-workload"), 0600); err != nil {
				t.Fatal(err)
			}
			name := "localhost"
			if failure == "wrong-tls-name" {
				name = "wrong.example.com"
			}
			bounds := mcpbrokergrpc.DefaultConfig()
			bounds.DialTimeout = 200 * time.Millisecond
			factory := mcpbrokergrpc.NewSessionRemoteFactory(mcpbrokergrpc.RemoteFactoryConfig{Target: listener.Addr().String(), CAFile: ca, ServerName: name, TokenFile: token, Transport: bounds})
			client, closeClient, err := factory(t.Context())
			if err == nil || client != nil || closeClient != nil {
				t.Fatal("failed broker admission returned a host client")
			}
		})
	}
}

func TestCanonicalSessionBrokerProjectedTokenRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	write := func(value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("first")
	certPEM, keyPEM := makeCompositionKeyPair(t)
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	rpc, err := mcpbrokergrpc.NewSessionRPC(rotationSessionService{})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var observed []string
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})), grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if !strings.HasPrefix(info.FullMethod, "/mecatl.broker.v1.SessionService/") {
			t.Error("legacy RPC selected")
		}
		md, _ := metadata.FromIncomingContext(ctx)
		mu.Lock()
		observed = append(observed, md.Get("authorization")...)
		mu.Unlock()
		return h(ctx, req)
	}))
	p.RegisterSessionServiceServer(srv, rpc)
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Stop)
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	factory := sessionBrokerFactory(config{mcpBrokerAddress: listener.Addr().String(), mcpBrokerTokenFile: path, mcpBrokerTLSCAFile: caPath, mcpBrokerServerName: "localhost"})
	remote, closeRemote, err := factory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeRemote() })
	for _, token := range []string{"rpc-first", "rpc-second"} {
		write(token)
		if _, err := remote.OpenSession(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(observed) < 2 || observed[len(observed)-2] != "Bearer rpc-first" || observed[len(observed)-1] != "Bearer rpc-second" {
		t.Fatal("projected credentials did not rotate")
	}
}
