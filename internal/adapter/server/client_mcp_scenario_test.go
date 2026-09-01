package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// --- harness -----------------------------------------------------------------

// mcpSpecRecorder captures what the SessionEngine factory was handed, so a test
// can assert the specs that crossed the seam VERBATIM — including the header map
// the wire carried, which no other surface is allowed to reveal.
type mcpSpecRecorder struct {
	mu    sync.Mutex
	calls [][]mcp.ServerConfig
	// closes counts SessionEngineResult.Close calls, so a test can prove a REFUSED
	// create tore the freshly-built engine down instead of leaking its MCP
	// connections for the process lifetime (ADR 0027 List 1 discipline).
	closes int
}

func (r *mcpSpecRecorder) noteClose() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closes++
}

func (r *mcpSpecRecorder) closeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closes
}

func (r *mcpSpecRecorder) record(specs []mcp.ServerConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, slices.Clone(specs))
}

func (r *mcpSpecRecorder) last(t *testing.T) []mcp.ServerConfig {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		t.Fatal("SessionEngine factory was never called: the create never reached the per-session engine path")
	}
	return r.calls[len(r.calls)-1]
}

func (r *mcpSpecRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// capturingDiag records every diagnostic line and attribute the Service emits, so
// AC9.5 can assert a header VALUE never reaches the operator log.
type capturingDiag struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (d *capturingDiag) Log(_ context.Context, level port.Level, msg string, attrs ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fmt.Fprintf(&d.buf, "%v %s %v\n", level, msg, attrs)
}

func (d *capturingDiag) With(attrs ...any) port.Diagnostics {
	d.mu.Lock()
	defer d.mu.Unlock()
	fmt.Fprintf(&d.buf, "with %v\n", attrs)
	return d
}

func (d *capturingDiag) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buf.String()
}

// clientMCPService builds a Service whose deployment either permits or refuses
// client-provided MCP servers, with a SessionEngine factory that mounts one tool
// PER SPEC named "mcp__<server>__ping". That mirrors the real namespacing closely
// enough for AC9.1's "reaches their tools" to be a genuine end-to-end assertion
// (a run that calls the tool and gets its result) rather than a claim about the
// factory argument alone.
func clientMCPService(t *testing.T, permit bool, rec *mcpSpecRecorder, diag port.Diagnostics, llm port.LLMProvider) *server.Service {
	t.Helper()
	return clientMCPServiceUnreachable(t, permit, rec, diag, llm, nil)
}

// clientMCPServiceUnreachable is clientMCPService with a set of server names the
// factory pretends it could not connect to, mirroring composition's best-effort
// mount: mcp.NewManager keeps the servers that answered, drops the rest, and
// reports only the survivors on SessionEngineResult.MountedClientMCP.
//
// It exists to test the all-or-nothing WIRE contract without a network call —
// which is the only way to test it offline, since a real unreachable server is a
// real dial.
func clientMCPServiceUnreachable(t *testing.T, permit bool, rec *mcpSpecRecorder, diag port.Diagnostics, llm port.LLMProvider, unreachable []string) *server.Service {
	t.Helper()
	shared := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("shared")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, permstore.New()),
		Model:   "test-model",
	})
	cfg := server.Config{
		Engine:           shared,
		Store:            memstore.New(),
		Workspaces:       func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		DefaultWorkspace: "/ws",
		Now:              func() time.Time { return time.Unix(0, 0) },
		Diagnostics:      diag,
		// The deployment policy under test. Derived by mecated from listener
		// topology; here it is set directly so a test can hold every other input
		// fixed and vary ONLY the policy.
		ClientMCPOnCreate: permit,
		SessionEngine: func(_ context.Context, _ server.ProviderSelector, specs []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
			if rec != nil {
				rec.record(specs)
			}
			cat := tool.NewCatalog()
			var mounted []string
			for _, s := range specs {
				// An unreachable server contributes neither a tool nor a name, exactly
				// as a failed mcp.Connect does in composition.
				if slices.Contains(unreachable, s.Name) {
					continue
				}
				cat.MustRegister(&scriptTool{
					name:     "mcp__" + s.Name + "__ping",
					readOnly: true,
					content:  "pong from " + s.Name,
				})
				mounted = append(mounted, s.Name)
			}
			return server.SessionEngineResult{
				Engine: agent.NewEngine(agent.Deps{
					LLM:     llm,
					Catalog: cat,
					Policy:  permpolicy.NewPolicy(allowRules(), permstore.New()),
					Model:   "test-model",
				}),
				// The honest mounted set. The Service compares it against the request
				// and fails the create on any shortfall (verifyClientMCPMounted).
				MountedClientMCP: mounted,
				Close: func() error {
					if rec != nil {
						rec.noteClose()
					}
					return nil
				},
			}, nil
		},
	}
	svc, err := server.NewService(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// httpFor stands up the HTTP surface over svc.
func httpFor(t *testing.T, svc *server.Service) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	t.Cleanup(srv.Close)
	return srv
}

