package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0290_PathSurfaceInventoryEnforcesPublicBoundary(t *testing.T) {
	forbidden := map[protoreflect.Name]bool{
		"path": true, "workspace": true, "cwd": true, "root": true, "mount": true,
		"environment_ref": true, "environment_id": true, "winner_workspace": true,
	}
	messages := mecatlv1.File_mecatl_v1_harness_proto.Messages()
	var inspectMessages func(protoreflect.MessageDescriptors)
	inspectMessages = func(descs protoreflect.MessageDescriptors) {
		for i := 0; i < descs.Len(); i++ {
			desc := descs.Get(i)
			fields := desc.Fields()
			for j := 0; j < fields.Len(); j++ {
				if forbidden[fields.Get(j).Name()] {
					t.Fatalf("public Harness field %s.%s exposes placement path/ref authority", desc.FullName(), fields.Get(j).Name())
				}
			}
			inspectMessages(desc.Messages())
		}
	}
	inspectMessages(messages)

	for _, typ := range []reflect.Type{reflect.TypeOf(client.Placement{}), reflect.TypeOf(client.SessionSnapshot{})} {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			for _, fragment := range []string{"path", "workspace", "cwd", "root", "mount", "environmentref"} {
				if strings.Contains(name, fragment) {
					t.Fatalf("public mecatui client type %s exposes field %s", typ, typ.Field(i).Name)
				}
			}
		}
	}

	svc := newADR0289Service(t)
	for name, body := range map[string]string{
		"create workspace": `{"workspace":"/attacker"}`,
		"create cwd":       `{"cwd":"/attacker"}`,
		"create exact ref": `{"environment_ref":{"kind":"local","id":"/attacker","revision":"r1"}}`,
	} {
		t.Run("HTTP rejects "+name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewBufferString(body))
			rec := httptest.NewRecorder()
			NewHTTPHandler(svc).ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown field") {
				t.Fatalf("status/body = %d %s", rec.Code, rec.Body.String())
			}
		})
	}
	created, err := NewHarnessServer(svc).CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+created.GetSessionId(), nil)
	rec := httptest.NewRecorder()
	NewHTTPHandler(svc).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET session = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), adrPrivateRoot) || strings.Contains(rec.Body.String(), "environment_ref") {
		t.Fatalf("HTTP projection exposed private placement: %s", rec.Body.String())
	}
	var projected map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &projected); err != nil {
		t.Fatalf("HTTP session response is not JSON: %v", err)
	}

	for _, typ := range []reflect.Type{reflect.TypeOf(session.Session{}), reflect.TypeOf(sessnap.Snapshot{})} {
		field, ok := typ.FieldByName("EnvironmentRef")
		if !ok || field.Type != reflect.TypeOf(session.EnvironmentRef{}) {
			t.Fatalf("trusted private storage type %s lost exact EnvironmentRef", typ)
		}
		if _, duplicate := typ.FieldByName("Workspace"); duplicate {
			t.Fatalf("trusted private storage type %s retains duplicate Workspace", typ)
		}
	}
	driverFields := (&driverv1.SessionMetadataEntry{}).ProtoReflect().Descriptor().Fields()
	if driverFields.ByName("environment_ref") == nil {
		t.Fatal("trusted driver storage lost exact private environment_ref")
	}
	storedRef := (&driverv1.StoredEnvironmentRef{}).ProtoReflect().Descriptor().Fields()
	for _, name := range []protoreflect.Name{"kind", "id", "revision"} {
		if storedRef.ByName(name) == nil {
			t.Fatalf("trusted driver StoredEnvironmentRef missing %s", name)
		}
	}
}
