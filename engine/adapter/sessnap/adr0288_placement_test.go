package sessnap_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0288_SessionAndSnapshotPersistOnlyEnvironmentRef(t *testing.T) {
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

func TestADR_0288_LegacyDuplicatePlacementStateIsUnsupported(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/private/root", Revision: "inventory-v7"}
	line := mustMarshal(t, session.New("legacy-placement", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0).UTC()))
	var object map[string]json.RawMessage
	if err := json.Unmarshal(line, &object); err != nil {
		t.Fatal(err)
	}

	for _, legacy := range []string{"workspace", "adoption_source_id", "adoption_request_digest"} {
		t.Run(legacy, func(t *testing.T) {
			duplicate := make(map[string]json.RawMessage, len(object)+1)
			for key, value := range object {
				duplicate[key] = value
			}
			duplicate[legacy] = json.RawMessage(`"/attacker/legacy-root"`)
			wire, err := json.Marshal(duplicate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sessnap.Unmarshal(wire); err == nil || !strings.Contains(err.Error(), "unsupported legacy duplicate placement field") {
				t.Fatalf("Unmarshal(%s) = %v, want explicit unsupported duplicate-placement error", legacy, err)
			}
		})
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
