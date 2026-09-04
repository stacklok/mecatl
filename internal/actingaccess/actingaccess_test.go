package actingaccess

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestActingAccess_Scenario1_RejectsNonCanonicalRequest(t *testing.T) {
	owner, err := NewOwner("https://issuer.example", "alice")
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := NewPresenter("broker-prod")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry([]Registration{{
		Resource:      "vmcp-repository",
		Operation:     "read",
		Scopes:        []string{"repo:read", "repo:status"},
		RequiredTools: []string{"Read"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	valid, err := registry.NewRequest(owner, presenter, "vmcp-repository", "read", []string{"repo:read", "repo:status"})
	if err != nil {
		t.Fatalf("canonical request: %v", err)
	}
	if got := valid.Scopes(); !reflect.DeepEqual(got, []string{"repo:read", "repo:status"}) {
		t.Fatalf("Scopes() = %#v", got)
	}
	got := valid.Scopes()
	got[0] = "repo:write"
	if valid.Scopes()[0] != "repo:read" {
		t.Fatal("request scopes alias caller mutation")
	}

	badOwners := [][2]string{
		{"", "alice"}, {" https://issuer.example", "alice"}, {"https://issuer.example/../tenant", "alice"},
		{"https://issuer.example", ""}, {"https://issuer.example", " alice"}, {"https://issuer.example", "ali\x00ce"},
	}
	for _, parts := range badOwners {
		if _, err := NewOwner(parts[0], parts[1]); err == nil {
			t.Errorf("NewOwner(%q, %q) succeeded", parts[0], parts[1])
		}
	}
	for _, raw := range []string{"", " broker", "broker\nprod", "BROKER"} {
		if _, err := NewPresenter(raw); err == nil {
			t.Errorf("NewPresenter(%q) succeeded", raw)
		}
	}
	for _, tc := range []struct {
		name      string
		resource  string
		operation string
		scopes    []string
	}{
		{"empty resource", "", "read", []string{"repo:read"}},
		{"unknown resource", "other", "read", []string{"repo:read"}},
		{"unknown operation", "vmcp-repository", "write", []string{"repo:read"}},
		{"empty scopes", "vmcp-repository", "read", nil},
		{"duplicate scope", "vmcp-repository", "read", []string{"repo:read", "repo:read"}},
		{"unsorted scopes", "vmcp-repository", "read", []string{"repo:status", "repo:read"}},
		{"unknown scope", "vmcp-repository", "read", []string{"repo:write"}},
		{"control scope", "vmcp-repository", "read", []string{"repo:\nread"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := registry.NewRequest(owner, presenter, tc.resource, tc.operation, tc.scopes); err == nil {
				t.Fatal("NewRequest succeeded")
			}
		})
	}
	oversized := make([]byte, maxValueBytes+1)
	for i := range oversized {
		oversized[i] = 'a'
	}
	if _, err := NewPresenter(string(oversized)); err == nil {
		t.Fatal("oversized presenter accepted")
	}
}

func TestInvariant_acting_access_closed_inputs(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(Request{}), reflect.TypeOf(Owner{}), reflect.TypeOf(Presenter{}),
		reflect.TypeOf(RegisteredResource{}), reflect.TypeOf(RegisteredOperation{}),
		reflect.TypeOf(Scope{}), reflect.TypeOf(VerifiedSubject{}), reflect.TypeOf(VerifiedActor{}),
	}
	for _, typ := range types {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.IsExported() {
				t.Errorf("%s exposes field %s", typ, field.Name)
			}
			if field.Type.Kind() == reflect.Map {
				t.Errorf("%s exposes generic map field %s", typ, field.Name)
			}
		}
	}
	forbidden := []string{"URL", "Audience", "Authorization", "Credential", "SessionStore", "EventLog", "ToolHive"}
	for _, typ := range types {
		for i := 0; i < typ.NumMethod(); i++ {
			for _, word := range forbidden {
				if typ.Method(i).Name == word {
					t.Errorf("%s exposes forbidden method %s", typ, word)
				}
			}
		}
	}
}

func TestActingAccess_Scenario1_SeparatesCredentialProfiles(t *testing.T) {
	const canary = "secret-canary"
	subject, err := NewSubjectAssertion(canary)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := NewI2Token(canary)
	if err != nil {
		t.Fatal(err)
	}
	output, err := NewOutputToken(canary)
	if err != nil {
		t.Fatal(err)
	}
	types := []reflect.Type{reflect.TypeOf(subject), reflect.TypeOf(actor), reflect.TypeOf(output)}
	for i := range types {
		for j := i + 1; j < len(types); j++ {
			if types[i].AssignableTo(types[j]) || types[j].AssignableTo(types[i]) {
				t.Fatalf("credential profiles are assignable: %s and %s", types[i], types[j])
			}
		}
	}
	for name, value := range map[string]any{"subject": subject, "actor": actor, "output": output} {
		if got := fmt.Sprintf("%v %#v", value, value); contains(got, canary) {
			t.Errorf("%s formatted secret: %q", name, got)
		}
		if raw, err := json.Marshal(value); err == nil && (string(raw) != "{}" || contains(string(raw), canary)) {
			t.Errorf("%s serialized credential as %s", name, raw)
		}
		typ := reflect.TypeOf(value)
		for _, method := range []string{"String", "GoString", "MarshalJSON", "MarshalText", "GobEncode"} {
			if _, ok := typ.MethodByName(method); ok {
				t.Errorf("%s exposes %s", name, method)
			}
		}
	}
}

func contains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
