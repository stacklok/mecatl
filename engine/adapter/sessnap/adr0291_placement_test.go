package sessnap_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0291_SessionAndSnapshotPersistOnlyEnvironmentRef(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/private/root", Revision: "inventory-v7"}
	sess := session.New("placement-only", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0).UTC())

	for _, typ := range []reflect.Type{reflect.TypeOf(*sess), reflect.TypeOf(sessnap.Snapshot{})} {
		for _, forbidden := range []string{"Workspace", "Environment", "CommandRunner", "Credentials", "Transport", "Process"} {
			if _, ok := typ.FieldByName(forbidden); ok {
				t.Fatalf("%s contains duplicate/live placement field %s", typ, forbidden)
			}
		}
		field, ok := typ.FieldByName("EnvironmentRef")
		if !ok || field.Type != reflect.TypeOf(session.EnvironmentRef{}) {
			t.Fatalf("%s EnvironmentRef field = %+v, want session.EnvironmentRef", typ, field)
		}
	}

	line := mustMarshal(t, sess)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(line, &object); err != nil {
		t.Fatal(err)
	}
	if len(object["environment_ref"]) == 0 {
		t.Fatalf("snapshot omitted environment_ref: %s", line)
	}
	for _, forbidden := range []string{"workspace", "environment", "command_runner", "credentials", "transport", "process"} {
		if _, ok := object[forbidden]; ok {
			t.Fatalf("snapshot persisted duplicate/live placement field %q: %s", forbidden, line)
		}
	}
	restored, err := sessnap.Unmarshal(line)
	if err != nil {
		t.Fatal(err)
	}
	if restored.EnvironmentRef != ref {
		t.Fatalf("restored ref = %+v, want %+v", restored.EnvironmentRef, ref)
	}
}

func TestADR_0291_UnknownPlacementFieldsAreIgnored(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/private/root", Revision: "inventory-v7"}
	line := mustMarshal(t, session.New("placement", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0).UTC()))
	var object map[string]json.RawMessage
	if err := json.Unmarshal(line, &object); err != nil {
		t.Fatal(err)
	}

	for name, value := range map[string]json.RawMessage{
		"workspace":               json.RawMessage(`{"Kind":false}`),
		"adoption_source_id":      json.RawMessage(`["malformed-shaped"]`),
		"adoption_request_digest": json.RawMessage(`true`),
	} {
		object[name] = value
	}
	wire, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sessnap.Unmarshal(wire)
	if err != nil {
		t.Fatalf("Unmarshal with unknown fields: %v", err)
	}
	if got.EnvironmentRef != ref {
		t.Fatalf("restored ref = %+v, want canonical %+v", got.EnvironmentRef, ref)
	}

	delete(object, "environment_ref")
	zeroRef, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessnap.Unmarshal(zeroRef); err == nil {
		t.Fatal("snapshot without environment_ref was accepted")
	}
}
