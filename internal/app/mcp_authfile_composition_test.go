package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

// Stop immediately after the real loader resolves credentials, before Build can
// construct a manager or attempt an outbound connection.
type selectedFileMCPProbe struct {
	resolver *cliconfig.MCPProfileResolver
	servers  []mcp.ServerConfig
	stop     error
}

func (p *selectedFileMCPProbe) Load(operator *permconfig.MCPSection) ([]mcp.ServerConfig, interface{ Close() error }, error) {
	servers, lifecycle, err := p.resolver.Load(operator)
	if err != nil {
		return nil, nil, err
	}
	p.servers = servers
	_ = lifecycle.Close()
	return nil, nil, p.stop
}

func (p *selectedFileMCPProbe) LoadAuthority(operator *permconfig.MCPSection, mode mcpauthority.Mode, broker bool) (*mcpauthority.Result, error) {
	authority, err := p.resolver.LoadAuthority(operator, mode, broker)
	if err != nil {
		return nil, err
	}
	servers, lifecycle, ok := authority.Global()
	if !ok {
		return nil, errors.New("expected global authority")
	}
	p.servers = servers
	_ = lifecycle.Close()
	return nil, p.stop
}

type authFileRequestCapture struct {
	t        *testing.T
	mu       sync.Mutex
	requests [][]byte
}

func (c *authFileRequestCapture) observe(req port.LLMRequest) {
	payload, err := json.Marshal(req)
	if err != nil {
		c.t.Errorf("marshal captured model request: %v", err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, payload)
}

func (c *authFileRequestCapture) payloads() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.requests...)
}

// isolateAuthFileDiscovery prevents ProviderFlags.Resolve from consulting any
// operator state before Build switches to the settings-selected credentials file.
func isolateAuthFileDiscovery(t *testing.T) {
	t.Helper()
	xdg, home := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", home)
	for _, name := range []string{"MECATL_MCP_TOKEN_EXACT", "MECATL_MCP_TOKEN_FIXTURE"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENCODE_API_KEY", "TYPESAFE_API_KEY", "EXA_API_KEY", "BRAVE_API_KEY", "WEBSEARCH_API_KEY"} {
		t.Setenv(name, "")
	}
	dir := filepath.Join(xdg, "mecatl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.yaml"), []byte("mcp: [poisoned-conventional\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runAuthFileMCPTool(ctx context.Context, built *app.Built, prompt string) (session.SessionID, []session.Event, error) {
	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		return "", nil, err
	}
	run, err := built.Service.StartRunContent(ctx, sess.ID, prompt, nil)
	if err != nil {
		return "", nil, err
	}
	var events []session.Event
	for event := range run.Events() {
		events = append(events, event)
		if event.Type == session.EvPermissionAsk && event.Ask != nil {
			run.Approve(event.Ask.AskID, session.VerdictAllowOnce)
		}
	}
	built.Service.FinishRun(sess.ID, run)
	return sess.ID, events, nil
}

func assertAuthFileContainment(t *testing.T, token string, capture *authFileRequestCapture, events []session.Event, durable *session.Session, diag *acceptanceDiag, err error) {
	t.Helper()
	var surfaces [][]byte
	surfaces = append(surfaces, capture.payloads()...)
	for _, event := range events {
		payload, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			t.Fatalf("marshal captured event: %v", marshalErr)
		}
		surfaces = append(surfaces, payload)
	}
	if durable != nil {
		payload, marshalErr := json.Marshal(durable)
		if marshalErr != nil {
			t.Fatalf("marshal persisted session: %v", marshalErr)
		}
		surfaces = append(surfaces, payload)
	}
	for _, line := range diagnosticSurfaces(diag) {
		surfaces = append(surfaces, []byte(line))
	}
	if err != nil {
		surfaces = append(surfaces, []byte(err.Error()))
	}
	encoded, marshalErr := json.Marshal(token)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, surface := range surfaces {
		if strings.Contains(string(surface), token) || strings.Contains(string(surface), string(encoded[1:len(encoded)-1])) {
			t.Fatal("static bearer reached a model request, event, session snapshot, diagnostic, or error surface")
		}
	}
}

