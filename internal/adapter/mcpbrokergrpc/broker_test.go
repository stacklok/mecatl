package mcpbrokergrpc_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os/exec"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
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
	conditional, ok := any(remote).(mcpbroker.BindingSessionDeleter)
	if !ok {
		t.Fatal("remote client does not support binding deletion")
	}
	if got, err := conditional.DeleteSessionIfBinding(ctx, "session-1", attachment.Binding()); err != nil || got != mcpbroker.DeleteDeleted {
		t.Fatalf("DeleteSessionIfBinding = %q, %v", got, err)
	}
	if got, err := conditional.DeleteSessionIfBinding(ctx, "session-1", attachment.Binding()); err != nil || got != mcpbroker.DeleteNotFound {
		t.Fatalf("second DeleteSessionIfBinding = %q, %v", got, err)
	}
}

func TestRemoteDeleteRejectsEmptyBinding(t *testing.T) {
	remote := newRemote(t, newBroker())
	if _, err := remote.DeleteSession(t.Context(), "session-1"); err == nil || !strings.Contains(err.Error(), "requires a binding") {
		t.Fatalf("DeleteSession without binding = %v, want binding rejection", err)
	}
}

func TestSingletonBrokerRemediation_Scenario1_HostilePeerBoundary(t *testing.T) {
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

	exact := newRemote(t, byteExactBroker{})
	exactAttachment, _, err := exact.AttachSession(t.Context(), "byte-exact")
	if err != nil {
		t.Fatal(err)
	}
	exactSchema := []byte("{\n  \"type\": \"object\"\n}")
	if !bytes.Equal(exactAttachment.Tools()[0].Spec().Schema, exactSchema) {
		t.Fatalf("schema changed: %q", exactAttachment.Tools()[0].Spec().Schema)
	}
	exactArgs := []byte(`{"text":"é","n":1}`)
	result, err = exactAttachment.Tools()[0].Execute(t.Context(), session.NewToolCall("exact-call", "exact", exactArgs), tool.Environment{})
	if err != nil || result.Content != string(exactArgs) || len(result.Parts) != 2 || result.Parts[0].Text != "é" || result.Parts[1].MIMEType != "application/x-exact" || !bytes.Equal(result.Parts[1].Data, []byte{0, 0xff, 1}) {
		t.Fatalf("byte-exact round trip = %#v, %v", result, err)
	}

	for _, peer := range []struct {
		name string
		conn grpc.ClientConnInterface
	}{
		{name: "malformed schema", conn: malformedAttachConn{}},
		{name: "unknown attach outcome", conn: hostilePeerConn{attachOutcome: "future"}},
		{name: "invalid UTF-8 descriptor", conn: hostilePeerConn{descriptorName: string([]byte{0xff})}},
	} {
		t.Run(peer.name, func(t *testing.T) {
			if _, _, err := mcpbrokergrpc.NewClient(peer.conn).AttachSession(t.Context(), "hostile"); err == nil {
				t.Fatal("hostile attach response was accepted")
			}
		})
	}
	for _, response := range []*brokerv1.ToolResult{
		{CallId: "hostile-call", Content: string([]byte{0xff})},
		{CallId: "hostile-call", Parts: []*brokerv1.ResultPart{{BlockKind: "future"}}},
	} {
		client := mcpbrokergrpc.NewClient(hostilePeerConn{result: response})
		peerAttachment, _, attachErr := client.AttachSession(t.Context(), "hostile-result")
		if attachErr != nil {
			t.Fatal(attachErr)
		}
		if _, executeErr := peerAttachment.Tools()[0].Execute(t.Context(), session.NewToolCall("hostile-call", "exact", []byte(`{}`)), tool.Environment{}); executeErr == nil {
			t.Fatalf("hostile result %#v was accepted", response)
		}
	}
}

type byteExactBroker struct{}

func (byteExactBroker) AttachSession(context.Context, session.SessionID) (mcpbroker.SessionHandle, mcpbroker.AttachOutcome, error) {
	return &byteExactAttachment{}, mcpbroker.AttachCreated, nil
}
func (byteExactBroker) DeleteSession(context.Context, session.SessionID) (mcpbroker.DeleteOutcome, error) {
	return mcpbroker.DeleteNotFound, nil
}

type byteExactAttachment struct{ attachment }

func (byteExactAttachment) Tools() []tool.Tool { return []tool.Tool{byteExactTool{}} }

type byteExactTool struct{}

func (byteExactTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "exact", Description: "é", Schema: []byte("{\n  \"type\": \"object\"\n}")}
}
func (byteExactTool) ReadOnly() bool { return true }
func (byteExactTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResultWithParts(call.ID, string(call.Args), []session.Content{
		session.NewTextBlock("é"),
		{BlockKind: session.BlockEmbeddedResource, MIMEType: "application/x-exact", Data: []byte{0, 0xff, 1}},
	}), nil
}

type hostilePeerConn struct {
	attachOutcome  string
	descriptorName string
	result         *brokerv1.ToolResult
}

