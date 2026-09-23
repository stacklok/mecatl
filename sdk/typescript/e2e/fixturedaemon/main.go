// Command fixturedaemon serves the real Mecatl wire adapters with an in-process
// OAuth-protected MCP broker. It exists only for the TypeScript SDK E2E suite.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/contracts/sessionaffinity"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mockscript"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/app"
)

const (
	toolOne = "mcp__fixture__one"
	toolTwo = "mcp__fixture__two"
)

type options struct {
	grpcAddr     string
	httpAddr     string
	permission   string
	readyFile    string
	script       string
	socketPath   string
	userModelDir string
	workspace    string
}

type helperMarker struct{}

func (helperMarker) Helper() {}

type readyDocument struct {
	APIMajor          int32    `json:"api_major"`
	Features          []string `json:"features,omitempty"`
	FixtureControlURL string   `json:"fixture_control_url"`
	GRPCAddress       string   `json:"grpc_address"`
	HTTPAddress       string   `json:"http_address,omitempty"`
	PID               int      `json:"pid"`
	Schema            string   `json:"schema"`
	SocketPath        string   `json:"socket_path,omitempty"`
	Transport         string   `json:"transport"`
}

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

//nolint:gocyclo // The test-only composition root owns listeners, OAuth fixtures, and readiness in one bounded lifecycle.
func run() error {
	opts := parseFlags()
	if opts.script == "" || opts.readyFile == "" || opts.workspace == "" {
		return errors.New("--script, --ready-file, and --workspace are required")
	}
	provider, err := mockscript.Load(opts.script)
	if err != nil {
		return err
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	callbackMux := http.NewServeMux()
	callbackServer := httptest.NewUnstartedServer(callbackMux)
	callbackServer.StartTLS()
	defer callbackServer.Close()

	oauthMux := http.NewServeMux()
	oauthServer := httptest.NewUnstartedServer(oauthMux)
	oauthServer.StartTLS()
	defer oauthServer.Close()

	roots := x509.NewCertPool()
	roots.AddCert(callbackServer.Certificate())
	roots.AddCert(oauthServer.Certificate())
	browserClient := trustedClient(roots)
	defer browserClient.CloseIdleConnections()

	oauthMux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		query := url.Values{"state": {r.URL.Query().Get("state")}}
		if r.URL.Query().Get("fixture_decision") == "deny" {
			query.Set("error", "access_denied")
		} else {
			query.Set("code", "fixture-code")
		}
		http.Redirect(w, r, callbackServer.URL+"/oauth/callback?"+query.Encode(), http.StatusFound)
	})
	oauthMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid token request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"fixture-access-token","token_type":"Bearer","expires_in":3600}`)
	})

	oauthProfile := func() permconfig.MCPAuthProfile {
		return permconfig.MCPAuthProfile{Mode: "oauth", OAuth: &permconfig.MCPOAuthProfile{
			Upstream: &permconfig.MCPOAuthUpstreamProfile{Mode: "oauth2", OAuth2: &permconfig.MCPOAuth2UpstreamProfile{
				AuthorizationEndpoint: oauthServer.URL + "/authorize",
				TokenEndpoint:         oauthServer.URL + "/token",
			}},
			Client: permconfig.MCPOAuthClientProfile{Mode: "preregistered", Preregistered: &permconfig.MCPPreregisteredClientProfile{
				ID: "fixture-client", SecretEnv: "MECATL_FIXTURE_CLIENT_SECRET", // #nosec G101 -- trusted environment-variable name, not a credential.
			}},
			Scopes: []string{"read"},
		}}
	}
	declaration := mcpauthority.NewBroker(mcpauthority.BrokerConfig{
		CallbackURL: callbackServer.URL + "/oauth/callback",
		Routes: []permconfig.MCPServerProfile{
			{Name: "fixture-one", URL: "https://fixture-one.example/mcp", Auth: oauthProfile()},
			{Name: "fixture-two", URL: "https://fixture-two.example/mcp", Auth: oauthProfile()},
		},
	})

	built, err := app.Build(rootCtx, app.Config{
		AuthorityEvaluator: "noop",
		MCPAuthority:       declaration,
		MCPBrokerAuthorizedCaller: func(_ context.Context, _ mcpbroker.SessionRef, _ string, call session.ToolCall, tokens oauth2.TokenSource) (session.ToolResult, error) {
			if _, err := tokens.Token(); err != nil {
				return session.ToolResult{}, err
			}
			return session.NewToolResult(call.ID, "protected result from "+call.Name), nil
		},
		MCPBrokerCaller: func(context.Context, mcpbroker.SessionRef, string, session.ToolCall) (session.ToolResult, error) {
			return session.ToolResult{}, errors.New("anonymous protected call reached fixture caller")
		},
		MCPBrokerDiscovered: []mcpbroker.ToolDefinition{
			{Backend: "fixture-one", Name: toolOne, Description: "first protected fixture tool", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true},
			{Backend: "fixture-two", Name: toolTwo, Description: "second protected fixture tool", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true},
		},
		MCPBrokerOptions: []mcpbroker.Option{
			mcpbroker.WithOAuthLoopbackForTest(helperMarker{}, roots),
			mcpbroker.WithOAuthLimits(2*time.Minute, 3*time.Second),
			mcpbroker.WithOAuthSecretResolver(func(context.Context, string) (string, error) { return "fixture-secret", nil }),
		},
		MockProvider:         provider,
		NoSoul:               true,
		PermissionConfigs:    []string{opts.permission},
		ServerImplementation: "mecated",
		UserModelDir:         opts.userModelDir,
		Workspace:            opts.workspace,
	})
	if err != nil {
		return fmt.Errorf("build fixture app: %w", err)
	}
	defer built.Close()
	if err := built.MountMCPBrokerHandlers(callbackMux); err != nil {
		return fmt.Errorf("mount broker callback: %w", err)
	}

	controlMux := http.NewServeMux()
	controlMux.HandleFunc("POST /complete", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Decision string `json:"decision"`
			URL      string `json:"url"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&request); err != nil {
			http.Error(w, "invalid control request", http.StatusBadRequest)
			return
		}
		if request.Decision != "grant" && request.Decision != "deny" {
			http.Error(w, "decision must be grant or deny", http.StatusBadRequest)
			return
		}
		presentation, err := url.Parse(request.URL)
		if err != nil || presentation.Scheme != "https" || presentation.Host != strings.TrimPrefix(oauthServer.URL, "https://") || presentation.Path != "/authorize" {
			http.Error(w, "presentation URL is outside the fixture authority", http.StatusBadRequest)
			return
		}
		query := presentation.Query()
		query.Set("fixture_decision", request.Decision)
		presentation.RawQuery = query.Encode()
		response, err := browserClient.Get(presentation.String())
		if err != nil {
			http.Error(w, "authorization completion failed", http.StatusBadGateway)
			return
		}
		defer func() { _ = response.Body.Close() }()
		accepted := response.StatusCode == http.StatusOK || request.Decision == "deny" && response.StatusCode == http.StatusBadRequest
		if !accepted {
			http.Error(w, "callback rejected authorization", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	controlListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen fixture control: %w", err)
	}
	defer func() { _ = controlListener.Close() }()
	controlServer := &http.Server{Handler: controlMux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = controlServer.Serve(controlListener) }()
	defer func() { _ = controlServer.Close() }()

	grpcListener, transport, socketPath, err := listenGRPC(opts)
	if err != nil {
		return err
	}
	defer func() { _ = grpcListener.Close() }()
	if socketPath != "" {
		defer func() { _ = os.Remove(socketPath) }()
	}
	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(unaryHeaders),
		grpc.StreamInterceptor(streamHeaders),
	)
	mecatlv1.RegisterHarnessServiceServer(grpcServer, server.NewHarnessServer(built.Service))
	grpcErrors := make(chan error, 1)
	go func() { grpcErrors <- grpcServer.Serve(grpcListener) }()
	defer grpcServer.Stop()

	var httpListener net.Listener
	var httpServer *http.Server
	if opts.httpAddr != "" {
		httpListener, err = net.Listen("tcp", opts.httpAddr)
		if err != nil {
			return fmt.Errorf("listen HTTP: %w", err)
		}
		httpServer = &http.Server{Handler: httpHeaders(server.NewHTTPHandler(built.Service)), ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = httpServer.Serve(httpListener) }()
		defer func() { _ = httpServer.Close() }()
	}

	info := built.Service.CompatibilityInfo(context.Background())
	ready := readyDocument{
		APIMajor: info.GetApiMajor(), Features: info.GetFeatures(),
		FixtureControlURL: "http://" + controlListener.Addr().String() + "/complete",
		GRPCAddress:       grpcListener.Addr().String(), PID: os.Getpid(), Schema: "mecated-ready/1",
		SocketPath: socketPath, Transport: transport,
	}
	if httpListener != nil {
		ready.HTTPAddress = httpListener.Addr().String()
	}
	if err := writeReady(opts.readyFile, ready); err != nil {
		return err
	}

	select {
	case <-rootCtx.Done():
		return nil
	case err := <-grpcErrors:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return fmt.Errorf("serve gRPC: %w", err)
	}
}

