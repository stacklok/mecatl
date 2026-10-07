package mcpbrokergrpc

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/redis/go-redis/v9"
	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
)

type lifecycleService struct {
	ref        c.SessionRef
	cat        c.Catalogue
	deleted    bool
	beginCount int
}

func newLifecycleService(t *testing.T) *lifecycleService {
	t.Helper()
	ref := c.SessionRef(base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	catRef := c.CatalogueRef(base64.RawURLEncoding.EncodeToString(bytes32(1)))
	cat, err := c.NewCatalogue(catRef, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return &lifecycleService{ref: ref, cat: cat}
}

func bytes32(last byte) []byte {
	out := make([]byte, 32)
	out[len(out)-1] = last
	return out
}

func (s *lifecycleService) OpenSession(_ context.Context, saved *c.SessionRef) (c.SessionSnapshot, error) {
	if s.deleted || (saved != nil && *saved != s.ref) {
		return c.SessionSnapshot{}, c.ErrStateUnavailable
	}
	return c.SessionSnapshot{Ref: s.ref, ExpiresAt: time.Now().Add(time.Hour), Catalogue: s.cat}, nil
}

func (s *lifecycleService) InvokeTool(context.Context, c.SessionRef, c.CatalogueRef, c.Call, c.BrokerAttempt) (c.InvocationOutcome, error) {
	return c.InvocationOutcome{Kind: c.InvocationNotDispatched, Reason: c.FailureAuthorityWithdrawn}, nil
}

func (s *lifecycleService) BeginEnrollment(_ context.Context, ref c.SessionRef) (c.BeginEnrollmentOutcome, error) {
	if ref != s.ref {
		return c.BeginEnrollmentOutcome{}, c.ErrStateUnavailable
	}
	s.beginCount++
	if s.beginCount == 1 {
		return c.BeginEnrollmentOutcome{Kind: c.EnrollmentCompletedKind, Catalogue: s.cat}, nil
	}
	if s.beginCount == 3 {
		return c.BeginEnrollmentOutcome{Kind: c.EnrollmentStartedKind, Started: &c.EnrollmentStarted{
			Ref:    c.EnrollmentRef(base64.RawURLEncoding.EncodeToString(bytes32(2))),
			Prompt: c.BrowserPrompt{URL: "https://broker.test/authorize", ExpiresAt: time.Now().Add(time.Hour)},
		}}, nil
	}
	return c.BeginEnrollmentOutcome{Kind: c.EnrollmentAlreadyConnected}, nil
}

func (s *lifecycleService) DisconnectTools(_ context.Context, ref c.SessionRef, _ c.ConnectionRef) (c.DisconnectResult, error) {
	if ref != s.ref {
		return 0, c.ErrStateUnavailable
	}
	return c.AlreadyDisconnected, nil
}

func (s *lifecycleService) DeleteSession(_ context.Context, ref c.SessionRef) (c.DeleteResult, error) {
	if ref != s.ref {
		return 0, c.ErrStateUnavailable
	}
	if s.deleted {
		return c.AlreadyAbsent, nil
	}
	s.deleted = true
	return c.Deleted, nil
}

func TestSessionClientLifecycleAndInvocationDescriptor(t *testing.T) {
	service := newLifecycleService(t)
	rpc, err := NewSessionRPC(service)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	p.RegisterSessionServiceServer(server, rpc)
	listener := bufconn.Listen(1 << 20)
	t.Cleanup(func() { _ = listener.Close(); server.Stop() })
	go func() { _ = server.Serve(listener) }()

	client, err := NewSessionClient("passthrough:///session-v1", time.Second, time.Second,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	opened, err := client.OpenSession(t.Context(), nil)
	if err != nil || opened.Ref != service.ref || opened.Catalogue == nil || len(opened.Catalogue.Tools()) != 0 {
		t.Fatalf("open = %+v, %v", opened, err)
	}
	saved, err := client.OpenSession(t.Context(), &opened.Ref)
	if err != nil || saved.Ref != opened.Ref {
		t.Fatalf("saved open = %+v, %v", saved, err)
	}
	completed, err := client.BeginEnrollment(t.Context(), opened.Ref)
	if err != nil || completed.Kind != c.EnrollmentCompletedKind || completed.Catalogue == nil || len(completed.Catalogue.Tools()) != 0 {
		t.Fatalf("completed enrollment = %+v, %v", completed, err)
	}
	begun, err := client.BeginEnrollment(t.Context(), opened.Ref)
	if err != nil || begun.Kind != c.EnrollmentAlreadyConnected {
		t.Fatalf("begin enrollment = %+v, %v", begun, err)
	}
	if _, err := client.BeginEnrollment(t.Context(), opened.Ref); status.Code(err) != codes.Internal {
		t.Fatalf("V2 published protected started arm: %v", err)
	}
	connection := c.ConnectionRef(base64.RawURLEncoding.EncodeToString(bytes32(4)))
	disconnected, err := client.DisconnectTools(t.Context(), opened.Ref, connection)
	if err != nil || disconnected != c.AlreadyDisconnected {
		t.Fatalf("disconnect = %v, %v", disconnected, err)
	}
	empty := ""
	if _, err := rpc.OpenSession(t.Context(), &p.OpenSessionRequest{SavedRef: &empty}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("present-empty saved reference = %v; want InvalidArgument", err)
	}
	deleted, err := client.DeleteSession(t.Context(), opened.Ref)
	if err != nil || deleted != c.Deleted {
		t.Fatalf("delete = %v, %v", deleted, err)
	}
	deleted, err = client.DeleteSession(t.Context(), opened.Ref)
	if err != nil || deleted != c.AlreadyAbsent {
		t.Fatalf("repeat delete = %v, %v", deleted, err)
	}

	descriptorRef := c.CatalogueRef(base64.RawURLEncoding.EncodeToString(bytes32(2)))
	connectionRef := base64.RawURLEncoding.EncodeToString(bytes32(3))
	decoded, err := client.catalogue(service.ref, &p.Catalogue{
		Ref: string(descriptorRef), ConnectionRef: &connectionRef,
		Tools: []*p.ToolDescriptor{{Name: "write", Description: "write data", Schema: []byte(`{"type":"object"}`), ReadOnly: true, DispatchSerial: true, AuthorizationCapable: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	remote := decoded.Tools()[0]
	if remote.Spec().Description != "write data" || !remote.ReadOnly() {
		t.Fatalf("descriptor lost public metadata: %+v", remote.Spec())
	}
	if _, ok := remote.(tool.AuthorizationRequester); !ok {
		t.Fatal("authorization capability marker was lost")
	}
	if _, ok := remote.(tool.DispatchSerial); !ok {
		t.Fatal("serial dispatch marker was lost")
	}
	result, err := remote.Execute(tool.WithBrokerInvocation(t.Context(), session.NewBrokerAttempt()), session.ToolCall{ID: "call", Name: "write", Args: []byte(`{}`)}, tool.Environment{})
	if !errors.Is(err, errInvocationNotDispatched) || result.CallID != "" {
		t.Fatalf("V3 remote invocation did not preserve non-dispatch: result=%+v err=%v", result, err)
	}
	requester := remote.(tool.AuthorizationRequester)
	if authorization, ready, err := requester.RequestAuthorization(t.Context(), session.ToolCall{}); err == nil || ready || authorization.ID != "" {
		t.Fatalf("V4 scaffold claimed authorization readiness: %+v ready=%v err=%v", authorization, ready, err)
	}
	if err := requester.AbortAuthorization(t.Context(), session.ExternalAuthorization{}); err == nil {
		t.Fatal("V4 scaffold claimed successful authorization abort")
	}
	if !validSessionRef(string(service.ref)) || validSessionRef(string(service.ref)[:42]+"B") {
		t.Fatal("noncanonical reference accepted")
	}
}

func TestSessionProtoV3SchemaSnapshot(t *testing.T) {
	file := p.File_mecatl_broker_v1_session_proto
	service := file.Services().ByName("SessionService")
	if service == nil || service.Methods().Len() != 5 || service.Methods().ByName("InvokeTool") == nil || service.Methods().ByName("OpenSession") == nil || service.Methods().ByName("BeginEnrollment") == nil || service.Methods().ByName("DisconnectTools") == nil || service.Methods().ByName("DeleteSession") == nil {
		t.Fatalf("unexpected V2 service methods: %v", service)
	}
	fields := func(message protoreflect.Name, want map[protoreflect.Name]protoreflect.FieldNumber) {
		t.Helper()
		descriptor := file.Messages().ByName(message)
		if descriptor == nil || descriptor.Fields().Len() != len(want) {
			t.Fatalf("%s fields = %v", message, descriptor)
		}
		for name, number := range want {
			field := descriptor.Fields().ByName(name)
			if field == nil || field.Number() != number {
				t.Errorf("%s.%s = %v, want field %d", message, name, field, number)
			}
		}
	}
	fields("Attempt", map[protoreflect.Name]protoreflect.FieldNumber{"id": 3})
	if !file.Messages().ByName("Attempt").ReservedRanges().Has(1) || !file.Messages().ByName("Attempt").ReservedRanges().Has(2) {
		t.Fatal("former slot framing must remain reserved")
	}
	fields("Call", map[protoreflect.Name]protoreflect.FieldNumber{"id": 1, "name": 2, "arguments": 3})
	fields("InvokeToolRequest", map[protoreflect.Name]protoreflect.FieldNumber{"session_ref": 1, "catalogue_ref": 2, "call": 3, "attempt": 4})
	fields("InvocationOutcome", map[protoreflect.Name]protoreflect.FieldNumber{"completed": 1, "authorization_required": 2, "not_dispatched": 3, "outcome_unknown": 4})
	fields("OpenSessionRequest", map[protoreflect.Name]protoreflect.FieldNumber{"saved_ref": 1})
	fields("SessionSnapshot", map[protoreflect.Name]protoreflect.FieldNumber{"ref": 1, "expires_at": 2, "catalogue": 3})
	fields("Catalogue", map[protoreflect.Name]protoreflect.FieldNumber{"ref": 1, "tools": 2, "connection_ref": 3})
	fields("ToolDescriptor", map[protoreflect.Name]protoreflect.FieldNumber{"name": 1, "description": 2, "schema": 3, "read_only": 4, "dispatch_serial": 5, "authorization_capable": 6})
	fields("BeginEnrollmentRequest", map[protoreflect.Name]protoreflect.FieldNumber{"session_ref": 1})
	fields("BeginEnrollmentResponse", map[protoreflect.Name]protoreflect.FieldNumber{"already_connected": 2, "completed": 3})
	if !file.Messages().ByName("BeginEnrollmentResponse").ReservedRanges().Has(1) {
		t.Fatal("started arm field 1 must remain reserved for V5")
	}
	fields("DisconnectToolsRequest", map[protoreflect.Name]protoreflect.FieldNumber{"session_ref": 1, "expected_connection": 2})
	fields("DisconnectOutcome", map[protoreflect.Name]protoreflect.FieldNumber{"outcome": 1})
	fields("DeleteSessionRequest", map[protoreflect.Name]protoreflect.FieldNumber{"session_ref": 1})
	fields("DeleteOutcome", map[protoreflect.Name]protoreflect.FieldNumber{"outcome": 1})
	if !file.Messages().ByName("OpenSessionRequest").Fields().ByName("saved_ref").HasPresence() {
		t.Fatal("saved_ref must distinguish absent from present-empty")
	}
	values := file.Messages().ByName("DeleteOutcome").Enums().Get(0).Values()
	if values.Len() != 3 || values.Get(0).Name() != "UNSPECIFIED" || values.Get(0).Number() != 0 ||
		values.Get(1).Name() != "DELETED" || values.Get(1).Number() != 1 ||
		values.Get(2).Name() != "ALREADY_ABSENT" || values.Get(2).Number() != 2 {
		t.Fatalf("delete outcome enum values = %v", values)
	}
	values = file.Messages().ByName("DisconnectOutcome").Enums().Get(0).Values()
	if values.Len() != 4 || values.Get(0).Number() != 0 || values.Get(1).Number() != 1 || values.Get(2).Number() != 2 || values.Get(3).Number() != 3 {
		t.Fatalf("disconnect outcome enum values = %v", values)
	}
}

type v3Fixture struct {
	p.UnimplementedSessionServiceServer
	invoke func(context.Context, *p.InvokeToolRequest) (*p.InvocationOutcome, error)
}

func (f *v3Fixture) InvokeTool(ctx context.Context, request *p.InvokeToolRequest) (*p.InvocationOutcome, error) {
	return f.invoke(ctx, request)
}

func v3Client(t *testing.T, f *v3Fixture) *SessionClient {
	t.Helper()
	server := grpc.NewServer()
	p.RegisterSessionServiceServer(server, f)
	listener := bufconn.Listen(1 << 20)
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	go func() { _ = server.Serve(listener) }()
	client, err := NewSessionClient("passthrough:///session-v3", 100*time.Millisecond, 100*time.Millisecond,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithDefaultServiceConfig(`{"methodConfig":[{"name":[{"service":"mecatl.broker.v1.SessionService"}],"retryPolicy":{"MaxAttempts":4,"InitialBackoff":"0.001s","MaxBackoff":"0.001s","BackoffMultiplier":1,"RetryableStatusCodes":["UNAVAILABLE"]}}]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func v3Invoke(t *testing.T, client *SessionClient) (c.InvocationOutcome, error) {
	t.Helper()
	return client.InvokeTool(t.Context(), c.SessionRef(base64.RawURLEncoding.EncodeToString(bytes32(1))), c.CatalogueRef(base64.RawURLEncoding.EncodeToString(bytes32(2))), c.Call{ID: "one", Name: "echo", Arguments: []byte(`{}`)}, session.NewBrokerAttempt())
}

func TestSessionClientMalformedInvocationFailsClosed(t *testing.T) {
	unknown := &p.InvocationOutcome{Outcome: &p.InvocationOutcome_OutcomeUnknown{OutcomeUnknown: &emptypb.Empty{}}}
	unknown.ProtoReflect().SetUnknown([]byte{0x98, 0x06, 0x01})
	cases := map[string]*p.InvocationOutcome{
		"missing arm":         {},
		"bad auth ref":        {Outcome: &p.InvocationOutcome_AuthorizationRequired{AuthorizationRequired: "bad"}},
		"unknown reason":      {Outcome: &p.InvocationOutcome_NotDispatched{NotDispatched: &p.NonDispatch{Reason: 257}}},
		"wrong call":          {Outcome: &p.InvocationOutcome_Completed{Completed: &p.ToolResult{CallId: "other"}}},
		"invalid result kind": {Outcome: &p.InvocationOutcome_Completed{Completed: &p.ToolResult{CallId: "one", Parts: []*p.ResultPart{{BlockKind: "made_up", Text: "bad"}}}}},
		"oversized result":    {Outcome: &p.InvocationOutcome_Completed{Completed: &p.ToolResult{CallId: "one", Content: string(make([]byte, 256*1024))}}},
		"unknown wire fields": unknown,
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			client := v3Client(t, &v3Fixture{invoke: func(context.Context, *p.InvokeToolRequest) (*p.InvocationOutcome, error) {
				calls.Add(1)
				return response, nil
			}})
			out, err := v3Invoke(t, client)
			if err == nil || out.Kind != c.InvocationOutcomeUnknown || calls.Load() != 1 {
				t.Fatalf("out=%#v err=%v calls=%d", out, err, calls.Load())
			}
		})
	}
}

func TestSessionClientNoRetry(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		var calls atomic.Int32
		client := v3Client(t, &v3Fixture{invoke: func(ctx context.Context, _ *p.InvokeToolRequest) (*p.InvocationOutcome, error) {
			calls.Add(1)
			if timeout {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return nil, status.Error(codes.Unavailable, "lost return")
		}})
		out, err := v3Invoke(t, client)
		if err == nil || out.Kind != c.InvocationOutcomeUnknown || calls.Load() != 1 {
			t.Fatalf("out=%#v err=%v calls=%d", out, err, calls.Load())
		}
	}
}

func TestSessionClientValidToolErrorIsNotFabricatedSuccess(t *testing.T) {
	client := v3Client(t, &v3Fixture{invoke: func(_ context.Context, request *p.InvokeToolRequest) (*p.InvocationOutcome, error) {
		return &p.InvocationOutcome{Outcome: &p.InvocationOutcome_Completed{Completed: &p.ToolResult{CallId: request.Call.Id, IsError: true, Content: "upstream error"}}}, nil
	}})
	out, err := v3Invoke(t, client)
	if err != nil || out.Kind != c.InvocationCompleted || !out.Result.IsError {
		t.Fatalf("error result lost: %#v %v", out, err)
	}
}

func TestSessionRemoteToolV3VerifiedNonDispatch(t *testing.T) {
	client := v3Client(t, &v3Fixture{invoke: func(context.Context, *p.InvokeToolRequest) (*p.InvocationOutcome, error) {
		return &p.InvocationOutcome{Outcome: &p.InvocationOutcome_AuthorizationRequired{AuthorizationRequired: base64.RawURLEncoding.EncodeToString(bytes32(4))}}, nil
	}})
	remote := &sessionRemoteTool{client: client, ref: c.SessionRef(base64.RawURLEncoding.EncodeToString(bytes32(1))), catalogue: c.CatalogueRef(base64.RawURLEncoding.EncodeToString(bytes32(2))), spec: tool.ToolSpec{Name: "echo"}}
	_, err := remote.Execute(tool.WithBrokerInvocation(t.Context(), session.NewBrokerAttempt()), session.ToolCall{ID: "one", Name: "echo", Args: []byte(`{}`)}, tool.Environment{})
	if !errors.Is(err, errInvocationNotDispatched) || remote.BrokerInvocationDisposition(err) != session.BrokerAttemptNotDispatched {
		t.Fatalf("authorization required must remain verified non-dispatch: %v", err)
	}
}

func TestSessionRPCNativeAnonymous(t *testing.T) {
	var calls atomic.Int32
	upstream := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "rpc-v3", Version: "test"}, nil)
	upstream.AddTool(&mcpsdk.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		calls.Add(1)
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "native-rpc"}}}, nil
	})
	httpServer := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return upstream }, nil))
	t.Cleanup(httpServer.Close)
	process, err := mcpbroker.NewToolHiveProcess(t.Context(), mcpbroker.ToolHiveConfig{DeferAnonymousDiscovery: true, Profiles: []mcpbroker.ToolHiveProfile{{Name: "echo", URL: httpServer.URL, Auth: "none"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })
	db := miniredis.RunT(t)
	storage := redis.NewClient(&redis.Options{MaxRetries: -1, Addr: db.Addr()})
	t.Cleanup(func() { _ = storage.Close() })
	api, err := mcpbroker.NewSessionAPI(process, storage, func(context.Context) *session.Principal {
		return &session.Principal{Issuer: "https://verified-workload.test", Subject: "client"}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.Close() })
	rpc, err := NewSessionRPC(api)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, request any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(session.WithPrincipal(ctx, &session.Principal{Issuer: "https://verified-owner.test", Subject: "owner"}), request)
	}))
	p.RegisterSessionServiceServer(server, rpc)
	listener := bufconn.Listen(1 << 20)
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	go func() { _ = server.Serve(listener) }()
	client, err := NewSessionClient("passthrough:///session-native-v3", time.Second, time.Second,
		grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	opened, err := client.OpenSession(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := client.BeginEnrollment(t.Context(), opened.Ref)
	if err != nil || enrolled.Kind != c.EnrollmentCompletedKind || len(enrolled.Catalogue.Tools()) != 1 {
		t.Fatalf("enrollment: %#v %v", enrolled, err)
	}
	call := session.ToolCall{ID: "one", Name: enrolled.Catalogue.Tools()[0].Spec().Name, Args: []byte(`{}`)}
	remote := enrolled.Catalogue.Tools()[0]
	result, err := remote.Execute(tool.WithBrokerInvocation(t.Context(), session.NewBrokerAttempt()), call, tool.Environment{})
	if err != nil || result.CallID != call.ID || result.Content != "native-rpc" || calls.Load() != 1 {
		t.Fatalf("native invocation: %#v %v calls=%d", result, err, calls.Load())
	}
	invalid := &p.InvokeToolRequest{SessionRef: string(opened.Ref), CatalogueRef: string(enrolled.Catalogue.Ref()), Call: &p.Call{Id: "one", Name: call.Name, Arguments: []byte(`{}`)}, Attempt: &p.Attempt{Id: session.NewBrokerAttempt().ID}}
	invalid.Attempt.ProtoReflect().SetUnknown([]byte{0x08, 0x01})
	if _, err := rpc.InvokeTool(t.Context(), invalid); status.Code(err) != codes.InvalidArgument || calls.Load() != 1 {
		t.Fatalf("legacy slot dispatched: %v calls=%d", err, calls.Load())
	}
}
