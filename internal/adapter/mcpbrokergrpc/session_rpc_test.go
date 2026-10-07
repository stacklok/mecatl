package mcpbrokergrpc_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/redis/go-redis/v9"
	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestSessionRPCNativeAnonymous(t *testing.T) {
	var calls, requests atomic.Int32
	upstream := sdk.NewServer(&sdk.Implementation{Name: "rpc-poc", Version: "test"}, nil)
	sdk.AddTool(upstream, &sdk.Tool{Name: "echo"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		calls.Add(1)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "native-rpc"}}}, nil, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return upstream }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); handler.ServeHTTP(w, r) }))
	defer httpServer.Close()
	process, err := mcpbroker.NewToolHiveProcess(t.Context(), mcpbroker.ToolHiveConfig{DeferAnonymousDiscovery: true, Profiles: []mcpbroker.ToolHiveProfile{{Name: "echo", URL: httpServer.URL, Auth: "none"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	db := miniredis.RunT(t)
	storage := redis.NewClient(&redis.Options{Addr: db.Addr()})
	defer storage.Close()
	api, err := mcpbroker.NewSessionAPI(process, storage, func(context.Context) *session.Principal {
		return &session.Principal{Issuer: "https://verified-workload.test", Subject: "client"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	rpc, err := mcpbrokergrpc.NewSessionRPC(api)
	if err != nil {
		t.Fatal(err)
	}
	// Offline fixture authentication, matching the donor continuity fixture.
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, r any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		return h(session.WithPrincipal(ctx, &session.Principal{Issuer: "https://verified-owner.test", Subject: "owner"}), r)
	}))
	p.RegisterSessionServiceServer(server, rpc)
	listener := bufconn.Listen(1 << 20)
	defer listener.Close()
	defer server.Stop()
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///session-poc", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := p.NewSessionServiceClient(conn)
	empty := ""
	if _, err = client.OpenSession(t.Context(), &p.OpenSessionRequest{SavedRef: &empty}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("present-empty: %v", err)
	}
	opened, err := client.OpenSession(t.Context(), &p.OpenSessionRequest{})
	if err != nil || len(opened.GetCatalogue().GetTools()) != 0 {
		t.Fatalf("open: %v %v", opened, err)
	}
	enrolled, err := client.BeginEnrollment(t.Context(), &p.BeginEnrollmentRequest{SessionRef: opened.Ref})
	if err != nil || enrolled.GetCompleted() == nil {
		t.Fatalf("anonymous completed: %v %v", enrolled, err)
	}
	cat := enrolled.GetCompleted()
	remote, err := mcpbrokergrpc.NewSessionClient("passthrough:///session-poc", time.Second, time.Second, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	saved := c.SessionRef(opened.Ref)
	snapshot, err := remote.OpenSession(t.Context(), &saved)
	if err != nil || snapshot.Ref != saved {
		t.Fatalf("remote open: %v", err)
	}
	enrollment, err := remote.BeginEnrollment(t.Context(), saved)
	if err != nil || enrollment.Kind != c.EnrollmentAlreadyConnected {
		t.Fatalf("remote enrollment: %v", err)
	}
	call := c.Call{ID: "one", Name: cat.Tools[0].Name, Arguments: []byte(`{}`)}
	beforeCheck := requests.Load()
	inventory, err := remote.InspectConnectors(t.Context(), saved, snapshot.Catalogue.Ref())
	if err != nil || inventory.Availability != c.AvailabilityAvailable || inventory.EnrollmentState != c.EnrollmentNotRequired || inventory.Connectors[0].ToolCount != 1 || requests.Load() != beforeCheck {
		t.Fatalf("remote passive inspection: %#v %v", inventory, err)
	}
	check, err := remote.CheckAuthorization(t.Context(), saved, snapshot.Catalogue.Ref(), &call, "", c.BrokerAttempt{Sequence: 1})
	if err != nil || !check.Ready || calls.Load() != 0 || requests.Load() != beforeCheck {
		t.Fatalf("preflight dispatched: %#v %v %d", check, err, calls.Load())
	}
	result, err := snapshot.Catalogue.Tools()[0].Execute(tool.WithBrokerInvocation(t.Context(), c.BrokerAttempt{Sequence: 1}), session.ToolCall{ID: call.ID, Name: call.Name, Args: call.Arguments}, tool.Environment{})
	if err != nil || result.Content != "native-rpc" || calls.Load() != 1 {
		t.Fatalf("wrapper invoke: %#v %v %d", result, err, calls.Load())
	}
	out, err := client.InvokeTool(t.Context(), &p.InvokeToolRequest{Attempt: &p.Attempt{Sequence: 1}, SessionRef: opened.Ref, CatalogueRef: cat.Ref, Call: &p.Call{Id: "one", Name: cat.Tools[0].Name, Arguments: []byte(`{}`)}})
	if err != nil || out.GetCompleted().GetContent() != "native-rpc" {
		t.Fatalf("invoke: %v %v", out, err)
	}
	// Unknown process-local refs are interrupted, never reconstructed or executed.
	fake := opened.Ref
	flow, err := client.ObserveAuthorization(t.Context(), &p.ObserveAuthorizationRequest{SessionRef: opened.Ref, AuthorizationRef: fake})
	if err != nil || flow.GetFailed() != p.FailureReason_FAILURE_REASON_INTERRUPTED {
		t.Fatalf("observe auth: %v %v", flow, err)
	}
	if _, err = client.BeginAuthorization(t.Context(), &p.BeginAuthorizationRequest{SessionRef: opened.Ref, AuthorizationRef: fake}); status.Code(err) != codes.NotFound {
		t.Fatalf("begin unknown auth: %v", err)
	}
	cancelled, err := client.CancelAuthorization(t.Context(), &p.CancelAuthorizationRequest{Attempt: &p.Attempt{Sequence: 1}, SessionRef: opened.Ref, AuthorizationRef: fake})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("cancel auth: %v %v", cancelled, err)
	}
	out, err = client.ResumeTool(t.Context(), &p.ResumeToolRequest{Attempt: &p.Attempt{Sequence: 1}, SessionRef: opened.Ref, AuthorizationRef: fake, AdoptedCatalogue: cat.Ref})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("resume unknown: %v %v", out, err)
	}
	flow, err = client.ObserveEnrollment(t.Context(), &p.ObserveEnrollmentRequest{SessionRef: opened.Ref, EnrollmentRef: fake})
	if err != nil || flow.GetFailed() != p.FailureReason_FAILURE_REASON_INTERRUPTED {
		t.Fatalf("observe enrollment: %v %v", flow, err)
	}
	cancelled, err = client.CancelEnrollment(t.Context(), &p.CancelEnrollmentRequest{SessionRef: opened.Ref, EnrollmentRef: fake})
	if err != nil || cancelled.Outcome != p.CancelOutcome_ALREADY_RESOLVED {
		t.Fatalf("cancel enrollment: %v %v", cancelled, err)
	}
	disconnected, err := client.DisconnectTools(t.Context(), &p.DisconnectToolsRequest{SessionRef: opened.Ref, ExpectedConnection: cat.GetConnectionRef()})
	if err != nil || disconnected.Outcome != p.DisconnectOutcome_DISCONNECTED {
		t.Fatalf("disconnect: %v %v", disconnected, err)
	}
	deleted, err := client.DeleteSession(t.Context(), &p.DeleteSessionRequest{SessionRef: opened.Ref})
	if err != nil || deleted.Outcome != p.DeleteOutcome_DELETED {
		t.Fatalf("delete: %v %v", deleted, err)
	}
	if _, err = client.OpenSession(t.Context(), &p.OpenSessionRequest{SavedRef: &opened.Ref}); err == nil {
		t.Fatal("deleted reopen created a new session")
	}
}
