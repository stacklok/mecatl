package session

import (
	"reflect"
	"testing"
)

func TestProviderModelID_IsOpaqueServerSelectedIdentity(t *testing.T) {
	identity := ProviderModelID{ProviderID: "jev", ModelID: "classifier-v1"}

	if identity.ProviderID != "jev" || identity.ModelID != "classifier-v1" {
		t.Fatalf("ProviderModelID = %#v, want exact provider/model identity", identity)
	}

	typeOfIdentity := reflect.TypeOf(identity)
	if typeOfIdentity.NumField() != 2 {
		t.Fatalf("ProviderModelID has %d fields, want only provider and model identity", typeOfIdentity.NumField())
	}
	for _, name := range []string{"ProviderID", "ModelID"} {
		field, ok := typeOfIdentity.FieldByName(name)
		if !ok || field.Type.Kind() != reflect.String {
			t.Fatalf("ProviderModelID.%s = %#v, want exported string field", name, field)
		}
	}
	if typeOfIdentity.NumMethod() != 0 || reflect.PointerTo(typeOfIdentity).NumMethod() != 0 {
		t.Fatal("ProviderModelID has methods, want an opaque value object")
	}
}