func assertAuthFileWireContainment(t *testing.T, built *app.Built, id session.SessionID, token string) {
	t.Helper()
	handler := server.NewHTTPHandler(built.Service)
	for _, route := range []struct{ path, want string }{
		{"/v1/sessions/" + string(id), string(id)},
		{"/v1/sessions/" + string(id) + "/transcript", "complete"},
		{"/v1/mcp/sources", "protected"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route.path, nil))
		body := response.Body.String()
		if response.Code != http.StatusOK || !strings.Contains(body, route.want) {
			t.Fatalf("%s did not produce the expected wire response: status %d", route.path, response.Code)
		}
		if strings.Contains(body, token) || strings.Contains(body, "Authorization") {
			t.Fatalf("%s exposed MCP credentials", route.path)
		}
	}
}

func TestBuildFinalizesAuthFileBeforeMCP(t *testing.T) {
	for _, authority := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "authority"}[authority], func(t *testing.T) {
			isolateAuthFileDiscovery(t)
			dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "mecatl")
			path := filepath.Join(t.TempDir(), "selected.yaml")
			if err := os.WriteFile(path, []byte("providers:\n  openai:\n    api_key: selected-provider\nmcp:\n  exact:\n    token: selected-mcp\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			settings := filepath.Join(dir, "settings.yaml")
			body := "credential_store:\n  api_key:\n    file: " + path + "\nmcp:\n  mode: global\n  servers:\n    - name: server\n      url: https://mcp.example/mcp\n      auth:\n        mode: static_bearer\n        static_bearer: {token_ref: exact}\n"
			if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			flags := cliconfig.RegisterProviderFlags(flag.NewFlagSet("test", flag.ContinueOnError), cliconfig.ProviderFlagHelp{})
			keys := flags.Resolve()
			source := cliconfig.NewCredentialSource(flags, keys)
			provider := cliconfig.NewProviderCredentialResolver(source, keys)
			probe := &selectedFileMCPProbe{resolver: cliconfig.NewMCPProfileResolver(nil, nil, source), stop: errors.New("resolved before transport")}
			cfg := app.Config{Workspace: t.TempDir(), UserModelDir: t.TempDir(), Model: "mock", MockProvider: mockllm.New(), NoSoul: true, PermissionConfigs: []string{settings}, ProviderCredentialLoader: provider, MCPAuthorityDefault: mcpauthority.Global}
			if authority {
				cfg.MCPAuthorityLoader = probe
			} else {
				cfg.MCPProfileLoader = probe
			}
			_, err := app.Build(t.Context(), cfg)
			if !errors.Is(err, probe.stop) {
				t.Fatalf("Build did not finalize selected credentials before MCP: %v", err)
			}
			if len(probe.servers) != 1 || probe.servers[0].Headers["Authorization"] != "Bearer selected-mcp" {
				t.Fatal("MCP did not use settings-selected source")
			}
			if err := os.WriteFile(path, []byte("providers: {}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			credentials, _, err := provider.Load(nil)
			if err != nil || credentials.OpenAIKey != "selected-provider" {
				t.Fatal("provider did not share immutable final selected snapshot")
			}
		})
	}
}