func (c hostilePeerConn) Invoke(_ context.Context, method string, _, reply any, _ ...grpc.CallOption) error {
	switch {
	case strings.HasSuffix(method, "/Attach"):
		outcome := c.attachOutcome
		if outcome == "" {
			outcome = string(mcpbroker.AttachCreated)
		}
		name := c.descriptorName
		if name == "" {
			name = "exact"
		}
		*reply.(*brokerv1.AttachResponse) = brokerv1.AttachResponse{Binding: "binding", Handle: "handle", Outcome: outcome, BrokerIncarnation: "incarnation", Tools: []*brokerv1.ToolDescriptor{{Name: name, Description: "exact", Schema: []byte(`{"type":"object"}`)}}}
	case strings.HasSuffix(method, "/Execute"):
		*reply.(*brokerv1.ExecuteResponse) = brokerv1.ExecuteResponse{Result: c.result}
	default:
		return errors.New("unexpected method")
	}
	return nil
}
func (hostilePeerConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("unexpected stream")
}

func TestInitialProductionMCPBroker_RejectsNonObjectToolSchema(t *testing.T) {
	t.Run("server", func(t *testing.T) {
		broker := &invalidDescriptorBroker{}
		remote := newRemote(t, broker)
		if _, _, err := remote.AttachSession(t.Context(), "invalid-schema"); err == nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("AttachSession with boolean schema = %v, want invalid descriptor rejection", err)
		}
		if broker.attachment == nil || !broker.attachment.aborted {
			t.Fatal("created logical attachment was not aborted after descriptor rejection")
		}
	})
	t.Run("client", func(t *testing.T) {
		aborts := 0
		client := mcpbrokergrpc.NewClient(malformedAttachConn{aborts: &aborts})
		if _, _, err := client.AttachSession(t.Context(), "invalid-schema"); err == nil || !strings.Contains(err.Error(), "malformed tool descriptor") {
			t.Fatalf("AttachSession with peer boolean schema = %v, want client descriptor rejection", err)
		}
		if aborts != 1 {
			t.Fatalf("malformed created attachment aborts = %d, want 1", aborts)
		}
	})
}

func TestInvariant_initial_broker_protocol_is_neutral_and_secret_free(t *testing.T) {
	wire := strings.ToLower(protodesc.ToFileDescriptorProto(brokerv1.File_mecatl_broker_v1_broker_proto).String())
	for _, token := range []string{"toolcall", "oauth", "verifier", "access_token", "refresh_token", "client_secret", "toolhive", "redis", "fence"} {
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

type malformedAttachConn struct{ aborts *int }

func (c malformedAttachConn) Invoke(_ context.Context, method string, _, reply any, _ ...grpc.CallOption) error {
	if strings.HasSuffix(method, "/Abort") {
		if c.aborts != nil {
			*c.aborts++
		}
		*reply.(*brokerv1.AbortResponse) = brokerv1.AbortResponse{}
		return nil
	}
	response := reply.(*brokerv1.AttachResponse)
	*response = brokerv1.AttachResponse{
		Handle:            "handle",
		Binding:           "binding",
		BrokerIncarnation: "incarnation",
		Outcome:           string(mcpbroker.AttachCreated),
		Tools: []*brokerv1.ToolDescriptor{{
			Name: "invalid", Description: "invalid", Schema: []byte(`true`),
		}},
	}
	return nil
}
func (malformedAttachConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("unexpected stream")
}

type invalidDescriptorBroker struct{ attachment *invalidDescriptorAttachment }

func (b *invalidDescriptorBroker) AttachSession(context.Context, session.SessionID) (mcpbroker.SessionHandle, mcpbroker.AttachOutcome, error) {
	b.attachment = &invalidDescriptorAttachment{}
	return b.attachment, mcpbroker.AttachCreated, nil
}
func (*invalidDescriptorBroker) DeleteSession(context.Context, session.SessionID) (mcpbroker.DeleteOutcome, error) {
	return mcpbroker.DeleteNotFound, nil
}

type invalidDescriptorAttachment struct {
	attachment
	aborted bool
}

func (a *invalidDescriptorAttachment) Abort(ctx context.Context) error {
	a.aborted = true
	return a.attachment.Abort(ctx)
}

func (*invalidDescriptorAttachment) Tools() []tool.Tool { return []tool.Tool{invalidSchemaTool{}} }

type invalidSchemaTool struct{ serialTool }

func (invalidSchemaTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "invalid", Description: "invalid", Schema: []byte(`true`)}
}

type broker struct{ exists bool }

func newBroker() *broker { return &broker{} }
func (b *broker) AttachSession(context.Context, session.SessionID) (mcpbroker.SessionHandle, mcpbroker.AttachOutcome, error) {
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

func (b *broker) DeleteSessionIfBinding(ctx context.Context, _ session.SessionID, binding session.ExternalBinding) (mcpbroker.DeleteOutcome, error) {
	if binding != "binding-1" {
		return mcpbroker.DeleteNotFound, nil
	}
	return b.DeleteSession(ctx, "")
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
	brokerServer, err := mcpbrokergrpc.NewServer(local, mcpbrokergrpc.DefaultConfig())
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
