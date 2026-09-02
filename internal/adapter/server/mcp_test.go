package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcp/source"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// fakeProvider is an offline mcp.Provider stub: canned resources/prompts keyed
// by server name, with injectable errors for the read/get paths. It is the only
// MCP dependency the inspection RPC tests need — no SDK, no network.
type fakeProvider struct {
	resources map[string][]mcp.Resource // server -> resources ("" key = the union answer)
	prompts   map[string][]mcp.Prompt
	readFn    func(srv, uri string) (mcp.ResourceContents, error)
	getFn     func(srv, name string, args map[string]string) (mcp.PromptResult, error)
}

func (f *fakeProvider) ListResources(_ context.Context, srv string) ([]mcp.Resource, error) {
	return f.resources[srv], nil
}

func (f *fakeProvider) ReadResource(_ context.Context, srv, uri string) (mcp.ResourceContents, error) {
	return f.readFn(srv, uri)
}

func (f *fakeProvider) ListPrompts(_ context.Context, srv string) ([]mcp.Prompt, error) {
	return f.prompts[srv], nil
}

func (f *fakeProvider) GetPrompt(_ context.Context, srv, name string, args map[string]string) (mcp.PromptResult, error) {
	return f.getFn(srv, name, args)
}

// CallTool is unused by the inspection RPC tests; it returns a sentinel error
// so an accidental call surfaces clearly rather than reporting a phantom result.
func (fakeProvider) CallTool(_ context.Context, srv, name string, _ json.RawMessage) (mcp.CallResult, error) {
	return mcp.CallResult{}, fmt.Errorf("%w: %q.%q", mcp.ErrUnknownServer, srv, name)
}

// cannedProvider returns a fakeProvider with two servers, per-server and union
// resource/prompt snapshots, and read/get funcs covering happy + error cases.
func cannedProvider() *fakeProvider {
	all := []mcp.Resource{
		{Server: "alpha", URI: "file:///a.txt", Name: "a", Title: "A", Description: "first", MIMEType: "text/plain", Size: 3, ReadOnly: true},
		{Server: "beta", URI: "file:///b.bin", Name: "b", MIMEType: "application/octet-stream", Size: 4, ReadOnly: true},
	}
	allPrompts := []mcp.Prompt{
		{Server: "alpha", Name: "greet", Title: "Greet", Description: "say hi", Arguments: []mcp.PromptArgument{{Name: "who", Required: true}}},
		{Server: "beta", Name: "bye", Description: "say bye"},
	}
	return &fakeProvider{
		resources: map[string][]mcp.Resource{
			"":      all,
			"alpha": {all[0]},
		},
		prompts: map[string][]mcp.Prompt{
			"":      allPrompts,
			"alpha": {allPrompts[0]},
		},
		readFn: func(srv, uri string) (mcp.ResourceContents, error) {
			switch {
			case srv != "alpha" && srv != "beta":
				// Unknown server name: a client error, reported as the routing
				// sentinel a real *Manager.byName would return.
				return mcp.ResourceContents{}, fmt.Errorf("%w: %q", mcp.ErrUnknownServer, srv)
			case srv == "alpha" && uri == "file:///a.txt":
				return mcp.ResourceContents{URI: uri, MIMEType: "text/plain", Text: "hi"}, nil
			case srv == "beta" && uri == "file:///b.bin":
				return mcp.ResourceContents{URI: uri, MIMEType: "application/octet-stream", Blob: []byte{0x00, 0x01, 0x02}}, nil
			default:
				// Known server, unknown/failed read: a downstream fault, not a
				// client error.
				return mcp.ResourceContents{}, errors.New("unknown resource")
			}
		},
		getFn: func(srv, name string, args map[string]string) (mcp.PromptResult, error) {
			if srv != "alpha" && srv != "beta" {
				return mcp.PromptResult{}, fmt.Errorf("%w: %q", mcp.ErrUnknownServer, srv)
			}
			if name == "greet" {
				if _, ok := args["who"]; !ok {
					return mcp.PromptResult{}, errors.New("missing required argument: who")
				}
				return mcp.PromptResult{Description: "greeting", Messages: []mcp.PromptMessage{{Role: "user", Text: "hi " + args["who"]}}}, nil
			}
			return mcp.PromptResult{}, errors.New("unknown prompt")
		},
	}
}

