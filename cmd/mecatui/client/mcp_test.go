package client

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// fakeHarnessClient is a scripted HarnessServiceClient for the MCP wrapper tests.
// It embeds the interface (so it satisfies it without spelling out the streaming
// methods) and overrides only the six MCP unary RPCs. Each override returns its
// canned response or a canned error, so the proto→plain mapping and the
// gRPC-status → MCPErrorClass classification run offline with no server.
type fakeHarnessClient struct {
	mecatlv1.HarnessServiceClient // embedded; unset methods panic if called

	resources  *mecatlv1.ListMcpResourcesResponse
	contents   *mecatlv1.ReadMcpResourceResponse
	prompts    *mecatlv1.ListMcpPromptsResponse
	prompt     *mecatlv1.GetMcpPromptResponse
	sources    *mecatlv1.ListMcpSourcesResponse
	groups     *mecatlv1.ListToolHiveGroupsResponse
	connectors *mecatlv1.ListSessionMcpConnectorsResponse

	err error // when non-nil, every RPC returns it

	// captured requests for round-trip assertions.
	lastResReq    *mecatlv1.ListMcpResourcesRequest
	lastReadReq   *mecatlv1.ReadMcpResourceRequest
	lastPromptReq *mecatlv1.GetMcpPromptRequest
}

func (f *fakeHarnessClient) ListMcpResources(_ context.Context, in *mecatlv1.ListMcpResourcesRequest, _ ...grpc.CallOption) (*mecatlv1.ListMcpResourcesResponse, error) {
	f.lastResReq = in
	if f.err != nil {
		return nil, f.err
	}
	return f.resources, nil
}

func (f *fakeHarnessClient) ReadMcpResource(_ context.Context, in *mecatlv1.ReadMcpResourceRequest, _ ...grpc.CallOption) (*mecatlv1.ReadMcpResourceResponse, error) {
	f.lastReadReq = in
	if f.err != nil {
		return nil, f.err
	}
	return f.contents, nil
}

func (f *fakeHarnessClient) ListMcpPrompts(_ context.Context, _ *mecatlv1.ListMcpPromptsRequest, _ ...grpc.CallOption) (*mecatlv1.ListMcpPromptsResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.prompts, nil
}

func (f *fakeHarnessClient) GetMcpPrompt(_ context.Context, in *mecatlv1.GetMcpPromptRequest, _ ...grpc.CallOption) (*mecatlv1.GetMcpPromptResponse, error) {
	f.lastPromptReq = in
	if f.err != nil {
		return nil, f.err
	}
	return f.prompt, nil
}

func (f *fakeHarnessClient) ListMcpSources(_ context.Context, _ *mecatlv1.ListMcpSourcesRequest, _ ...grpc.CallOption) (*mecatlv1.ListMcpSourcesResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.sources, nil
}

func (f *fakeHarnessClient) ListToolHiveGroups(_ context.Context, _ *mecatlv1.ListToolHiveGroupsRequest, _ ...grpc.CallOption) (*mecatlv1.ListToolHiveGroupsResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.groups, nil
}

func (f *fakeHarnessClient) ListSessionMcpConnectors(_ context.Context, _ *mecatlv1.ListSessionMcpConnectorsRequest, _ ...grpc.CallOption) (*mecatlv1.ListSessionMcpConnectorsResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.connectors, nil
}

// newFakeClient wraps a fakeHarnessClient in a *Client so the wrappers exercise
// the real svc call path.
func newFakeClient(svc mecatlv1.HarnessServiceClient) *Client {
	return &Client{svc: svc}
}

func TestListMCPResourcesMapping(t *testing.T) {
	fake := &fakeHarnessClient{resources: &mecatlv1.ListMcpResourcesResponse{
		Resources: []*mecatlv1.McpResource{{
			Server: "srv", Uri: "file://a", Name: "a", Title: "A", Description: "d",
			MimeType: "text/plain", Size: 12, ReadOnly: true,
		}},
	}}
	cl := newFakeClient(fake)

	got, err := cl.ListMCPResources(context.Background(), "srv")
	if err != nil {
		t.Fatalf("ListMCPResources: %v", err)
	}
	if fake.lastResReq.GetServer() != "srv" {
		t.Errorf("request server = %q, want srv", fake.lastResReq.GetServer())
	}
	if len(got) != 1 {
		t.Fatalf("got %d resources, want 1", len(got))
	}
	want := MCPResource{
		Server: "srv", URI: "file://a", Name: "a", Title: "A", Description: "d",
		MimeType: "text/plain", Size: 12, ReadOnly: true,
	}
	if got[0] != want {
		t.Errorf("resource = %#v, want %#v", got[0], want)
	}
}

