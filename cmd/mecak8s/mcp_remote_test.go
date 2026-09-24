package main

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/app"
	mcpbrokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestInitialProductionMCPBroker_Scenario5_Mecak8sComposition(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(tokenFile, []byte("workload-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caFile, []byte("not parsed until dial"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := parseFlags([]string{
		"--mcp-broker-address", "broker.mecatl.svc:8443",
		"--mcp-broker-token-file", tokenFile,
		"--mcp-broker-tls-ca", caFile,
		"--mcp-broker-server-name", "broker.mecatl.svc",
	})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.mcpBrokerAddress != "broker.mecatl.svc:8443" || cfg.mcpBrokerTokenFile != tokenFile || cfg.mcpBrokerTLSCAFile != caFile || cfg.mcpBrokerServerName != "broker.mecatl.svc" {
		t.Fatalf("remote broker configuration was not retained: %+v", cfg)
	}
	ac := appConfig(cfg, port.NopDiagnostics{}, observability{})
	if ac.MCPBrokerFactory == nil || !ac.MCPBrokerFactoryRequired {
		t.Fatal("app.Config did not select required remote broker composition")
	}

	catalogue, err := mcpbroker.Compile(mcpauthority.BrokerConfig{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	remoteService, err := mcpbroker.New(catalogue, func(_ context.Context, _ mcpbroker.SessionRef, _ string, call session.ToolCall) (session.ToolResult, error) {
		return session.NewToolResult(call.ID, "ok"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	factoryCalled := false
	ac.MCPAuthorityLoader = nil
	ac.MCPProfileLoader = nil
	ac.MCPAuthority = mcpauthority.NewBroker(mcpauthority.BrokerConfig{})
	ac.MCPBrokerFactory = func(context.Context) (mcpbrokercontract.Service, func() error, error) {
		factoryCalled = true
		return remoteService, remoteService.Close, nil
	}
	ac.RedisURL = ""
	ac.SessionLeaseK8sNamespace = ""
	ac.SchedulerEnabled = false
	ac.Workspace = t.TempDir()
	ac.MockProvider = mockllm.New()
	built, err := app.Build(t.Context(), ac)
	if err != nil {
		t.Fatalf("Build with remote broker factory: %v", err)
	}
	t.Cleanup(built.Close)
	if !factoryCalled || built.MCPBroker != nil || !built.MCPBrokerHandlers.Empty() {
		t.Fatalf("remote composition called=%v local=%v handlers-empty=%v", factoryCalled, built.MCPBroker != nil, built.MCPBrokerHandlers.Empty())
	}

	for _, argv := range [][]string{
		{"--mcp-broker-address", "broker:8443"},
		{"--mcp-broker-address", "broker:8443", "--mcp-broker-token-file", tokenFile, "--mcp-broker-tls-ca", caFile},
		{"--mcp-broker-token-file", tokenFile, "--mcp-broker-tls-ca", caFile, "--mcp-broker-server-name", "broker"},
	} {
		if _, err := parseFlags(argv); err == nil {
			t.Fatalf("parseFlags(%v) accepted an incomplete/anonymous remote broker configuration", argv)
		}
	}
}

func TestSingletonBrokerRemediation_Scenario4_ProjectedTokenRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	write := func(value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("first")
	projectedCredentials := mcpbrokergrpc.ProjectedTokenCredentials{Path: path}
	first, err := projectedCredentials.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	write("second")
	second, err := projectedCredentials.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first["authorization"] != "Bearer first" || second["authorization"] != "Bearer second" {
		t.Fatalf("authorization generations = %q then %q", first["authorization"], second["authorization"])
	}
	if !projectedCredentials.RequireTransportSecurity() {
		t.Fatal("projected broker credentials permitted cleartext transport")
	}

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
	catalogue, err := mcpbroker.Compile(mcpauthority.BrokerConfig{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	local, err := mcpbroker.New(catalogue, func(_ context.Context, _ mcpbroker.SessionRef, _ string, call session.ToolCall) (session.ToolResult, error) {
		return session.NewToolResult(call.ID, "ok"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	brokerServer, err := mcpbrokergrpc.NewServer(local, mcpbrokergrpc.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var observed []string
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})),
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			md, _ := metadata.FromIncomingContext(ctx)
			mu.Lock()
			observed = append(observed, md.Get("authorization")...)
			mu.Unlock()
			return handler(ctx, req)
		}),
	)
	mcpbrokergrpc.RegisterServer(server, brokerServer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = brokerServer.Shutdown(context.Background()); _ = local.Close() })
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	factory := mcpBrokerFactory(config{mcpBrokerAddress: listener.Addr().String(), mcpBrokerTokenFile: path, mcpBrokerTLSCAFile: caPath, mcpBrokerServerName: "localhost"})
	remote, closeRemote, err := factory(t.Context())
	if err != nil {
		t.Fatalf("dial remote broker: %v", err)
	}
	t.Cleanup(func() { _ = closeRemote() })
	write("rpc-first")
	if _, _, err := remote.AttachSession(t.Context(), "rotation-1"); err != nil {
		t.Fatal(err)
	}
	write("rpc-second")
	if _, _, err := remote.AttachSession(t.Context(), "rotation-2"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), observed...)
	mu.Unlock()
	if len(got) < 2 || got[len(got)-2] != "Bearer rpc-first" || got[len(got)-1] != "Bearer rpc-second" {
		t.Fatalf("real RPC credentials = %v", got)
	}
}