// postCreate issues POST /v1/sessions with the given body and returns the status
// and the raw response bytes.
func postCreate(t *testing.T, srv *httptest.Server, body any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	resp, err := http.Post(srv.URL+"/v1/sessions", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST /v1/sessions: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, out
}

// The one header value used across these tests. It is deliberately a distinctive
// literal so a leak into a log, an event, or an error is unmistakable.
const secretHeaderValue = "Bearer zzz-scenario9-secret-token-zzz"

func httpMCPEntry() map[string]any {
	return map[string]any{
		"name":    "notes",
		"url":     "https://mcp.example/mcp",
		"type":    "http",
		"headers": map[string]string{"Authorization": secretHeaderValue},
	}
}

func protoMCPEntry() *mecatlv1.McpServerSpec {
	return &mecatlv1.McpServerSpec{
		Name:    "notes",
		Url:     "https://mcp.example/mcp",
		Type:    "http",
		Headers: map[string]string{"Authorization": secretHeaderValue},
	}
}

// --- AC9.1 -------------------------------------------------------------------

// TestSDKServerEnablers_Scenario9_UDSSessionMountsMCPServers is AC9.1.
//
// A session created on a deployment that PERMITS client MCP (the UDS / no
// network listener shape mecated derives — see clientMCPOnCreateForListeners)
// mounts the client's servers and REACHES THEIR TOOLS: the specs cross the
// SessionEngine seam verbatim, and a subsequent run calls the mounted tool and
// gets its result back. Asserting only the factory argument would prove the
// plumbing but not the mount, so the run is the load-bearing half.
//
// Both transports are exercised: gRPC (the SDK's path) and HTTP, because the
// field exists on both and a client must get the same answer from either.
func TestSDKServerEnablers_Scenario9_UDSSessionMountsMCPServers(t *testing.T) {
	t.Run("grpc mounts and the run reaches the tool", func(t *testing.T) {
		rec := &mcpSpecRecorder{}
		llm := mockllm.New(
			mockllm.ToolCallTurn(call("c1", "mcp__notes__ping", `{}`)),
			mockllm.TextTurn("used the client server"),
		)
		svc := clientMCPService(t, true, rec, nil, llm)
		client, cleanup := dialGRPC(t, svc)
		defer cleanup()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{
			Workspace:  "/ws",
			McpServers: []*mecatlv1.McpServerSpec{protoMCPEntry()},
		})
		if err != nil {
			t.Fatalf("CreateSession with mcp_servers on a permitting deployment: %v", err)
		}

		specs := rec.last(t)
		if len(specs) != 1 {
			t.Fatalf("factory got %d specs, want 1", len(specs))
		}
		got := specs[0]
		if got.Name != "notes" || got.URL != "https://mcp.example/mcp" {
			t.Fatalf("spec = %+v, want the client's name/url verbatim", struct{ Name, URL string }{got.Name, got.URL})
		}
		if got.Headers["Authorization"] != secretHeaderValue {
			t.Fatalf("header did not reach the mount verbatim: %q", got.Headers["Authorization"])
		}
		// The bounded per-server connect timeout is what stops N slow servers from
		// stalling one create; it must be stamped by the shared validator, not left
		// to the operator default.
		if got.Timeout != mcp.ClientConnectTimeout {
			t.Fatalf("spec Timeout = %v, want the bounded client timeout %v", got.Timeout, mcp.ClientConnectTimeout)
		}

		// Now prove the tools are REACHABLE, not merely configured.
		stream, err := client.Converse(ctx)
		if err != nil {
			t.Fatalf("Converse: %v", err)
		}
		if err := stream.Send(&mecatlv1.ConverseRequest{
			Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "use it"}},
		}); err != nil {
			t.Fatalf("Send prompt: %v", err)
		}
		_ = stream.CloseSend()

		events := recvAll(t, stream)
		if !hasType(events, "tool.call") || !hasType(events, "tool.result") {
			t.Fatalf("the mounted MCP tool was never reached: %v", typesOf(events))
		}
		if res := lastResult(t, events); res.GetText() != "used the client server" {
			t.Fatalf("result = %+v", res)
		}
	})

	t.Run("http mounts the same specs", func(t *testing.T) {
		rec := &mcpSpecRecorder{}
		svc := clientMCPService(t, true, rec, nil, mockllm.New(mockllm.TextTurn("ok")))
		srv := httpFor(t, svc)

		code, body := postCreate(t, srv, map[string]any{
			"workspace":   "/ws",
			"mcp_servers": []any{httpMCPEntry()},
		})
		if code != http.StatusCreated {
			t.Fatalf("POST /v1/sessions = %d, want 201; body=%s", code, body)
		}
		specs := rec.last(t)
		if len(specs) != 1 || specs[0].Name != "notes" || specs[0].Headers["Authorization"] != secretHeaderValue {
			t.Fatalf("HTTP specs did not reach the mount verbatim: %+v", specs)
		}
	})

	t.Run("an empty list stays on the shared-engine path", func(t *testing.T) {
		// A create carrying no MCP servers is not a USE of the feature, so it must
		// be byte-identical to today on EVERY deployment — no per-session engine,
		// no policy consultation.
		rec := &mcpSpecRecorder{}
		svc := clientMCPService(t, true, rec, nil, mockllm.New(mockllm.TextTurn("ok")))
		client, cleanup := dialGRPC(t, svc)
		defer cleanup()

		if _, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{Workspace: "/ws"}); err != nil {
			t.Fatalf("CreateSession without mcp_servers: %v", err)
		}
		if rec.count() != 0 {
			t.Fatalf("a create with no mcp_servers built a per-session engine (%d factory calls); the empty case must stay on the shared engine", rec.count())
		}
	})
}

// --- AC9.2 -------------------------------------------------------------------

