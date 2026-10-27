package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/adrg/xdg"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/app"
)

func TestEmbeddedMCPAuthFileComposition(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "selected.yaml")
	if err := os.WriteFile(path, []byte("mcp:\n  exact:\n    token: selected-file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseFlags([]string{"--mock", "--toolhive-llm=false", "--workspace", t.TempDir(), "--api-key-file", path})
	if err != nil {
		t.Fatal(err)
	}
	ac := embeddedConfig(cfg, port.NopDiagnostics{})
	ac.ProviderCredentialLoader.(interface{ SetAPIKeyFile(string) }).SetAPIKeyFile(filepath.Join(t.TempDir(), "not-selected"))
	if err := os.WriteFile(path, []byte("mcp: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	operator := &permconfig.MCPSection{Mode: "global", Servers: []permconfig.MCPServerProfile{{Name: "server", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "static_bearer", StaticBearer: &permconfig.MCPStaticBearerProfile{TokenRef: "exact"}}}}}
	servers, lifecycle, err := ac.MCPProfileLoader.Load(operator)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle.Close()
	if len(servers) != 1 || servers[0].Headers["Authorization"] != "Bearer selected-file-token" {
		t.Fatal("direct loader lost the embedded credential snapshot")
	}
	authority, err := ac.MCPAuthorityLoader.LoadAuthority(operator, ac.MCPAuthorityDefault, ac.MCPBrokerSupported)
	if err != nil {
		t.Fatal(err)
	}
	servers, lifecycle, ok := authority.Global()
	if !ok {
		t.Fatal("expected global authority")
	}
	defer lifecycle.Close()
	if len(servers) != 1 || servers[0].Headers["Authorization"] != "Bearer selected-file-token" {
		t.Fatal("authority loader lost the embedded credential snapshot")
	}
}

// Connect parses and dials the remote server without reading or transmitting the
// local MCP credential. The read seam also observes a positive embedded load.
func TestConnectSkipsEmbeddedAuthFileComposition(t *testing.T) {
	const localToken = "local-mcp-must-not-be-uploaded"
	const remoteToken = "remote-server-fixture-bearer"
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", t.TempDir())
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = configHome
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })
	authDir := filepath.Join(configHome, "mecatl")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(authDir, "auth.yaml")
	if err := os.WriteFile(path, []byte("mcp:\n  local:\n    token: "+localToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalEnv := xdgconfig.OSEnv
	t.Cleanup(func() { xdgconfig.OSEnv = originalEnv })
	var authReads atomic.Int32
	xdgconfig.OSEnv.ReadFile = func(name string) ([]byte, error) {
		if name == path {
			authReads.Add(1)
		}
		return originalEnv.ReadFile(name)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	remote := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		calls.Add(1)
		md, _ := metadata.FromIncomingContext(ctx)
		if strings.Contains(fmt.Sprint(req, md), localToken) {
			t.Error("connect uploaded the local MCP credential")
		}
		if values := md.Get("authorization"); len(values) != 1 || values[0] != "Bearer "+remoteToken {
			t.Error("connect did not send the selected remote-server bearer")
		}
		return next(ctx, req)
	}))
	mecatlv1.RegisterHarnessServiceServer(remote, anonymousConnectServer{})
	go func() { _ = remote.Serve(listener) }()
	t.Cleanup(func() { remote.Stop(); _ = listener.Close() })
	cfg, err := parseRunConfig(resolveInvocation([]string{"mecatui", "connect", listener.Addr().String(), "--tls=false", "--auth-token", remoteToken}))
	if err != nil {
		t.Fatal(err)
	}
	embedded := false
	_, dial, cleanup, err := resolveTransportWithHook(t.Context(), cfg, func(app.Config) error {
		embedded = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cl, err := client.Dial(dial)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	id, _, _, err := cl.CreateSession(t.Context(), mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, client.ModelSelection{})
	if err != nil || id != "anonymous-local" || calls.Load() == 0 || embedded || authReads.Load() != 0 {
		t.Fatalf("connect did not preserve remote-only custody: session=%q err=%v calls=%d embedded=%v reads=%d", id, err, calls.Load(), embedded, authReads.Load())
	}
	// Positive control: an embedded parse reaches the same file-read detector.
	if _, err := parseFlags([]string{"--mock", "--toolhive-llm=false", "--workspace", t.TempDir(), "--api-key-file", path}); err != nil {
		t.Fatal(err)
	}
	if authReads.Load() == 0 {
		t.Fatal("credential file read detector was not exercised by embedded mode")
	}
}
