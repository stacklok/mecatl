package mcpbrokergrpc_test

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/reflect/protodesc"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestInitialProductionMCPBroker_Scenario1_AttachmentParity(t *testing.T) {
	local := newBroker()
	remote := newRemote(t, local)
	attachment, outcome, err := remote.AttachSession(context.Background(), "session-1")
	if err != nil || outcome != mcpbroker.AttachCreated {
		t.Fatalf("AttachSession() = (%q, %v), want created", outcome, err)
	}
	if attachment.Binding() != "binding-1" {
		t.Fatalf("Binding() = %q, want binding-1", attachment.Binding())
	}
	tools := attachment.Tools()
	if len(tools) != 2 || tools[0].Spec().Name != "read" || tools[1].Spec().Name != "protected" {
		t.Fatalf("Tools() = %#v, want frozen complete catalogue", tools)
	}
	if !tools[0].ReadOnly() {
		t.Error("read-only descriptor was lost")
	}
	if _, ok := tools[0].(tool.DispatchSerial); !ok {
		t.Error("dispatch-serial descriptor was lost")
	}
	if _, ok := tools[1].(tool.AuthorizationRequester); !ok {
		t.Error("authorization capability was lost")
	}
}

func TestInitialProductionMCPBroker_Scenario1_LifecycleOutcomes(t *testing.T) {
	local := newBroker()
	remote := newRemote(t, local)
	ctx := context.Background()
	attachment, _, err := remote.AttachSession(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := attachment.Commit(ctx); err != nil {
		t.Fatalf("first Commit: %v", err)
	}
	if err := attachment.Commit(ctx); err != nil {
		t.Fatalf("second Commit: %v", err)
	}
	if got, err := attachment.Close(ctx); err != nil || got != mcpbroker.CloseClosed {
		t.Fatalf("Close = %q, %v", got, err)
	}
	if got, err := attachment.Close(ctx); err != nil || got != mcpbroker.CloseAlreadyClosed {
		t.Fatalf("second Close = %q, %v", got, err)
	}
	if _, outcome, err := remote.AttachSession(ctx, "session-1"); err != nil || outcome != mcpbroker.AttachReattached {
		t.Fatalf("reattach after Close = %q, %v", outcome, err)
	}
	if got, err := remote.DeleteSession(ctx, "session-1"); err != nil || got != mcpbroker.DeleteDeleted {
		t.Fatalf("DeleteSession = %q, %v", got, err)
	}
	if got, err := remote.DeleteSession(ctx, "session-1"); err != nil || got != mcpbroker.DeleteNotFound {
		t.Fatalf("second DeleteSession = %q, %v", got, err)
	}
}

func TestInitialProductionMCPBroker_Scenario1_ToolRoundTrip(t *testing.T) {
	remote := newRemote(t, newBroker())
	attachment, _, err := remote.AttachSession(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("call-1", "read", []byte(`{"nested":{"value":"preserve"}}`))
	result, err := attachment.Tools()[0].Execute(context.Background(), call, tool.Environment{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.CallID != call.ID || result.Content != "summary" || len(result.Parts) != 2 || result.Parts[0].Text != "text" || string(result.Parts[1].Data) != "\x00\xff" {
		t.Fatalf("ToolResult = %#v, want complete lossless result", result)
	}
	if _, err := attachment.Tools()[0].Execute(context.Background(), session.NewToolCall("call-2", "read", []byte("{")), tool.Environment{}); err == nil {
		t.Fatal("malformed arguments were accepted")
	}
}

func TestInvariant_initial_broker_protocol_is_neutral_and_secret_free(t *testing.T) {
	wire := strings.ToLower(protodesc.ToFileDescriptorProto(brokerv1.File_mecatl_broker_v1_broker_proto).String())
	for _, token := range []string{"toolcall", "oauth", "verifier", "access_token", "refresh_token", "client_secret", "toolhive", "redis", "generation", "fence", "owner"} {
		if strings.Contains(wire, token) {
			t.Fatalf("broker descriptor contains forbidden %q", token)
		}
	}
}

func TestInvariant_initial_broker_preserves_engine_layering(t *testing.T) {
	output := goListDeps(t, "github.com/stacklok/mecatl/engine/...")
	for _, forbidden := range []string{"google.golang.org/grpc", "google.golang.org/protobuf", "toolhive", "mcpbrokergrpc"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("engine dependency graph contains %q", forbidden)
		}
	}
}

func TestInvariant_initial_broker_excludes_distributed_donor_contract(t *testing.T) {
	output := goListDeps(t, "github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc")
	for _, forbidden := range []string{"/internal/broker", "redis", "toolhive"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("remote broker dependency graph contains forbidden %q", forbidden)
		}
	}
}

func goListDeps(t *testing.T, pkg string) string {
	t.Helper()
	output, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", pkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go list %s: %v\n%s", pkg, err, output)
	}
	return string(output)
}