func parseFlags() options {
	var opts options
	flag.StringVar(&opts.grpcAddr, "grpc-addr", "127.0.0.1:0", "gRPC TCP address")
	flag.StringVar(&opts.httpAddr, "http-addr", "", "HTTP/SSE TCP address")
	flag.StringVar(&opts.permission, "permission-config", "", "explicit permission config")
	flag.StringVar(&opts.readyFile, "ready-file", "", "readiness document path")
	flag.StringVar(&opts.script, "script", "", "mock provider script")
	flag.StringVar(&opts.socketPath, "grpc-unix-socket", "", "gRPC UDS path")
	flag.StringVar(&opts.userModelDir, "user-model-dir", "", "user model directory")
	flag.StringVar(&opts.workspace, "workspace", "", "workspace directory")
	flag.Parse()
	return opts
}

func listenGRPC(opts options) (net.Listener, string, string, error) {
	if opts.socketPath == "" {
		listener, err := net.Listen("tcp", opts.grpcAddr)
		if err != nil {
			return nil, "", "", fmt.Errorf("listen gRPC TCP: %w", err)
		}
		return listener, "tcp", "", nil
	}
	if err := os.Remove(opts.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, "", "", fmt.Errorf("remove stale gRPC socket: %w", err)
	}
	listener, err := net.Listen("unix", opts.socketPath)
	if err != nil {
		return nil, "", "", fmt.Errorf("listen gRPC UDS: %w", err)
	}
	return listener, "unix", opts.socketPath, nil
}

