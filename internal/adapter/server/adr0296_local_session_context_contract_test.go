package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func TestADR_0296_PrivilegedProtoSurfaceIsNarrow(t *testing.T) {
	services := mecatlv1.File_mecatl_v1_local_session_context_proto.Services()
	if services.Len() != 1 {
		t.Fatalf("privileged service count = %d, want 1", services.Len())
	}
	service := services.Get(0)
	if got, want := string(service.Name()), "LocalSessionContextService"; got != want {
		t.Fatalf("service name = %q, want %q", got, want)
	}
	if service.Methods().Len() != 1 {
		t.Fatalf("privileged method count = %d, want 1", service.Methods().Len())
	}
	method := service.Methods().Get(0)
	if got, want := string(method.Name()), "GetLocalSessionContext"; got != want {
		t.Fatalf("method name = %q, want %q", got, want)
	}
	assertExactFields(t, method.Input(), "session_id")
	assertExactFields(t, method.Output(), "workspace_path")

	harness := mecatlv1.File_mecatl_v1_harness_proto.Services().ByName("HarnessService")
	if harness == nil {
		t.Fatal("HarnessService descriptor is missing")
	}
	if harness.Methods().ByName("GetLocalSessionContext") != nil {
		t.Fatal("privileged method leaked into HarnessService")
	}
}

func TestADR_0296_UnregisteredServiceIsUnimplemented(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	defer grpcServer.Stop()
	go func() { _ = grpcServer.Serve(lis) }()
	defer lis.Close()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_, err = mecatlv1.NewLocalSessionContextServiceClient(conn).GetLocalSessionContext(
		context.Background(), &mecatlv1.GetLocalSessionContextRequest{SessionId: "session-1"},
	)
	if got := status.Code(err); got != codes.Unimplemented {
		t.Fatalf("unregistered privileged service code = %v, want %v (err = %v)", got, codes.Unimplemented, err)
	}
}

func TestADR_0296_PublicPathInventoryRemainsClosed(t *testing.T) {
	for _, message := range []protoreflect.ProtoMessage{
		&mecatlv1.CreateSessionRequest{}, &mecatlv1.CreateSessionResponse{}, &mecatlv1.Session{},
		&mecatlv1.SessionSummary{}, &mecatlv1.PlacementMetadata{}, &mecatlv1.ListWorktreesRequest{},
		&mecatlv1.Worktree{},
	} {
		fields := message.ProtoReflect().Descriptor().Fields()
		for _, forbidden := range []protoreflect.Name{"workspace_path", "workspace", "path", "root", "environment_ref", "environment_id"} {
			if fields.ByName(forbidden) != nil {
				t.Fatalf("public Harness message %s exposes %q", message.ProtoReflect().Descriptor().FullName(), forbidden)
			}
		}
	}

	for _, typ := range []reflect.Type{reflect.TypeOf(client.Placement{}), reflect.TypeOf(client.SessionSnapshot{})} {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			for _, fragment := range []string{"path", "workspace", "cwd", "root", "mount", "environmentref"} {
				if strings.Contains(name, fragment) {
					t.Fatalf("ordinary client type %s exposes placement field %s", typ, typ.Field(i).Name)
				}
			}
		}
	}

	svc := newADR0291Service(t)
	created, err := NewHarnessServer(svc).CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+created.GetSessionId()+"/local-context", nil)
	rec := httptest.NewRecorder()
	NewHTTPHandler(svc).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("privileged context HTTP route status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func assertExactFields(t *testing.T, message protoreflect.MessageDescriptor, want ...protoreflect.Name) {
	t.Helper()
	fields := message.Fields()
	if fields.Len() != len(want) {
		t.Fatalf("%s field count = %d, want %d", message.FullName(), fields.Len(), len(want))
	}
	for _, name := range want {
		if fields.ByName(name) == nil {
			t.Fatalf("%s missing %q", message.FullName(), name)
		}
	}
}
