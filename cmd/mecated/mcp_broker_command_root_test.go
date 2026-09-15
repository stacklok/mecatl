package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/app"
)

func TestMecatedExplicitBrokerAuthorityReachesBuild(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(settings, []byte(`mcp:
  mode: broker
  servers:
    - name: calendar
      url: https://calendar.example/mcp
      auth: {mode: none}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseFlagsMode(modeServe, []string{"--permission-config", settings})
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	ac := appConfig(cfg, nil, nil, nil, nil, port.NopDiagnostics{})
	if ac.MCPAuthorityLoader == nil || !ac.MCPBrokerSupported {
		t.Fatal("mecated did not enable canonical broker authority resolution")
	}
	ac.Workspace = t.TempDir()
	ac.UseMock = true
	ac.NoSoul = true
	ac.NoUserModel = true
	ac.PermissionsConventional = false
	ac.AgentsConventional = false
	ac.MCPBrokerCaller = func(context.Context, mcpbroker.SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		return session.NewToolResult("", "unused"), nil
	}
	built, err := buildIsolated(t, context.Background(), ac)
	if err != nil {
		t.Fatalf("app.Build explicit broker authority: %v", err)
	}
	defer built.Close()
	if built.MCPBroker == nil {
		t.Fatal("explicit mcp.mode broker did not reach app.Build's broker construction")
	}
	if !built.MCPBrokerHandlers.Empty() {
		t.Fatal("broker without callback unexpectedly exposed an HTTP handler bundle")
	}
}

func TestMecatedOmittedMCPModeDefaultsGlobalWithoutBroker(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := parseFlagsMode(modeServe, nil)
	if err != nil {
		t.Fatalf("parseFlagsMode: %v", err)
	}
	ac := appConfig(cfg, nil, nil, nil, nil, port.NopDiagnostics{})
	if ac.MCPAuthorityDefault != mcpauthority.Global {
		t.Fatalf("MCPAuthorityDefault = %q, want global", ac.MCPAuthorityDefault)
	}
	ac.Workspace = t.TempDir()
	ac.UseMock = true
	ac.NoSoul = true
	ac.NoUserModel = true
	ac.PermissionsConventional = false
	ac.AgentsConventional = false
	ac.ToolHiveEnabled = false
	built, err := buildIsolated(t, context.Background(), ac)
	if err != nil {
		t.Fatalf("app.Build omitted MCP mode: %v", err)
	}
	defer built.Close()
	if built.MCPBroker != nil || !built.MCPBrokerHandlers.Empty() {
		t.Fatal("omitted MCP mode started a broker")
	}
}

func TestMecatedBrokerMountPreflightsRealMuxCollisions(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	complete := testBrokerBundle(handler)
	for _, tc := range []struct {
		name           string
		handlers       mcpbroker.HandlerBundle
		callbackPath   string
		occupy         string
		occupiedStatus int
		unmounted      string
	}{
		{
			name:           "callback",
			handlers:       mcpbroker.HandlerBundle{Callback: handler},
			callbackPath:   "/readyz",
			occupy:         "/readyz",
			occupiedStatus: http.StatusOK,
			unmounted:      "/v1/mcp/broker/mcp",
		},
		{
			name:           "fixed route",
			handlers:       complete,
			callbackPath:   "/agent/callback",
			occupy:         "/v1/mcp/broker/oauth/authorize",
			occupiedStatus: http.StatusConflict,
			unmounted:      "/v1/mcp/broker/oauth/token",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			server.NewHealthHandler(func() bool { return true }).RegisterHealth(mux)
			mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
			if tc.occupiedStatus == http.StatusConflict {
				mux.Handle(tc.occupy, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusConflict) }))
			}

			err := mountBrokerHandlers(mux, "127.0.0.1:8081", false, tc.handlers, tc.callbackPath)
			if err == nil {
				t.Fatal("mountBrokerHandlers succeeded despite a real-mux route collision")
			}
			for _, path := range []string{tc.occupy, tc.unmounted} {
				r := httptest.NewRequest(http.MethodGet, path, nil)
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				if path == tc.occupy && w.Code != tc.occupiedStatus {
					t.Fatalf("occupied route status = %d, want %d", w.Code, tc.occupiedStatus)
				}
				if path == tc.unmounted && w.Code != http.StatusTeapot {
					t.Fatalf("%s status = %d, want API fallback %d; broker partially mounted", path, w.Code, http.StatusTeapot)
				}
			}
		})
	}
}

func TestMecatedCallbackCollisionCoversEveryHTTPMethodAtomically(t *testing.T) {
	methods := []string{
		http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead,
		http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace,
	}
	broker := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
			mux.HandleFunc(method+" /callback", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusConflict) })
			if err := mountBrokerHandlers(mux, "127.0.0.1:8081", false, testBrokerBundle(broker), "/callback"); err == nil {
				t.Fatalf("%s callback collision succeeded", method)
			}

			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/mcp/broker/oauth/token", nil))
			if w.Code != http.StatusTeapot {
				t.Fatalf("fixed route status = %d, want fallback %d; mount was partial", w.Code, http.StatusTeapot)
			}
		})
	}
}

func TestMecatedRootCallbackPreflightIsExactAndMethodAware(t *testing.T) {
	broker := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	t.Run("ordinary fallback retains subpaths", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
		if err := mountBrokerHandlers(mux, "127.0.0.1:8081", false, mcpbroker.HandlerBundle{Callback: broker}, "/"); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			method, path string
			want         int
		}{
			{method: http.MethodGet, path: "/", want: http.StatusNoContent},
			{method: http.MethodPost, path: "/", want: http.StatusTeapot},
			{method: http.MethodGet, path: "/api/child", want: http.StatusTeapot},
		} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.want {
				t.Errorf("%s %s status = %d, want %d", tc.method, tc.path, w.Code, tc.want)
			}
		}
	})

	t.Run("only GET root conflicts", func(t *testing.T) {
		for _, tc := range []struct {
			method  string
			wantErr bool
		}{
			{method: http.MethodGet, wantErr: true},
			{method: http.MethodPost, wantErr: false},
		} {
			t.Run(tc.method, func(t *testing.T) {
				mux := http.NewServeMux()
				mux.Handle("/", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				mux.HandleFunc(tc.method+" /{$}", func(http.ResponseWriter, *http.Request) {})
				err := mountBrokerHandlers(mux, "127.0.0.1:8081", false, mcpbroker.HandlerBundle{Callback: broker}, "/")
				if (err != nil) != tc.wantErr {
					t.Fatalf("root callback mount error = %v, want error %t", err, tc.wantErr)
				}
			})
		}
	})
}

func TestMecatedFixedBrokerCollisionCoversEveryHTTPMethodAtomically(t *testing.T) {
	methods := []string{
		http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead,
		http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace,
	}
	const fixed = "/v1/mcp/broker/oauth/authorize"
	broker := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
			mux.HandleFunc(method+" "+fixed, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusConflict) })
			if err := mountBrokerHandlers(mux, "127.0.0.1:8081", false, testBrokerBundle(broker), "/callback"); err == nil {
				t.Fatalf("%s fixed-route collision succeeded", method)
			}

			for _, path := range []string{"/v1/mcp/broker/oauth/token", "/callback"} {
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
				if w.Code != http.StatusTeapot {
					t.Fatalf("%s status = %d, want fallback %d; mount was partial", path, w.Code, http.StatusTeapot)
				}
			}
		})
	}
}

func TestMecatedExplicitBrokerRejectsDisabledHTTP(t *testing.T) {
	built := &app.Built{MCPBroker: &mcpbroker.Runtime{}}
	for _, cfg := range []config{{}, {acp: true, httpAddr: defaultHTTPAddr}} {
		err := serveBuilt(context.Background(), cfg, built, nil, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "requires network serve mode with an enabled HTTP listener") {
			t.Fatalf("serveBuilt unreachable broker error = %v", err)
		}
	}
}

func TestMecatedRunServeHandoffMountsBuiltBrokerBundle(t *testing.T) {
	callback := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	built := &app.Built{
		Service:               newOfflineService(t),
		MCPBroker:             &mcpbroker.Runtime{},
		MCPBrokerHandlers:     mcpbroker.HandlerBundle{Callback: callback},
		MCPBrokerCallbackPath: "/broker-callback",
	}
	cfg := config{grpcAddr: freeTCPAddr(t), httpAddr: freeTCPAddr(t), metricsAddr: ""}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveBuilt(ctx, cfg, built, nil, nil, nil) }()

	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err := http.Get("http://" + cfg.httpAddr + "/broker-callback")
		if err == nil {
			response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				cancel()
				<-done
				t.Fatalf("broker callback status = %d, want %d", response.StatusCode, http.StatusNoContent)
			}
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("serveBuilt did not mount the app.Build bundle: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serveBuilt shutdown: %v", err)
	}
}

func TestMecatedBrokerProtocolRoutesArePublicNotBearerWrapped(t *testing.T) {
	auth, token := authenticatorFromFlags(t, []string{"--auth-token", "control-secret"})
	defer auth.Close()
	mux := http.NewServeMux()
	mux.Handle("/", auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})))
	protocol := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	controlAPIAuthenticated := brokerControlAPIAuthenticated(config{authToken: token}, nil)
	if err := mountBrokerHandlers(mux, "0.0.0.0:8081", controlAPIAuthenticated, mcpbroker.HandlerBundle{Callback: protocol}, "/callback"); err != nil {
		t.Fatalf("mount public OAuth protocol route: %v", err)
	}

	for path, want := range map[string]int{"/callback": http.StatusNoContent, "/v1/control": http.StatusUnauthorized} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != want {
			t.Fatalf("unauthenticated %s status = %d, want %d", path, w.Code, want)
		}
	}

	unprotected := http.NewServeMux()
	unprotected.Handle("/", http.NotFoundHandler())
	if err := mountBrokerHandlers(unprotected, "0.0.0.0:8081", false, mcpbroker.HandlerBundle{Callback: protocol}, "/callback"); err == nil {
		t.Fatal("non-loopback broker with an unauthenticated control API was admitted")
	}
}

func testBrokerBundle(handler http.Handler) mcpbroker.HandlerBundle {
	return mcpbroker.HandlerBundle{
		Authorization: handler, Token: handler, UpstreamCallback: handler,
		Discovery: handler, JWKS: handler, ProtectedResource: handler, VMCP: handler,
		Callback: handler,
	}
}