// cannedSources is a representative inventory: a static source and two ToolHive
// sources (one duplicating a group) plus one with an empty group.
func cannedSources() []source.SourceInfo {
	return []source.SourceInfo{
		{Name: "static", Kind: "static", Enabled: true, Servers: []source.ServerInfo{{Name: "alpha", URL: "http://a", Transport: "streamable-http"}}},
		{Name: "toolhive(default)", Kind: "toolhive", Enabled: true, Group: "default", Servers: []source.ServerInfo{{Name: "beta", URL: "http://b", Transport: "streamable-http", Group: "default"}}, Diagnostics: []string{"skipped foo"}},
		{Name: "toolhive(prod)", Kind: "toolhive", Enabled: true, Group: "prod"},
		{Name: "toolhive(default)#2", Kind: "toolhive", Enabled: true, Group: "default"}, // duplicate group
		{Name: "toolhive(blank)", Kind: "toolhive", Enabled: true, Group: ""},            // empty group: excluded
	}
}

// mcpService builds a server.Service wired with a (possibly nil) provider and
// inventory; the engine/store/workspace are real offline stubs.
func mcpService(t *testing.T, provider mcp.Provider, sources []source.SourceInfo) *server.Service {
	t.Helper()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(allowRules(), nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:      engine,
		Store:       memstore.New(),
		Workspaces:  func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:         func() time.Time { return time.Unix(0, 0) },
		MCPProvider: provider,
		MCPSources:  sources,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func TestGRPCListMcpResources(t *testing.T) {
	svc := mcpService(t, cannedProvider(), cannedSources())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx := context.Background()

	// Union (empty server) returns both.
	all, err := client.ListMcpResources(ctx, &mecatlv1.ListMcpResourcesRequest{})
	if err != nil {
		t.Fatalf("ListMcpResources: %v", err)
	}
	if len(all.GetResources()) != 2 {
		t.Fatalf("union resources = %d, want 2", len(all.GetResources()))
	}
	first := all.GetResources()[0]
	if first.GetServer() != "alpha" || first.GetUri() != "file:///a.txt" || !first.GetReadOnly() || first.GetSize() != 3 {
		t.Fatalf("resource[0] = %+v", first)
	}

	// Per-server filter returns just alpha's.
	one, err := client.ListMcpResources(ctx, &mecatlv1.ListMcpResourcesRequest{Server: "alpha"})
	if err != nil {
		t.Fatalf("ListMcpResources(alpha): %v", err)
	}
	if len(one.GetResources()) != 1 || one.GetResources()[0].GetServer() != "alpha" {
		t.Fatalf("alpha resources = %+v", one.GetResources())
	}
}

func TestGRPCReadMcpResource(t *testing.T) {
	svc := mcpService(t, cannedProvider(), cannedSources())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx := context.Background()

	// Text resource.
	txt, err := client.ReadMcpResource(ctx, &mecatlv1.ReadMcpResourceRequest{Server: "alpha", Uri: "file:///a.txt"})
	if err != nil {
		t.Fatalf("ReadMcpResource(text): %v", err)
	}
	if len(txt.GetContents()) != 1 || txt.GetContents()[0].GetText() != "hi" {
		t.Fatalf("text contents = %+v", txt.GetContents())
	}

	// Binary resource: blob passes through unchanged, no base64 mangling.
	bin, err := client.ReadMcpResource(ctx, &mecatlv1.ReadMcpResourceRequest{Server: "beta", Uri: "file:///b.bin"})
	if err != nil {
		t.Fatalf("ReadMcpResource(binary): %v", err)
	}
	got := bin.GetContents()[0].GetBlob()
	if string(got) != string([]byte{0x00, 0x01, 0x02}) {
		t.Fatalf("blob = %v, want [0 1 2]", got)
	}

	// Unknown SERVER name is a client error → InvalidArgument.
	_, err = client.ReadMcpResource(ctx, &mecatlv1.ReadMcpResourceRequest{Server: "nope", Uri: "file:///a.txt"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown-server read code = %v, want InvalidArgument", status.Code(err))
	}

	// Known server, failed read (transport/downstream fault) → Internal, NOT
	// InvalidArgument: the client did nothing wrong.
	_, err = client.ReadMcpResource(ctx, &mecatlv1.ReadMcpResourceRequest{Server: "alpha", Uri: "file:///missing"})
	if status.Code(err) != codes.Internal {
		t.Fatalf("downstream-fault read code = %v, want Internal", status.Code(err))
	}

	// Missing required args → InvalidArgument (server-side guard).
	_, err = client.ReadMcpResource(ctx, &mecatlv1.ReadMcpResourceRequest{Server: "", Uri: ""})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty read code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestGRPCListMcpPrompts(t *testing.T) {
	svc := mcpService(t, cannedProvider(), cannedSources())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx := context.Background()

	all, err := client.ListMcpPrompts(ctx, &mecatlv1.ListMcpPromptsRequest{})
	if err != nil {
		t.Fatalf("ListMcpPrompts: %v", err)
	}
	if len(all.GetPrompts()) != 2 {
		t.Fatalf("union prompts = %d, want 2", len(all.GetPrompts()))
	}
	greet := all.GetPrompts()[0]
	if greet.GetName() != "greet" || len(greet.GetArguments()) != 1 || !greet.GetArguments()[0].GetRequired() {
		t.Fatalf("prompt[0] = %+v", greet)
	}

	one, err := client.ListMcpPrompts(ctx, &mecatlv1.ListMcpPromptsRequest{Server: "alpha"})
	if err != nil {
		t.Fatalf("ListMcpPrompts(alpha): %v", err)
	}
	if len(one.GetPrompts()) != 1 {
		t.Fatalf("alpha prompts = %d, want 1", len(one.GetPrompts()))
	}
}

func TestGRPCGetMcpPrompt(t *testing.T) {
	svc := mcpService(t, cannedProvider(), cannedSources())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx := context.Background()

	// Happy path.
	res, err := client.GetMcpPrompt(ctx, &mecatlv1.GetMcpPromptRequest{Server: "alpha", Name: "greet", Arguments: map[string]string{"who": "ozz"}})
	if err != nil {
		t.Fatalf("GetMcpPrompt: %v", err)
	}
	if res.GetDescription() != "greeting" || len(res.GetMessages()) != 1 || res.GetMessages()[0].GetText() != "hi ozz" {
		t.Fatalf("prompt result = %+v", res)
	}

	// Unknown SERVER name is a client error → InvalidArgument.
	_, err = client.GetMcpPrompt(ctx, &mecatlv1.GetMcpPromptRequest{Server: "nope", Name: "greet", Arguments: map[string]string{"who": "ozz"}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown-server get code = %v, want InvalidArgument", status.Code(err))
	}

	// Missing required arg on a KNOWN server is a downstream expansion fault →
	// Internal (the empty-required-field server guard is for server/name, not
	// prompt arguments).
	_, err = client.GetMcpPrompt(ctx, &mecatlv1.GetMcpPromptRequest{Server: "alpha", Name: "greet"})
	if status.Code(err) != codes.Internal {
		t.Fatalf("missing-arg code = %v, want Internal", status.Code(err))
	}

	// Missing required fields (server/name) → InvalidArgument.
	_, err = client.GetMcpPrompt(ctx, &mecatlv1.GetMcpPromptRequest{Server: "", Name: ""})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty-fields code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestGRPCListMcpSources(t *testing.T) {
	svc := mcpService(t, cannedProvider(), cannedSources())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	resp, err := client.ListMcpSources(context.Background(), &mecatlv1.ListMcpSourcesRequest{})
	if err != nil {
		t.Fatalf("ListMcpSources: %v", err)
	}
	if len(resp.GetSources()) != 5 {
		t.Fatalf("sources = %d, want 5", len(resp.GetSources()))
	}
	static := resp.GetSources()[0]
	if static.GetKind() != "static" || len(static.GetServers()) != 1 || static.GetServers()[0].GetName() != "alpha" {
		t.Fatalf("static source = %+v", static)
	}
	th := resp.GetSources()[1]
	if th.GetKind() != "toolhive" || th.GetGroup() != "default" || len(th.GetDiagnostics()) != 1 {
		t.Fatalf("toolhive source = %+v", th)
	}
}

func TestGRPCListToolHiveGroups(t *testing.T) {
	svc := mcpService(t, cannedProvider(), cannedSources())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	resp, err := client.ListToolHiveGroups(context.Background(), &mecatlv1.ListToolHiveGroupsRequest{})
	if err != nil {
		t.Fatalf("ListToolHiveGroups: %v", err)
	}
	// Distinct, non-empty, toolhive-kind only, sorted: default + prod (the
	// static source's absent group and the blank toolhive group are excluded;
	// the duplicate "default" is collapsed).
	got := resp.GetGroups()
	if len(got) != 2 || got[0] != "default" || got[1] != "prod" {
		t.Fatalf("groups = %v, want [default prod]", got)
	}
}

// TestServiceMcpSourceProberReflectsLiveStatus asserts that when a MCPSourceProber
// is configured, ListMcpSources/ListToolHiveGroups re-consult it on every call —
// so a source whose status changed since startup (a server reconnected, a new
// group appeared) is reflected, not the cached startup snapshot.
func TestServiceMcpSourceProberReflectsLiveStatus(t *testing.T) {
	startup := cannedSources()
	// The "live" inventory differs: the toolhive(default) diagnostic cleared and a
	// brand-new toolhive group ("staging") appeared.
	live := []source.SourceInfo{
		{Name: "static", Kind: "static", Enabled: true, Servers: []source.ServerInfo{{Name: "alpha", URL: "http://a", Transport: "streamable-http"}}},
		{Name: "toolhive(default)", Kind: "toolhive", Enabled: true, Group: "default", Servers: []source.ServerInfo{{Name: "beta", URL: "http://b", Transport: "streamable-http", Group: "default"}}},
		{Name: "toolhive(staging)", Kind: "toolhive", Enabled: true, Group: "staging"},
	}
	var probeCalls int
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(allowRules(), nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:     engine,
		Store:      memstore.New(),
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(0, 0) },
		MCPSources: startup, // cached fallback
		MCPSourceProber: func(_ context.Context) []source.SourceInfo {
			probeCalls++
			return live
		},
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	got := svc.ListMcpSources(context.Background())
	if len(got) != 3 {
		t.Fatalf("live sources = %d, want 3 (prober result, not the 5-source snapshot)", len(got))
	}
	if len(got[1].Diagnostics) != 0 {
		t.Fatalf("toolhive diagnostic not cleared on re-probe: %#v", got[1].Diagnostics)
	}
	groups := svc.ListToolHiveGroups(context.Background())
	if len(groups) != 2 || groups[0] != "default" || groups[1] != "staging" {
		t.Fatalf("live groups = %v, want [default staging]", groups)
	}
	if probeCalls != 2 { // once per List call — re-consulted every time
		t.Fatalf("prober called %d times, want 2", probeCalls)
	}
}

// TestServiceMcpSourceProberFailSoft asserts a prober that returns nil (a failed
// re-probe) falls back to the cached startup snapshot, so the panel still renders.
func TestServiceMcpSourceProberFailSoft(t *testing.T) {
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(allowRules(), nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:          engine,
		Store:           memstore.New(),
		Workspaces:      func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:             func() time.Time { return time.Unix(0, 0) },
		MCPSources:      cannedSources(),
		MCPSourceProber: func(_ context.Context) []source.SourceInfo { return nil },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if got := svc.ListMcpSources(context.Background()); len(got) != 5 {
		t.Fatalf("fail-soft sources = %d, want 5 (cached snapshot)", len(got))
	}
}

func TestGRPCMcpNilProvider(t *testing.T) {
	// No provider configured: lists degrade to empty; read/get require a
	// provider and signal FailedPrecondition.
	svc := mcpService(t, nil, cannedSources())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx := context.Background()

	res, err := client.ListMcpResources(ctx, &mecatlv1.ListMcpResourcesRequest{})
	if err != nil || len(res.GetResources()) != 0 {
		t.Fatalf("nil-provider resources = %+v, err %v", res.GetResources(), err)
	}
	ps, err := client.ListMcpPrompts(ctx, &mecatlv1.ListMcpPromptsRequest{})
	if err != nil || len(ps.GetPrompts()) != 0 {
		t.Fatalf("nil-provider prompts = %+v, err %v", ps.GetPrompts(), err)
	}

	_, err = client.ReadMcpResource(ctx, &mecatlv1.ReadMcpResourceRequest{Server: "alpha", Uri: "file:///a.txt"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("nil-provider read code = %v, want FailedPrecondition", status.Code(err))
	}
	_, err = client.GetMcpPrompt(ctx, &mecatlv1.GetMcpPromptRequest{Server: "alpha", Name: "greet"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("nil-provider get code = %v, want FailedPrecondition", status.Code(err))
	}

	// Sources still flow through; toolhive-groups still derived.
	src, err := client.ListMcpSources(ctx, &mecatlv1.ListMcpSourcesRequest{})
	if err != nil || len(src.GetSources()) != 5 {
		t.Fatalf("nil-provider sources = %d, err %v", len(src.GetSources()), err)
	}
}

// httpGet issues a GET against srv and returns the status code and decoded body
// into v (v may be nil to skip decoding).
func httpGet(t *testing.T, srv *httptest.Server, path string, v any) int {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if v != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	return resp.StatusCode
}

// TestHTTPMcpEndpoints exercises the HTTP mirror for the read and groups paths,
// confirming the surfaces share one shape and the precondition mapping.
func TestHTTPMcpEndpoints(t *testing.T) {
	svc := mcpService(t, cannedProvider(), cannedSources())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	// GET resources read (binary blob passthrough as base64 in JSON).
	var read mecatlv1.ReadMcpResourceResponse
	if code := httpGet(t, srv, "/v1/mcp/resources/read?server=beta&uri=file:///b.bin", &read); code != 200 {
		t.Fatalf("read status = %d", code)
	}
	if len(read.GetContents()) != 1 || len(read.GetContents()[0].GetBlob()) != 3 {
		t.Fatalf("http read contents = %+v", read.GetContents())
	}

	// GET toolhive groups.
	var groups mecatlv1.ListToolHiveGroupsResponse
	httpGet(t, srv, "/v1/mcp/toolhive/groups", &groups)
	if len(groups.GetGroups()) != 2 {
		t.Fatalf("http groups = %v", groups.GetGroups())
	}

	// Missing query args → 400.
	if code := httpGet(t, srv, "/v1/mcp/resources/read?server=alpha", nil); code != 400 {
		t.Fatalf("missing uri status = %d", code)
	}

	// Unknown server name → 400 (client error).
	if code := httpGet(t, srv, "/v1/mcp/resources/read?server=nope&uri=file:///a.txt", nil); code != 400 {
		t.Fatalf("unknown-server read status = %d, want 400", code)
	}

	// Known server, downstream read fault → 5xx (not a client error).
	if code := httpGet(t, srv, "/v1/mcp/resources/read?server=alpha&uri=file:///missing", nil); code < 500 {
		t.Fatalf("downstream-fault read status = %d, want 5xx", code)
	}
}

func TestHTTPMcpNilProviderPrecondition(t *testing.T) {
	svc := mcpService(t, nil, cannedSources())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	if code := httpGet(t, srv, "/v1/mcp/resources/read?server=alpha&uri=file:///a.txt", nil); code != 412 {
		t.Fatalf("nil-provider http read status = %d, want 412", code)
	}
}