// TestLogicalBearerReachesMCPTransport proves source selection at the HTTP
// destination, including byte-exact whitespace and immutable credentials on refresh.
func TestLogicalBearerReachesMCPTransport(t *testing.T) {
	for _, sourceName := range []string{"file", "derived environment", "environment overrides file", "explicit environment"} {
		t.Run(sourceName, func(t *testing.T) {
			for _, authority := range []bool{false, true} {
				t.Run(map[bool]string{false: "direct", true: "authority"}[authority], func(t *testing.T) {
					isolateAuthFileDiscovery(t)
					fixture := newLoginFixture(t)
					const token = " \tmcp-authfile\t transport-canary"
					fixture.acceptedBearer = token
					// The CLI's legacy name must not influence token_ref.
					t.Setenv("MCP_PROTECTED_TOKEN", "poisoned-cli-token")
					fileToken := token
					selector := "token_ref: fixture"
					switch sourceName {
					case "derived environment":
						t.Setenv("MECATL_MCP_TOKEN_FIXTURE", token)
						fileToken = ""
					case "environment overrides file":
						t.Setenv("MECATL_MCP_TOKEN_FIXTURE", token)
						fileToken = "unused-file-canary"
					case "explicit environment":
						t.Setenv("MECATL_EXPLICIT_FIXTURE", token)
						selector = "token_env: MECATL_EXPLICIT_FIXTURE"
						fileToken = "unused-file-canary"
					}

					path := filepath.Join(t.TempDir(), "auth.yaml")
					encodedToken, _ := json.Marshal(fileToken)
					fileBody := "mcp:\n  fixture:\n    token: " + string(encodedToken) + "\n"
					if fileToken == "" {
						fileBody = "mcp: {}\n"
					}
					if err := os.WriteFile(path, []byte(fileBody), 0o600); err != nil {
						t.Fatal(err)
					}
					settings := filepath.Join(t.TempDir(), "settings.yaml")
					body := "credential_store:\n  api_key:\n    file: " + path + "\nmcp:\n  mode: global\n  servers:\n    - name: protected\n      url: " + fixture.resource() + "\n      auth:\n        mode: static_bearer\n        static_bearer: {" + selector + "}\n"
					if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
						t.Fatal(err)
					}

					flags := cliconfig.RegisterProviderFlags(flag.NewFlagSet("test", flag.ContinueOnError), cliconfig.ProviderFlagHelp{})
					keys := flags.Resolve()
					source := cliconfig.NewCredentialSource(flags, keys)
					provider := cliconfig.NewProviderCredentialResolver(source, keys)
					profiles := cliconfig.NewMCPProfileResolver(nil, nil, source)
					capture := &authFileRequestCapture{t: t}
					diag := &acceptanceDiag{}
					llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(capture.observe)},
						mockllm.ToolCallTurn(session.NewToolCall("call", "mcp__protected__ready", []byte(`{}`))), mockllm.TextTurn("complete"),
						mockllm.ToolCallTurn(session.NewToolCall("after-rotation", "mcp__protected__ready", []byte(`{}`))), mockllm.TextTurn("complete"),
						mockllm.ToolCallTurn(session.NewToolCall("after-refresh", "mcp__protected__ready", []byte(`{}`))), mockllm.TextTurn("complete"))
					cfg := app.Config{
						Workspace:                t.TempDir(),
						UserModelDir:             t.TempDir(),
						Model:                    "mock",
						MockProvider:             llm,
						Diagnostics:              diag,
						PermissionConfigs:        []string{settings},
						ProviderCredentialLoader: provider,
						MCPAuthorityDefault:      mcpauthority.Global,
					}
					if authority {
						cfg.MCPAuthorityLoader = profiles
					} else {
						cfg.MCPProfileLoader = profiles
					}
					built, err := app.Build(t.Context(), cfg)
					if err != nil {
						t.Fatal(err)
					}
					defer built.Close()

					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					sessionID, events, runErr := runAuthFileMCPTool(ctx, built, "call the protected tool")
					if runErr != nil {
						t.Fatalf("file-backed MCP tool invocation failed: %v", runErr)
					}
					durable, getErr := built.Service.GetSession(ctx, sessionID)
					if getErr != nil {
						t.Fatal(getErr)
					}
					counts := fixture.snapshot()
					if counts.authenticated == 0 || counts.toolCalls != 1 {
						t.Fatalf("fixture did not receive the file-backed bearer and tool call: authenticated=%d tools=%d", counts.authenticated, counts.toolCalls)
					}
					if len(capture.payloads()) != 2 {
						t.Fatalf("model requests = %d, want tool call and tool result turns", len(capture.payloads()))
					}
					if err := os.WriteFile(path, []byte("mcp:\n  fixture:\n    token: rotated-token-not-accepted-by-fixture\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					// Lose the destination's session table, forcing the existing manager
					// to reconnect before source refresh can replace any runtime state.
					fixture.mu.Lock()
					fixture.mcpHandler = fixture.newMCPHandler()
					fixture.mu.Unlock()
					_, afterReconnect, err := runAuthFileMCPTool(ctx, built, "call after credential rotation and destination session loss")
					if err != nil {
						t.Fatal(err)
					}
					reconnected := fixture.snapshot()
					if reconnected.toolCalls != 2 || reconnected.opened <= counts.opened {
						t.Fatal("reconnect lost the captured credential or did not open a new session")
					}
					if _, err := built.Service.RefreshMcpSources(ctx, sessionID); err != nil {
						t.Fatalf("refresh after token rotation: %v", err)
					}
					_, afterRotation, err := runAuthFileMCPTool(ctx, built, "call after credential-file rotation and source refresh")
					if err != nil {
						t.Fatal(err)
					}
					after := fixture.snapshot()
					if after.authenticated <= reconnected.authenticated || after.toolCalls != 3 {
						t.Fatal("tool call after live refresh lost the original credential snapshot")
					}
					allEvents := append(append(events, afterReconnect...), afterRotation...)
					assertAuthFileContainment(t, token, capture, allEvents, durable, diag, nil)
					assertAuthFileContainment(t, "unused-file-canary", capture, allEvents, durable, diag, nil)
					assertAuthFileWireContainment(t, built, sessionID, token)
					if sourceName == "explicit environment" {
						if strings.Count(strings.Join(diagnosticSurfaces(diag), "\n"), "deprecated") != 1 {
							t.Fatal("Build warning missing or repeated on refresh")
						}
					}
				})
			}
		})
	}
}