type broker struct{ exists bool }

func newBroker() *broker { return &broker{} }
func (b *broker) AttachSession(context.Context, session.SessionID) (mcpbroker.Attachment, mcpbroker.AttachOutcome, error) {
	if !b.exists {
		b.exists = true
		return &attachment{}, mcpbroker.AttachCreated, nil
	}
	return &attachment{}, mcpbroker.AttachReattached, nil
}
func (b *broker) DeleteSession(context.Context, session.SessionID) (mcpbroker.DeleteOutcome, error) {
	if !b.exists {
		return mcpbroker.DeleteNotFound, nil
	}
	b.exists = false
	return mcpbroker.DeleteDeleted, nil
}

type attachment struct{ closed bool }

func (*attachment) Binding() session.ExternalBinding { return "binding-1" }
func (a *attachment) Commit(context.Context) error {
	if a.closed {
		return mcpbroker.ErrAttachmentClosed
	}
	return nil
}
func (a *attachment) Abort(context.Context) error { a.closed = true; return nil }
func (a *attachment) Close(context.Context) (mcpbroker.CloseOutcome, error) {
	if a.closed {
		return mcpbroker.CloseAlreadyClosed, nil
	}
	a.closed = true
	return mcpbroker.CloseClosed, nil
}
func (*attachment) Tools() []tool.Tool { return []tool.Tool{serialTool{}, protectedTool{}} }
func (*attachment) PresentAuthorization(context.Context, session.ExternalAuthorization) (string, error) {
	return "", errors.New("unused")
}
func (*attachment) AuthorizationStatus(context.Context, session.ExternalAuthorization) (session.AuthorizationStatus, error) {
	return "", errors.New("unused")
}
func (*attachment) CancelAuthorization(context.Context, session.ExternalAuthorization) (mcpbroker.CancelOutcome, error) {
	return "", errors.New("unused")
}

type serialTool struct{}

func (serialTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "read", Description: "read", Schema: []byte(`{"type":"object"}`)}
}
func (serialTool) ReadOnly() bool      { return true }
func (serialTool) DispatchSerialTool() {}
func (serialTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	if !jsonValid(call.Args) {
		return session.ToolResult{}, errors.New("invalid arguments")
	}
	return session.NewToolResultWithParts(call.ID, "summary", []session.Content{session.NewTextBlock("text"), {BlockKind: session.BlockEmbeddedResource, MIMEType: "application/octet-stream", Data: []byte{0, 255}}}), nil
}

type protectedTool struct{ serialTool }

func (protectedTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "protected", Description: "protected", Schema: []byte(`{"type":"object"}`)}
}
func (protectedTool) RequestAuthorization(context.Context, session.ToolCall) (session.ExternalAuthorization, bool, error) {
	return session.ExternalAuthorization{}, false, nil
}
func (protectedTool) AbortAuthorization(context.Context, session.ExternalAuthorization) error {
	return nil
}
func jsonValid(b []byte) bool { return len(b) > 0 && b[0] == '{' && b[len(b)-1] == '}' }
func newRemote(t *testing.T, local mcpbroker.Service) mcpbroker.Service {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	brokerServer, err := mcpbrokergrpc.NewServer(local, 0)
	if err != nil {
		t.Fatal(err)
	}
	mcpbrokergrpc.RegisterServer(server, brokerServer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = brokerServer.Shutdown(context.Background()); server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///broker", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return mcpbrokergrpc.NewClient(conn)
}