func TestReadMCPResourceMapping(t *testing.T) {
	fake := &fakeHarnessClient{contents: &mecatlv1.ReadMcpResourceResponse{
		Contents: []*mecatlv1.McpResourceContents{
			{Uri: "file://a", MimeType: "text/plain", Text: "hello"},
			{Uri: "file://b", MimeType: "application/octet-stream", Blob: []byte{1, 2, 3}},
		},
	}}
	cl := newFakeClient(fake)

	got, err := cl.ReadMCPResource(context.Background(), "srv", "file://a")
	if err != nil {
		t.Fatalf("ReadMCPResource: %v", err)
	}
	if fake.lastReadReq.GetUri() != "file://a" || fake.lastReadReq.GetServer() != "srv" {
		t.Errorf("request = %#v, want {srv, file://a}", fake.lastReadReq)
	}
	if len(got) != 2 || got[0].Text != "hello" || string(got[1].Blob) != "\x01\x02\x03" {
		t.Errorf("contents = %#v", got)
	}
}

func TestListMCPPromptsMapping(t *testing.T) {
	fake := &fakeHarnessClient{prompts: &mecatlv1.ListMcpPromptsResponse{
		Prompts: []*mecatlv1.McpPrompt{{
			Server: "srv", Name: "review", Title: "Review", Description: "d",
			Arguments: []*mecatlv1.McpPromptArgument{
				{Name: "path", Title: "Path", Description: "the file", Required: true},
				{Name: "depth", Required: false},
			},
		}},
	}}
	cl := newFakeClient(fake)

	got, err := cl.ListMCPPrompts(context.Background(), "")
	if err != nil {
		t.Fatalf("ListMCPPrompts: %v", err)
	}
	if len(got) != 1 || got[0].Name != "review" || len(got[0].Arguments) != 2 {
		t.Fatalf("prompts = %#v", got)
	}
	if !got[0].Arguments[0].Required || got[0].Arguments[1].Required {
		t.Errorf("required flags = %#v", got[0].Arguments)
	}
}

func TestGetMCPPromptMapping(t *testing.T) {
	fake := &fakeHarnessClient{prompt: &mecatlv1.GetMcpPromptResponse{
		Description: "rendered",
		Messages: []*mecatlv1.McpPromptMessage{
			{Role: "user", Text: "do the thing"},
		},
	}}
	cl := newFakeClient(fake)

	desc, msgs, err := cl.GetMCPPrompt(context.Background(), "srv", "review", map[string]string{"path": "x"})
	if err != nil {
		t.Fatalf("GetMCPPrompt: %v", err)
	}
	if fake.lastPromptReq.GetArguments()["path"] != "x" {
		t.Errorf("args = %#v, want {path:x}", fake.lastPromptReq.GetArguments())
	}
	if desc != "rendered" || len(msgs) != 1 || msgs[0].Role != "user" {
		t.Errorf("desc=%q msgs=%#v", desc, msgs)
	}
}

func TestListMCPSourcesMapping(t *testing.T) {
	fake := &fakeHarnessClient{sources: &mecatlv1.ListMcpSourcesResponse{
		Sources: []*mecatlv1.McpSource{{
			Name: "toolhive", Kind: "toolhive", Enabled: true, Group: "dev",
			Servers: []*mecatlv1.McpServerInfo{
				{Name: "fetch", Url: "http://x", Transport: "streamable-http", Group: "dev"},
			},
			Diagnostics: []string{"skipped sse server foo"},
		}},
	}}
	cl := newFakeClient(fake)

	got, err := cl.ListMCPSources(context.Background())
	if err != nil {
		t.Fatalf("ListMCPSources: %v", err)
	}
	if len(got) != 1 || got[0].Name != "toolhive" || !got[0].Enabled {
		t.Fatalf("sources = %#v", got)
	}
	if len(got[0].Servers) != 1 || got[0].Servers[0].Name != "fetch" {
		t.Errorf("servers = %#v", got[0].Servers)
	}
	if len(got[0].Diagnostics) != 1 {
		t.Errorf("diagnostics = %#v", got[0].Diagnostics)
	}
}

func TestListToolHiveGroupsMapping(t *testing.T) {
	fake := &fakeHarnessClient{groups: &mecatlv1.ListToolHiveGroupsResponse{Groups: []string{"dev", "prod"}}}
	cl := newFakeClient(fake)

	got, err := cl.ListToolHiveGroups(context.Background())
	if err != nil {
		t.Fatalf("ListToolHiveGroups: %v", err)
	}
	if len(got) != 2 || got[0] != "dev" {
		t.Errorf("groups = %#v", got)
	}
}