func TestAuthFileBearerAuthenticationFailureIsContained(t *testing.T) {
	isolateAuthFileDiscovery(t)
	const token = "mcp-authfile-auth-failure-canary"
	var requests atomic.Int32
	var receivedBearer atomic.Bool
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") == "Bearer "+token {
			receivedBearer.Store(true)
		}
		w.Header().Set("WWW-Authenticate", `Bearer scope="read"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer mcpServer.Close()

	path := filepath.Join(t.TempDir(), "auth.yaml")
	if err := os.WriteFile(path, []byte("mcp:\n  fixture:\n    token: "+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	body := "credential_store:\n  api_key:\n    file: " + path + "\nmcp:\n  servers:\n    - name: protected\n      url: " + mcpServer.URL + "\n      auth:\n        mode: static_bearer\n        static_bearer: {token_ref: fixture}\n"
	if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	capture := &authFileRequestCapture{t: t}
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(capture.observe)},
		mockllm.ToolCallTurn(session.NewToolCall("call", "mcp__protected__ready", []byte(`{}`))), mockllm.TextTurn("complete"))
	flags := cliconfig.RegisterProviderFlags(flag.NewFlagSet("test", flag.ContinueOnError), cliconfig.ProviderFlagHelp{})
	keys := flags.Resolve()
	source := cliconfig.NewCredentialSource(flags, keys)
	diag := &acceptanceDiag{}
	built, err := app.Build(t.Context(), app.Config{
		Workspace:                t.TempDir(),
		UserModelDir:             t.TempDir(),
		Model:                    "mock",
		MockProvider:             llm,
		Diagnostics:              diag,
		PermissionConfigs:        []string{settings},
		ProviderCredentialLoader: cliconfig.NewProviderCredentialResolver(source, keys),
		MCPProfileLoader:         cliconfig.NewMCPProfileResolver(nil, nil, source),
	})
	if err != nil {
		assertAuthFileContainment(t, token, capture, nil, nil, diag, err)
		t.Fatalf("authentication failure prevented server startup: %v", err)
	}
	defer built.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	sessionID, events, runErr := runAuthFileMCPTool(ctx, built, "call the protected tool")
	var durable *session.Session
	if sessionID != "" {
		durable, _ = built.Service.GetSession(ctx, sessionID)
	}
	assertAuthFileContainment(t, token, capture, events, durable, diag, runErr)
	if runErr != nil {
		t.Fatalf("authentication-failure run failed: %v", runErr)
	}
	if requests.Load() == 0 || !receivedBearer.Load() {
		t.Fatal("authentication-failure fixture did not receive the file-backed bearer")
	}
	if len(capture.payloads()) != 2 {
		t.Fatalf("model requests = %d, want tool call and error-result turns", len(capture.payloads()))
	}
}

func TestBuildLegacyWarningUsesEffectiveProfilesAndResolverLifetime(t *testing.T) {
	for _, authority := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "authority"}[authority], func(t *testing.T) {
			isolateAuthFileDiscovery(t)
			t.Setenv("MECATL_BUILD_WARNING", "")
			settings := filepath.Join(t.TempDir(), "settings.yaml")
			body := "mcp:\n  mode: global\n  servers:\n"
			for _, name := range []string{"overridden", "selected"} {
				body += "    - name: " + name + "\n      url: https://mcp.example/mcp\n      auth:\n        mode: static_bearer\n        static_bearer: {token_env: MECATL_BUILD_WARNING}\n"
			}
			if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			legacy := &cliconfig.MCPServerList{}
			if err := legacy.Set("overridden=https://legacy.example/mcp"); err != nil {
				t.Fatal(err)
			}
			var loads int
			diag := &acceptanceDiag{}
			load := func(profiles *cliconfig.MCPProfileResolver) {
				loads++
				cfg := app.Config{Workspace: t.TempDir(), UserModelDir: t.TempDir(), Model: "mock", MockProvider: mockllm.New(), Diagnostics: diag, PermissionConfigs: []string{settings}, MCPAuthorityDefault: mcpauthority.Global}
				if authority {
					cfg.MCPAuthorityLoader = profiles
				} else {
					cfg.MCPProfileLoader = profiles
				}
				_, err := app.Build(t.Context(), cfg)
				if !errors.Is(err, cliconfig.ErrMCPProfileSecret) || !strings.Contains(err.Error(), "selected") {
					t.Fatalf("load %d failed at wrong profile: %v", loads, err)
				}
			}
			profiles := cliconfig.NewMCPProfileResolver(legacy, func(string) (string, bool) { return "", false }, cliconfig.NewCredentialSource(nil, cliconfig.ResolvedCredentials{}))
			load(profiles)
			load(profiles)
			logs := strings.Join(diagnosticSurfaces(diag), "\n")
			if strings.Count(logs, "deprecated") != 1 || strings.Contains(logs, "overridden") || strings.Contains(logs, "MECATL_BUILD_WARNING") {
				t.Fatal("Build warning filtering, containment, or once lifetime failed")
			}
			load(cliconfig.NewMCPProfileResolver(legacy, func(string) (string, bool) { return "", false }, cliconfig.NewCredentialSource(nil, cliconfig.ResolvedCredentials{})))
			if strings.Count(strings.Join(diagnosticSurfaces(diag), "\n"), "deprecated") != 2 {
				t.Fatal("new Build owner did not warn")
			}
		})
	}
}

func TestInvalidStaticBearerStopsBeforeMCPTransport(t *testing.T) {
	for _, sourceName := range []string{"file", "derived environment", "explicit environment"} {
		t.Run(sourceName, func(t *testing.T) {
			for _, tc := range []struct{ name, token string }{
				{"empty", ""}, {"blank", " \t"},
				{"CR", "sentinel\rcontrol"}, {"LF", "sentinel\ncontrol"},
				{"NUL", "sentinel\x00control"}, {"other control", "sentinel\x01control"},
				{"DEL", "sentinel\x7fcontrol"}, {"trailing space", "sentinel "}, {"trailing tab", "sentinel\t"},
			} {
				// Blank file entries are rejected by the auth-file document validator;
				// the profile secret boundary is exercised with nonblank file values.
				if sourceName == "file" && strings.TrimSpace(tc.token) == "" {
					continue
				}
				t.Run(tc.name, func(t *testing.T) {
					for _, authority := range []bool{false, true} {
						t.Run(map[bool]string{false: "direct", true: "authority"}[authority], func(t *testing.T) {
							isolateAuthFileDiscovery(t)
							t.Setenv("MECATL_MCP_TOKEN_FIXTURE", "")
							if err := os.Unsetenv("MECATL_MCP_TOKEN_FIXTURE"); err != nil {
								t.Fatal(err)
							}
							var requests atomic.Int32
							mcpServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
							defer mcpServer.Close()
							selector := "token_ref: fixture"
							fileToken := "valid-fallback-must-not-be-used"
							switch sourceName {
							case "file":
								fileToken = tc.token
							case "derived environment":
								// An invalid present winner must never fall through to the valid file.
								// NUL cannot appear in a real process environment; it is covered via file/injected-source tests.
								if strings.ContainsRune(tc.token, 0) {
									t.Skip("OS environments cannot contain NUL")
								}
								t.Setenv("MECATL_MCP_TOKEN_FIXTURE", tc.token)
							case "explicit environment":
								if strings.ContainsRune(tc.token, 0) {
									t.Skip("OS environments cannot contain NUL")
								}
								t.Setenv("MECATL_INVALID_EXPLICIT", tc.token)
								selector = "token_env: MECATL_INVALID_EXPLICIT"
							}
							path := filepath.Join(t.TempDir(), "auth.yaml")
							encoded, _ := json.Marshal(fileToken)
							if err := os.WriteFile(path, []byte("mcp:\n  fixture:\n    token: "+string(encoded)+"\n"), 0o600); err != nil {
								t.Fatal(err)
							}
							settings := filepath.Join(t.TempDir(), "settings.yaml")
							body := "credential_store:\n  api_key:\n    file: " + path + "\nmcp:\n  mode: global\n  servers:\n    - name: protected\n      url: " + mcpServer.URL + "\n      auth:\n        mode: static_bearer\n        static_bearer: {" + selector + "}\n"
							if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
								t.Fatal(err)
							}
							flags := cliconfig.RegisterProviderFlags(flag.NewFlagSet("test", flag.ContinueOnError), cliconfig.ProviderFlagHelp{})
							keys := flags.Resolve()
							source := cliconfig.NewCredentialSource(flags, keys)
							diag := &acceptanceDiag{}
							profiles := cliconfig.NewMCPProfileResolver(nil, nil, source)
							cfg := app.Config{Workspace: t.TempDir(), UserModelDir: t.TempDir(), Model: "mock", MockProvider: mockllm.New(), Diagnostics: diag, PermissionConfigs: []string{settings}, ProviderCredentialLoader: cliconfig.NewProviderCredentialResolver(source, keys), MCPAuthorityDefault: mcpauthority.Global}
							if authority {
								cfg.MCPAuthorityLoader = profiles
							} else {
								cfg.MCPProfileLoader = profiles
							}
							_, err := app.Build(t.Context(), cfg)
							if !errors.Is(err, cliconfig.ErrMCPProfileSecret) || strings.Contains(err.Error(), "sentinel") {
								t.Fatalf("unsafe token error = %v", err)
							}
							if requests.Load() != 0 {
								t.Fatal("unsafe token reached MCP transport")
							}
							logs := strings.Join(diagnosticSurfaces(diag), "\n")
							if strings.Contains(logs, "sentinel") || strings.Contains(logs, fileToken) {
								t.Fatal("unsafe token reached diagnostics")
							}
							if sourceName == "explicit environment" && strings.Count(logs, "deprecated") != 1 {
								t.Fatal("Build lost warning on selected-secret startup failure")
							}
						})
					}
				})
			}
		})
	}
}
