package mcpbrokergrpc

import (
	"context"
	"encoding/base64"
	"net"
	"testing"
	"time"

	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/reflect/protoreflect"
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

func TestSessionClientLifecycleAndFailClosedDescriptorScaffold(t *testing.T) {
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
	result, err := remote.Execute(t.Context(), session.ToolCall{ID: "call", Name: "write", Args: []byte(`{}`)}, tool.Environment{})
	if err == nil || result.CallID != "" {
		t.Fatalf("V3 scaffold executed remotely: result=%+v err=%v", result, err)
	}
	requester := remote.(tool.AuthorizationRequester)
	if authorization, ready, err := requester.RequestAuthorization(t.Context(), session.ToolCall{}); err == nil || ready || authorization.ID != "" {
		t.Fatalf("V4 scaffold claimed authorization readiness: %+v ready=%v err=%v", authorization, ready, err)
	}
	if err := requester.AbortAuthorization(t.Context(), session.ExternalAuthorization{}); err == nil {
		t.Fatal("V4 scaffold claimed successful authorization abort")
	}
}

func TestSessionProtoV2SchemaSnapshot(t *testing.T) {
	file := p.File_mecatl_broker_v1_session_proto
	service := file.Services().ByName("SessionService")
	if service == nil || service.Methods().Len() != 4 || service.Methods().ByName("OpenSession") == nil || service.Methods().ByName("BeginEnrollment") == nil || service.Methods().ByName("DisconnectTools") == nil || service.Methods().ByName("DeleteSession") == nil {
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