// TestSDKServerEnablers_Scenario9_McpServersRejectedOnTCPListener is AC9.2.
//
// The SAME request that AC9.1 accepts is REFUSED on a deployment with a
// network-facing API listener, with the typed unsupported-feature error — and the
// refusal is the SERVER's, so it holds against a client that never implemented
// the mcp_servers_on_create check. That is the whole point: the boundary cannot
// depend on client good behaviour.
//
// It also asserts the refusal is LOUD rather than a silent drop: no session is
// created and no engine is built. A client whose servers were quietly ignored
// would go on to run a session it believes has tools it does not have.
func TestSDKServerEnablers_Scenario9_McpServersRejectedOnTCPListener(t *testing.T) {
	t.Run("grpc: Unimplemented with the typed code", func(t *testing.T) {
		rec := &mcpSpecRecorder{}
		svc := clientMCPService(t, false, rec, nil, mockllm.New(mockllm.TextTurn("ok")))
		client, cleanup := dialGRPC(t, svc)
		defer cleanup()

		_, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
			Workspace:  "/ws",
			McpServers: []*mecatlv1.McpServerSpec{protoMCPEntry()},
		})
		if err == nil {
			t.Fatal("CreateSession with mcp_servers succeeded on a network-facing deployment; the boundary is not enforced by the server")
		}
		st, _ := status.FromError(err)
		if st.Code() != codes.Unimplemented {
			t.Fatalf("status = %v, want Unimplemented (this deployment does not offer the surface)", st.Code())
		}
		if !strings.Contains(errorCodeOf(t, st), "client_mcp_unsupported") {
			t.Fatalf("error carries no typed code; a client cannot tell this from version skew: %v", st.Proto())
		}
		if rec.count() != 0 {
			t.Fatalf("the refused request still built a per-session engine (%d factory calls)", rec.count())
		}
	})

	t.Run("http: 501 with the typed problem code", func(t *testing.T) {
		rec := &mcpSpecRecorder{}
		svc := clientMCPService(t, false, rec, nil, mockllm.New(mockllm.TextTurn("ok")))
		srv := httpFor(t, svc)

		code, body := postCreate(t, srv, map[string]any{
			"workspace":   "/ws",
			"mcp_servers": []any{httpMCPEntry()},
		})
		if code != http.StatusNotImplemented {
			t.Fatalf("status = %d, want 501; body=%s", code, body)
		}
		if !bytes.Contains(body, []byte("client_mcp_unsupported")) {
			t.Fatalf("problem body carries no typed code: %s", body)
		}
		if rec.count() != 0 {
			t.Fatalf("the refused request still built a per-session engine (%d factory calls)", rec.count())
		}
	})

	t.Run("an ordinary create is unaffected on the same deployment", func(t *testing.T) {
		// The refusal is scoped to the FIELD, not to the deployment's ability to
		// create sessions. A network-facing daemon must keep working normally.
		svc := clientMCPService(t, false, nil, nil, mockllm.New(mockllm.TextTurn("ok")))
		client, cleanup := dialGRPC(t, svc)
		defer cleanup()

		if _, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{Workspace: "/ws"}); err != nil {
			t.Fatalf("plain CreateSession on a network-facing deployment: %v", err)
		}
	})
}

// TestSDKServerEnablers_Scenario9_ClientMCPRefusalIsWireScoped pins the SCOPE of
// the refusal stated on Config.ClientMCPOnCreate: it gates the WIRE surface, not
// the in-process CreateSessionWithMCP entry the ACP adapter uses.
//
// ACP is a stdio peer of the operator's own editor and has no listener at all, so
// listener-derived policy is meaningless there — deriving one anyway would break
// editor MCP on every deployment that happens to serve a TCP port, for no security
// gain (the ACP peer already has the operator's shell). The two entries are
// distinct on purpose; this test is what stops a later refactor from "tidying"
// them into one and silently taking editor MCP away.
func TestSDKServerEnablers_Scenario9_ClientMCPRefusalIsWireScoped(t *testing.T) {
	rec := &mcpSpecRecorder{}
	svc := clientMCPService(t, false, rec, nil, mockllm.New(mockllm.TextTurn("ok")))

	if _, err := svc.CreateSessionWithMCP(context.Background(), "/ws", session.ModeDefault, session.Limits{}, []mcp.ServerConfig{{
		Name: "notes", URL: "https://mcp.example/mcp", Timeout: mcp.ClientConnectTimeout,
	}}); err != nil {
		t.Fatalf("the in-process ACP entry was refused by a WIRE deployment policy: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("factory calls = %d, want 1: the ACP path must still mount its servers", rec.count())
	}
}

// TestSDKServerEnablers_Scenario9_ClientMCPRejectedWithDebugTarget pins the
// pre-existing cross-field rule now reachable from a NEW path: a debug session
// selects already-configured server-global MCP by name and accepts no client
// endpoint, so combining mcp_servers with debug_target_session_id is a loud
// InvalidArgument rather than a silent drop of the servers the client asked for.
func TestSDKServerEnablers_Scenario9_ClientMCPRejectedWithDebugTarget(t *testing.T) {
	svc := clientMCPService(t, true, nil, nil, mockllm.New(mockllm.TextTurn("ok")))
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	_, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
		Profile:              "no-fs",
		DebugTargetSessionId: "some-target",
		McpServers:           []*mecatlv1.McpServerSpec{protoMCPEntry()},
	})
	if err == nil {
		t.Fatal("mcp_servers alongside a debug target was accepted; the servers would have been silently dropped")
	}
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument {
		t.Fatalf("status = %v, want InvalidArgument", st.Code())
	}
}

// --- AC9.3 -------------------------------------------------------------------