// TestClassifyMCPErr covers the gRPC-status → MCPErrorClass mapping per the
// Stage C contract: InvalidArgument → input, FailedPrecondition → not-configured,
// everything else → server.
func TestClassifyMCPErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want MCPErrorClass
	}{
		{"invalid-argument", status.Error(codes.InvalidArgument, "unknown server"), MCPErrInput},
		{"failed-precondition", status.Error(codes.FailedPrecondition, "no mcp"), MCPErrNotConfigured},
		{"internal", status.Error(codes.Internal, "boom"), MCPErrServer},
		{"unavailable", status.Error(codes.Unavailable, "down"), MCPErrServer},
		{"plain-error", errors.New("raw"), MCPErrServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyMCPErr(tc.err); got != tc.want {
				t.Errorf("classifyMCPErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestMCPCmdsClassifyErrors asserts each tea.Cmd surfaces a classified MCPErrMsg
// on RPC failure (the ui renders the three classes distinctly).
func TestMCPCmdsClassifyErrors(t *testing.T) {
	fake := &fakeHarnessClient{err: status.Error(codes.FailedPrecondition, "no mcp provider")}
	cl := newFakeClient(fake)
	ctx := context.Background()

	got := ListMcpSourcesCmd(ctx, cl)()
	em, ok := got.(MCPErrMsg)
	if !ok {
		t.Fatalf("ListMcpSourcesCmd msg = %T, want MCPErrMsg", got)
	}
	if em.Class != MCPErrNotConfigured {
		t.Errorf("class = %v, want NotConfigured", em.Class)
	}
	if em.Op != "list sources" {
		t.Errorf("op = %q, want 'list sources'", em.Op)
	}
}

// TestMCPCmdsSuccess asserts a representative success Cmd produces the success msg.
func TestMCPCmdsSuccess(t *testing.T) {
	fake := &fakeHarnessClient{sources: &mecatlv1.ListMcpSourcesResponse{
		Sources: []*mecatlv1.McpSource{{Name: "s", Kind: "static", Enabled: true}},
	}}
	cl := newFakeClient(fake)

	got := ListMcpSourcesCmd(context.Background(), cl)()
	sm, ok := got.(MCPSourcesMsg)
	if !ok {
		t.Fatalf("msg = %T, want MCPSourcesMsg", got)
	}
	if len(sm.Sources) != 1 || sm.Sources[0].Name != "s" {
		t.Errorf("sources = %#v", sm.Sources)
	}
}

func TestIsMCPAuthorizationPendingUsesStatusAndApplicationCode(t *testing.T) {
	pending, err := status.New(codes.FailedPrecondition, "unrelated wording").WithDetails(&errdetails.ErrorInfo{
		Reason: MCPAuthorizationPendingCode,
		Domain: "mecatl.stacklok.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !IsMCPAuthorizationPending(pending.Err()) {
		t.Fatal("matching status and application code was not recognized")
	}
	for _, err := range []error{
		status.Error(codes.FailedPrecondition, "mcp_authorization_pending"),
		status.Error(codes.Aborted, "mcp_authorization_pending"),
	} {
		if IsMCPAuthorizationPending(err) {
			t.Fatalf("untyped or wrong-status error classified as pending: %v", err)
		}
	}
}

func TestBrokerMCPStatus_Scenario3_Compatibility(t *testing.T) {
	cl := newFakeClient(&fakeHarnessClient{connectors: &mecatlv1.ListSessionMcpConnectorsResponse{
		Availability: "available", EnrollmentState: "completed", TotalConnectors: 1,
		Connectors: []*mecatlv1.McpConnectorStatus{{Name: "github", CatalogueState: "discovered", ToolCount: 2}},
	}})
	msg := ListMCPConnectorsCmd(context.Background(), cl, "session-1", 7, 3)()
	got, ok := msg.(MCPConnectorStatusMsg)
	if !ok {
		t.Fatalf("msg = %T, want MCPConnectorStatusMsg", msg)
	}
	if got.RequestToken != 7 || got.SessionID != "session-1" || got.Generation != 3 || len(got.Inventory.Connectors) != 1 || got.Inventory.Connectors[0].ToolCount != 2 {
		t.Fatalf("broker status = %#v", got)
	}

	errMsg := ListMCPConnectorsCmd(context.Background(), newFakeClient(&fakeHarnessClient{err: errors.New("broker unavailable")}), "session-2", 8, 4)()
	brokerErr, ok := errMsg.(MCPConnectorErrMsg)
	if !ok {
		t.Fatalf("error msg = %T, want MCPConnectorErrMsg", errMsg)
	}
	if brokerErr.RequestToken != 8 || brokerErr.SessionID != "session-2" || brokerErr.Generation != 4 {
		t.Fatalf("broker error lost correlation: %#v", brokerErr)
	}
}