func trustedClient(roots *x509.CertPool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

func requestProof(ctx context.Context) metadata.MD {
	md, _ := metadata.FromIncomingContext(ctx)
	proof := metadata.Pairs("x-e2e-response", "fixture")
	if caller := md.Get("x-e2e-caller"); len(caller) == 1 {
		proof.Set("x-e2e-caller-seen", caller[0])
	}
	if expected := md.Get("x-e2e-session-id"); len(expected) == 1 {
		affinity := md.Get(sessionaffinity.HeaderName)
		proof.Set("x-e2e-affinity-seen", fmt.Sprint(len(affinity) == 1 && affinity[0] == expected[0]))
	}
	return proof
}

func unaryHeaders(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	_ = info
	_ = grpc.SetHeader(ctx, requestProof(ctx))
	return handler(ctx, req)
}

func streamHeaders(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	_ = info
	_ = stream.SetHeader(requestProof(stream.Context()))
	return handler(srv, stream)
}

func httpHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-e2e-response", "fixture")
		if caller := r.Header.Get("x-e2e-caller"); caller != "" {
			w.Header().Set("x-e2e-caller-seen", caller)
		}
		if expected := r.Header.Get("x-e2e-session-id"); expected != "" {
			w.Header().Set("x-e2e-affinity-seen", fmt.Sprint(r.Header.Get(sessionaffinity.HeaderName) == expected))
		}
		next.ServeHTTP(w, r)
	})
}

func writeReady(path string, document readyDocument) error {
	body, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("marshal readiness document: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create readiness directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".authorization-ready-*")
	if err != nil {
		return fmt.Errorf("create readiness temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish readiness document: %w", err)
	}
	return nil
}