// TestSDKServerEnablers_Scenario9_ListenerScopedFeatureAdvertisement is AC9.3.
//
// mcp_servers_on_create appears in GetCompatibilityInfo.features ONLY on a
// deployment that permits it, on BOTH transports. The advertisement and the
// enforcement read the same Config value, so the test additionally pins the
// property that actually matters to a client: advertised implies accepted, and
// unadvertised implies refused. An advertisement that lies is worse than none —
// the client would have skipped its fallback path.
//
// The unrelated build-fact features stay advertised either way, which is what
// keeps "this deployment does not offer X" distinguishable from "this build is
// too old".
func TestSDKServerEnablers_Scenario9_ListenerScopedFeatureAdvertisement(t *testing.T) {
	for _, tc := range []struct {
		name   string
		permit bool
	}{
		{"permitting deployment advertises it", true},
		{"network-facing deployment does not", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := clientMCPService(t, tc.permit, nil, nil, mockllm.New(mockllm.TextTurn("ok")))

			client, cleanup := dialGRPC(t, svc)
			defer cleanup()
			info, err := client.GetCompatibilityInfo(context.Background(), &mecatlv1.GetCompatibilityInfoRequest{})
			if err != nil {
				t.Fatalf("GetCompatibilityInfo: %v", err)
			}
			grpcFeatures := info.GetFeatures()
			if got := slices.Contains(grpcFeatures, server.FeatureMCPServersOnCreate); got != tc.permit {
				t.Fatalf("gRPC features contains %q = %v, want %v (features = %v)", server.FeatureMCPServersOnCreate, got, tc.permit, grpcFeatures)
			}
			// A build fact is unaffected by deployment scope; otherwise a client
			// could not tell a scoped refusal from version skew.
			if !slices.Contains(grpcFeatures, server.FeatureServerInfo) {
				t.Fatalf("build-fact feature %q vanished under listener scoping: %v", server.FeatureServerInfo, grpcFeatures)
			}

			srv := httpFor(t, svc)
			resp, err := http.Get(srv.URL + "/v1/compatibility")
			if err != nil {
				t.Fatalf("GET /v1/compatibility: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			var httpInfo struct {
				Features []string `json:"features"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&httpInfo); err != nil {
				t.Fatalf("decode compatibility: %v", err)
			}
			if got := slices.Contains(httpInfo.Features, server.FeatureMCPServersOnCreate); got != tc.permit {
				t.Fatalf("HTTP features contains %q = %v, want %v (features = %v)", server.FeatureMCPServersOnCreate, got, tc.permit, httpInfo.Features)
			}
			// The two transports must never disagree: they read one registry
			// through one scope, and a client that probes either must be able to
			// act on the answer.
			if !slices.Equal(slices.Sorted(slices.Values(grpcFeatures)), slices.Sorted(slices.Values(httpInfo.Features))) {
				t.Fatalf("transports advertise different feature sets: grpc=%v http=%v", grpcFeatures, httpInfo.Features)
			}

			// Advertised implies accepted; unadvertised implies refused.
			_, err = client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
				Workspace:  "/ws",
				McpServers: []*mecatlv1.McpServerSpec{protoMCPEntry()},
			})
			if tc.permit && err != nil {
				t.Fatalf("advertised mcp_servers_on_create but refused the request: %v", err)
			}
			if !tc.permit && err == nil {
				t.Fatal("did not advertise mcp_servers_on_create but accepted the request; the advertisement is not the enforcement")
			}
		})
	}
}

// --- AC9.4 -------------------------------------------------------------------

// TestInvariant_no_stdio_mcp_ever pins the AGENTS.md hard invariant: "No stdio
// MCP, ever." mecatl never spawns an MCP server process, and sse is not a
// supported transport either — streaming-HTTP only.
//
// The invariant is UNCONDITIONAL, which is the specific thing this test exists to
// prove. A stdio or sse entry is rejected AS SUCH on EVERY deployment — including
// the one that permits client MCP — so the no-stdio rule is a property of the
// request shape, not a downstream consequence of a policy flag some future
// composition root might flip. That is why classification runs BEFORE the
// deployment gate in Service.ClientMCPFromWire: were the order reversed, a
// permitting deployment would be the only place the invariant was observable,
// and the guarantee would quietly depend on configuration.
//
// It covers the shared validator directly (every surface reaches it) and both
// wire transports on both deployment postures.
func TestInvariant_no_stdio_mcp_ever(t *testing.T) {
	// The rejected shapes, including the ACP-style command-shaped entry with no
	// explicit type — which must be classified as stdio rather than falling into
	// the vaguer "no recognized transport" arm.
	rejected := []struct {
		name string
		spec mcp.ClientServer
		want string
	}{
		{"explicit stdio", mcp.ClientServer{Name: "s", Type: "stdio", Command: "/usr/bin/thing"}, "stdio"},
		{"stdio with no command", mcp.ClientServer{Name: "s", Type: "stdio"}, "stdio"},
		{"command-shaped, untyped", mcp.ClientServer{Name: "s", Command: "/usr/bin/thing"}, "stdio"},
		{"command-shaped, untyped, with a url too", mcp.ClientServer{Name: "s", Command: "/usr/bin/thing", URL: "https://mcp.example/mcp"}, "stdio"},
		{"sse", mcp.ClientServer{Name: "s", Type: "sse", URL: "https://mcp.example/sse"}, "sse"},
	}

	t.Run("the shared validator rejects every spawn-shaped entry", func(t *testing.T) {
		for _, tc := range rejected {
			specs, err := mcp.PartitionClientServers([]mcp.ClientServer{tc.spec})
			if err == nil {
				t.Fatalf("%s: accepted; mecatl must never mount a %s MCP server", tc.name, tc.want)
			}
			if specs != nil {
				t.Fatalf("%s: returned specs alongside the error: %+v", tc.name, specs)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: error %q does not name the rejected transport %q", tc.name, err, tc.want)
			}
		}
	})

	// Both deployment postures, so the invariant is visibly independent of policy.
	for _, permit := range []bool{true, false} {
		t.Run(fmt.Sprintf("wire rejects it with ClientMCPOnCreate=%v", permit), func(t *testing.T) {
			for _, tc := range rejected {
				rec := &mcpSpecRecorder{}
				svc := clientMCPService(t, permit, rec, nil, mockllm.New(mockllm.TextTurn("ok")))

				client, cleanup := dialGRPC(t, svc)
				_, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
					Workspace: "/ws",
					McpServers: []*mecatlv1.McpServerSpec{{
						Name:    tc.spec.Name,
						Url:     tc.spec.URL,
						Type:    tc.spec.Type,
						Command: tc.spec.Command,
					}},
				})
				cleanup()
				if err == nil {
					t.Fatalf("grpc %s (permit=%v): accepted a %s entry", tc.name, permit, tc.want)
				}
				// The rejection must name the transport and be InvalidArgument on
				// BOTH postures. This is what pins the ORDER in ClientMCPFromWire:
				// if the deployment gate ran first, a refusing deployment would
				// answer Unimplemented here and the no-stdio rule would only be
				// observable where client MCP happens to be permitted — a guarantee
				// contingent on configuration, which is not an invariant.
				st, _ := status.FromError(err)
				if st.Code() != codes.InvalidArgument {
					t.Fatalf("grpc %s (permit=%v): status = %v, want InvalidArgument — a spawn-shaped entry is a MALFORMED request on every deployment, not an unsupported one", tc.name, permit, st.Code())
				}
				if !strings.Contains(st.Message(), tc.want) {
					t.Fatalf("grpc %s (permit=%v): message %q does not name the rejected transport", tc.name, permit, st.Message())
				}

				srv := httpFor(t, svc)
				code, body := postCreate(t, srv, map[string]any{
					"workspace": "/ws",
					"mcp_servers": []any{map[string]any{
						"name":    tc.spec.Name,
						"url":     tc.spec.URL,
						"type":    tc.spec.Type,
						"command": tc.spec.Command,
					}},
				})
				if code < 400 {
					t.Fatalf("http %s (permit=%v): status %d accepted a %s entry", tc.name, permit, code, tc.want)
				}
				if code != http.StatusBadRequest {
					t.Fatalf("http %s (permit=%v): status = %d, want 400 on every deployment", tc.name, permit, code)
				}
				if !bytes.Contains(body, []byte(tc.want)) {
					t.Fatalf("http %s (permit=%v): body does not name the rejected transport: %s", tc.name, permit, body)
				}
				if rec.count() != 0 {
					t.Fatalf("%s: a rejected entry still reached the engine factory", tc.name)
				}
			}
		})
	}
}

// --- AC9.5 -------------------------------------------------------------------

// TestSDKServerEnablers_Scenario9_McpHeadersNeverLogged is AC9.5, the
// secret-shaped invariant AGENTS.md states for AgentMCPServer.Headers, applied to
// the client-supplied MCP headers this scenario puts on the wire: a header value
// is never logged, never projected into an event, and never appears in an error.
//
// The value reaches EXACTLY ONE place — the mounted mcp.ServerConfig that AC9.1
// asserts — and nowhere else. A header is a bearer credential; a leak into a log
// line or an error body hands it to whoever reads operator output.
func TestSDKServerEnablers_Scenario9_McpHeadersNeverLogged(t *testing.T) {
	assertClean := func(t *testing.T, what, s string) {
		t.Helper()
		if strings.Contains(s, secretHeaderValue) {
			t.Fatalf("%s leaked the MCP header value: %s", what, s)
		}
		// Also catch a partial leak — a truncated or requoted credential is still a
		// credential.
		if strings.Contains(s, "zzz-scenario9-secret-token-zzz") {
			t.Fatalf("%s leaked part of the MCP header value: %s", what, s)
		}
	}

	t.Run("accepted mount: not in diagnostics, not in any event", func(t *testing.T) {
		diag := &capturingDiag{}
		rec := &mcpSpecRecorder{}
		llm := mockllm.New(
			mockllm.ToolCallTurn(call("c1", "mcp__notes__ping", `{}`)),
			mockllm.TextTurn("done"),
		)
		svc := clientMCPService(t, true, rec, diag, llm)
		client, cleanup := dialGRPC(t, svc)
		defer cleanup()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{
			Workspace:  "/ws",
			McpServers: []*mecatlv1.McpServerSpec{protoMCPEntry()},
		})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		// The create RESPONSE must not echo it back either.
		assertClean(t, "CreateSessionResponse", cs.String())

		stream, err := client.Converse(ctx)
		if err != nil {
			t.Fatalf("Converse: %v", err)
		}
		if err := stream.Send(&mecatlv1.ConverseRequest{
			Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "go"}},
		}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		_ = stream.CloseSend()
		for _, ev := range recvAll(t, stream) {
			assertClean(t, "event "+ev.GetType(), ev.String())
		}
		assertClean(t, "diagnostics", diag.String())

		// Positive control: the value DID cross the one seam it is meant to, so a
		// vacuous pass (the header never arriving at all) cannot masquerade as
		// containment.
		if got := rec.last(t)[0].Headers["Authorization"]; got != secretHeaderValue {
			t.Fatalf("the header never reached the mount (%q); this test would pass vacuously", got)
		}
	})

	t.Run("rejected requests: not in the error", func(t *testing.T) {
		// Every rejection arm, on both postures — a policy refusal, a bad URL, and a
		// stdio entry — each carrying the same credential.
		type arm struct {
			name   string
			permit bool
			url    string
			typ    string
			cmd    string
		}
		for _, a := range []arm{
			{"deployment refusal", false, "https://mcp.example/mcp", "http", ""},
			{"rejected url scheme", true, "ftp://mcp.example/mcp", "http", ""},
			{"plaintext to a non-loopback host", true, "http://mcp.example/mcp", "http", ""},
			{"stdio entry", true, "", "stdio", "/usr/bin/thing"},
		} {
			t.Run(a.name, func(t *testing.T) {
				diag := &capturingDiag{}
				svc := clientMCPService(t, a.permit, nil, diag, mockllm.New(mockllm.TextTurn("ok")))

				client, cleanup := dialGRPC(t, svc)
				_, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
					Workspace: "/ws",
					McpServers: []*mecatlv1.McpServerSpec{{
						Name:    "notes",
						Url:     a.url,
						Type:    a.typ,
						Command: a.cmd,
						Headers: map[string]string{"Authorization": secretHeaderValue},
					}},
				})
				cleanup()
				if err == nil {
					t.Fatal("expected a rejection")
				}
				assertClean(t, "grpc error", err.Error())

				srv := httpFor(t, svc)
				_, body := postCreate(t, srv, map[string]any{
					"workspace": "/ws",
					"mcp_servers": []any{map[string]any{
						"name":    "notes",
						"url":     a.url,
						"type":    a.typ,
						"command": a.cmd,
						"headers": map[string]string{"Authorization": secretHeaderValue},
					}},
				})
				assertClean(t, "http problem body", string(body))
				assertClean(t, "diagnostics", diag.String())
			})
		}
	})

	// The unreachable-server refusal is a SEPARATE error-composing site from the
	// four arms above: it is the only one that runs AFTER the specs (headers and
	// all) have crossed into the factory, and it is the only one that reports
	// per-server detail back to the caller. So it is the likeliest place for a
	// header to be appended "helpfully" while diagnosing a connection failure —
	// a mutation that leaked spec.Headers into the missing-server list passed
	// every other arm of this test.
	t.Run("unreachable server: not in the error", func(t *testing.T) {
		diag := &capturingDiag{}
		svc := clientMCPServiceUnreachable(t, true, nil, diag, mockllm.New(mockllm.TextTurn("ok")), []string{"notes"})

		client, cleanup := dialGRPC(t, svc)
		_, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
			Workspace:  "/ws",
			McpServers: []*mecatlv1.McpServerSpec{protoMCPEntry()},
		})
		cleanup()
		if err == nil {
			t.Fatal("expected the unreachable-server refusal")
		}
		assertClean(t, "grpc error", err.Error())

		srv := httpFor(t, svc)
		_, body := postCreate(t, srv, map[string]any{
			"workspace":   "/ws",
			"mcp_servers": []any{httpMCPEntry()},
		})
		assertClean(t, "http problem body", string(body))
		assertClean(t, "diagnostics", diag.String())
	})
}

// --- AC9.6 -------------------------------------------------------------------

// TestSDKServerEnablers_Scenario9_SingleMCPValidationPath is AC9.6: the wire path
// and the ACP path share ONE validation helper, and no second, divergent
// validator exists.
//
// Two halves, because either alone is weak. The STRUCTURAL half is the durable
// one: it parses the non-test sources of the two consuming adapters and asserts
// that neither calls mcp.ValidateClientURL nor re-implements the transport
// classification — only internal/adapter/mcp may. A future surface that grows its
// own partition function fails here rather than shipping a quiet divergence in
// which one entry point accepts what the other refuses. The BEHAVIOURAL half
// pins that the wire surface's rejections are the shared validator's, verbatim.
func TestSDKServerEnablers_Scenario9_SingleMCPValidationPath(t *testing.T) {
	t.Run("only the mcp adapter validates client MCP", func(t *testing.T) {
		// ValidateClientURL is the SSRF backstop; PartitionClientServers is the
		// classifier that wraps it. A consuming adapter may call the classifier (that
		// is the shared path) but must never reach past it to the URL check, which
		// would mean it had classified the entry itself.
		for _, pkg := range []string{"internal/adapter/acp", "internal/adapter/server"} {
			for _, file := range goFilesIn(t, pkg) {
				src := parseGo(t, file)
				ast.Inspect(src, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					ident, ok := sel.X.(*ast.Ident)
					if !ok || ident.Name != "mcp" {
						return true
					}
					if sel.Sel.Name == "ValidateClientURL" {
						t.Errorf("%s calls mcp.ValidateClientURL directly: client MCP entries must be classified ONLY through mcp.PartitionClientServers, or the two surfaces will diverge", file)
					}
					return true
				})
			}
		}
	})

	t.Run("no consuming adapter re-implements the transport classification", func(t *testing.T) {
		// The transport discriminants are string literals. Only the one classifier
		// may compare against them; a copy elsewhere is the divergence this AC bans.
		for _, pkg := range []string{"internal/adapter/acp", "internal/adapter/server"} {
			for _, file := range goFilesIn(t, pkg) {
				body, err := os.ReadFile(file)
				if err != nil {
					t.Fatalf("read %s: %v", file, err)
				}
				for _, lit := range []string{`== "stdio"`, `== "sse"`, `Type == "http"`} {
					if bytes.Contains(body, []byte(lit)) {
						t.Errorf("%s contains %s: the transport classification belongs to mcp.PartitionClientServers alone", file, lit)
					}
				}
			}
		}
	})

	t.Run("the wire rejection is the shared validator's, verbatim", func(t *testing.T) {
		svc := clientMCPService(t, true, nil, nil, mockllm.New(mockllm.TextTurn("ok")))
		client, cleanup := dialGRPC(t, svc)
		defer cleanup()

		bad := mcp.ClientServer{Name: "notes", Type: "http", URL: "ftp://mcp.example/mcp"}
		_, want := mcp.PartitionClientServers([]mcp.ClientServer{bad})
		if want == nil {
			t.Fatal("fixture is not actually rejected by the shared validator")
		}

		_, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
			Workspace:  "/ws",
			McpServers: []*mecatlv1.McpServerSpec{{Name: bad.Name, Type: bad.Type, Url: bad.URL}},
		})
		if err == nil {
			t.Fatal("the wire accepted a URL the shared validator rejects")
		}
		st, _ := status.FromError(err)
		if !strings.Contains(st.Message(), want.Error()) {
			t.Fatalf("wire message %q does not carry the shared validator's rejection %q; the wire is deciding for itself", st.Message(), want)
		}
	})

	t.Run("the server cap is the shared one", func(t *testing.T) {
		svc := clientMCPService(t, true, nil, nil, mockllm.New(mockllm.TextTurn("ok")))
		client, cleanup := dialGRPC(t, svc)
		defer cleanup()

		many := make([]*mecatlv1.McpServerSpec, 0, mcp.MaxClientServers+1)
		for i := range mcp.MaxClientServers + 1 {
			many = append(many, &mecatlv1.McpServerSpec{Name: fmt.Sprintf("s%d", i), Type: "http", Url: "https://mcp.example/mcp"})
		}
		if _, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{Workspace: "/ws", McpServers: many}); err == nil {
			t.Fatalf("accepted %d servers; the shared cap is %d (a create connects them serially)", len(many), mcp.MaxClientServers)
		}
	})
}

// goFilesIn returns the non-test .go files of a repo-relative package dir.
func goFilesIn(t *testing.T, pkg string) []string {
	t.Helper()
	dir := filepath.Join("..", "..", "..", pkg)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	if len(out) == 0 {
		t.Fatalf("no non-test Go files found under %s: the scan would pass vacuously", dir)
	}
	return out
}

// errorCodeOf extracts the stable ErrorInfo.Reason a typed server error carries,
// which is the DISAMBIGUATOR a client switches on: the coarse gRPC status alone
// cannot separate a scoped deployment refusal from version skew.
func errorCodeOf(t *testing.T, st *status.Status) string {
	t.Helper()
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	t.Fatalf("no ErrorInfo detail on the status: %v", st.Proto())
	return ""
}

func parseGo(t *testing.T, file string) *ast.File {
	t.Helper()
	src, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return src
}

// TestSDKServerEnablers_Scenario9_PartialMountFailsTheWireCreate closes the
// silent-degradation hole raised in review on PR #903.
//
// Connecting client MCP is BEST-EFFORT in composition: mcp.NewManager connects
// every server concurrently, keeps the ones that answered, drops the rest with an
// operator WARN, and returns an error only when EVERY server failed. So before
// this check, a wire client could ask for three servers, have two connect, and
// receive a perfectly ordinary 201/OK with a session id — running a session that
// silently lacks a third of the tools it asked for, with nothing in the response
// to distinguish that from success. The all-failed case was worse: a session with
// NO client tools at all, still reported as created.
//
// The contract is therefore ALL-OR-NOTHING on the wire, and the failure is a
// DISTINCT code from the deployment refusal: client_mcp_unreachable is transient
// and the client's own to fix, client_mcp_unsupported is permanent.
func TestSDKServerEnablers_Scenario9_PartialMountFailsTheWireCreate(t *testing.T) {
	twoServers := []*mecatlv1.McpServerSpec{
		{Name: "notes", Url: "https://notes.example/mcp", Type: "http"},
		{Name: "calendar", Url: "https://cal.example/mcp", Type: "http"},
	}

	t.Run("grpc: one of two unreachable", func(t *testing.T) {
		rec := &mcpSpecRecorder{}
		svc := clientMCPServiceUnreachable(t, true, rec, nil, mockllm.New(mockllm.TextTurn("ok")), []string{"calendar"})
		client, cleanup := dialGRPC(t, svc)
		defer cleanup()

		_, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
			Workspace:  "/ws",
			McpServers: twoServers,
		})
		if err == nil {
			t.Fatal("a create whose MCP servers only PARTIALLY mounted succeeded: the client cannot detect the missing tools")
		}
		st, _ := status.FromError(err)
		if st.Code() != codes.Unavailable {
			t.Fatalf("code = %v, want Unavailable (transient, the client's endpoint to fix)", st.Code())
		}
		if code := errorCodeOf(t, st); code != "client_mcp_unreachable" {
			t.Fatalf("typed code = %q, want client_mcp_unreachable", code)
		}
		// The message must name WHICH server, or the client cannot act on it.
		if !strings.Contains(st.Message(), "calendar") {
			t.Fatalf("error does not name the unreachable server: %q", st.Message())
		}
		// ...and must NOT indict the one that worked.
		if strings.Contains(st.Message(), "notes") {
			t.Fatalf("error names a server that DID mount: %q", st.Message())
		}
		// The engine was built (the factory ran) and must have been torn down
		// rather than left registered against a session that does not exist.
		if rec.count() != 1 {
			t.Fatalf("factory calls = %d, want 1", rec.count())
		}
		if rec.closeCount() != 1 {
			t.Fatalf("Close calls = %d, want 1: a refused create must tear the built engine down, not leak its MCP connections", rec.closeCount())
		}
		assertNoLiveSessions(t, svc)
	})

	t.Run("grpc: every server unreachable", func(t *testing.T) {
		svc := clientMCPServiceUnreachable(t, true, nil, nil, mockllm.New(mockllm.TextTurn("ok")), []string{"notes", "calendar"})
		client, cleanup := dialGRPC(t, svc)
		defer cleanup()

		_, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
			Workspace:  "/ws",
			McpServers: twoServers,
		})
		if err == nil {
			t.Fatal("a create whose MCP servers ALL failed to mount succeeded with a core-only engine")
		}
		st, _ := status.FromError(err)
		if code := errorCodeOf(t, st); code != "client_mcp_unreachable" {
			t.Fatalf("typed code = %q, want client_mcp_unreachable", code)
		}
		assertNoLiveSessions(t, svc)
	})

	t.Run("http: partial mount is 503, not 201", func(t *testing.T) {
		svc := clientMCPServiceUnreachable(t, true, nil, nil, mockllm.New(mockllm.TextTurn("ok")), []string{"calendar"})
		srv := httpFor(t, svc)

		code, body := postCreate(t, srv, map[string]any{
			"workspace": "/ws",
			"mcp_servers": []map[string]any{
				{"name": "notes", "url": "https://notes.example/mcp", "type": "http"},
				{"name": "calendar", "url": "https://cal.example/mcp", "type": "http"},
			},
		})
		if code != http.StatusServiceUnavailable {
			t.Fatalf("POST /v1/sessions = %d, want 503; body=%s", code, body)
		}
		var problem struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(body, &problem); err != nil {
			t.Fatalf("decode problem: %v; body=%s", err, body)
		}
		if problem.Code != "client_mcp_unreachable" {
			t.Fatalf("problem code = %q, want client_mcp_unreachable", problem.Code)
		}
		if !strings.Contains(problem.Detail, "calendar") {
			t.Fatalf("detail does not name the unreachable server: %q", problem.Detail)
		}
		assertNoLiveSessions(t, svc)
	})

	t.Run("full mount still succeeds", func(t *testing.T) {
		// The guard must not be a blanket refusal: the happy path is unchanged.
		svc := clientMCPServiceUnreachable(t, true, nil, nil, mockllm.New(mockllm.TextTurn("ok")), nil)
		client, cleanup := dialGRPC(t, svc)
		defer cleanup()

		cs, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
			Workspace:  "/ws",
			McpServers: twoServers,
		})
		if err != nil {
			t.Fatalf("a create whose servers ALL mounted was refused: %v", err)
		}
		if cs.GetSessionId() == "" {
			t.Fatal("no session id on a fully-mounted create")
		}
	})
}

// TestSDKServerEnablers_Scenario9_PartialMountToleratedOnACPPath pins the OTHER
// half of the all-or-nothing decision: it is the WIRE contract, not a global one.
//
// The ACP peer is the operator's own editor. A flaky editor-side MCP server should
// cost the user some tools, not their whole session, and ACP has its own channel
// to report it — which is why composition's mount stays best-effort and only
// WithClientMCP (the wire's sole entry) arms the strict check. Without this test,
// "fix the silent partial mount" would plausibly be implemented one layer down in
// composition and break editor sessions on every deployment.
func TestSDKServerEnablers_Scenario9_PartialMountToleratedOnACPPath(t *testing.T) {
	rec := &mcpSpecRecorder{}
	svc := clientMCPServiceUnreachable(t, true, rec, nil, mockllm.New(mockllm.TextTurn("ok")), []string{"calendar"})

	sess, err := svc.CreateSessionWithMCP(context.Background(), "/ws", session.ModeDefault, session.Limits{}, []mcp.ServerConfig{
		{Name: "notes", URL: "https://notes.example/mcp", Timeout: mcp.ClientConnectTimeout},
		{Name: "calendar", URL: "https://cal.example/mcp", Timeout: mcp.ClientConnectTimeout},
	})
	if err != nil {
		t.Fatalf("the ACP entry must tolerate a partial mount, but it failed: %v", err)
	}
	if sess == nil || sess.ID == "" {
		t.Fatal("ACP create returned no session")
	}
	if rec.count() != 1 {
		t.Fatalf("factory calls = %d, want 1", rec.count())
	}
}

// TestSDKServerEnablers_Scenario9_UnreportedMountFailsClosed pins the direction of
// the ambiguous case: a factory reached through the WIRE that reports NO mounted
// servers while servers were requested fails the create.
//
// The alternative — treat an empty MountedClientMCP as "made no claim" and pass —
// would make the guarantee opt-out by omission: any future factory that forgot the
// field would silently restore the exact silent-partial-mount bug this check
// exists to prevent, and it would pass its tests while doing so.
func TestSDKServerEnablers_Scenario9_UnreportedMountFailsClosed(t *testing.T) {
	svc := mustService(t, server.Config{
		Engine:            sharedTestEngine(),
		Store:             memstore.New(),
		Workspaces:        func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		DefaultWorkspace:  "/ws",
		Now:               func() time.Time { return time.Unix(0, 0) },
		ClientMCPOnCreate: true,
		// A factory that mounts specs but never populates MountedClientMCP.
		SessionEngine: func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
			return server.SessionEngineResult{
				Engine: sharedTestEngine(),
				Close:  func() error { return nil },
			}, nil
		},
	})
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	_, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
		Workspace:  "/ws",
		McpServers: []*mecatlv1.McpServerSpec{protoMCPEntry()},
	})
	if err == nil {
		t.Fatal("a factory that reported no mounted servers produced a successful create: the check is opt-out by omission")
	}
	st, _ := status.FromError(err)
	if code := errorCodeOf(t, st); code != "client_mcp_unreachable" {
		t.Fatalf("typed code = %q, want client_mcp_unreachable", code)
	}

	// An ordinary create with NO mcp_servers must be entirely unaffected: the
	// check keys on a REQUEST for servers, never on the field being unpopulated.
	if _, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{Workspace: "/ws"}); err != nil {
		t.Fatalf("ordinary create broke: %v", err)
	}
}

// --- shared helpers for the mount-enforcement tests --------------------------

func sharedTestEngine() *agent.Engine {
	return agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("shared")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, permstore.New()),
		Model:   "test-model",
	})
}

func mustService(t *testing.T, cfg server.Config) *server.Service {
	t.Helper()
	svc, err := server.NewService(cfg)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// assertNoLiveSessions proves a REFUSED create left nothing behind — no session in
// the store and no per-session engine holding an MCP connection. A guard that
// returns an error but keeps the half-built session would trade a detectable
// failure for a leak.
func assertNoLiveSessions(t *testing.T, svc *server.Service) {
	t.Helper()
	sessions, err := svc.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("a refused create left %d session(s) behind", len(sessions))
	}
}
