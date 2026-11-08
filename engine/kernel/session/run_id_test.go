package session_test

import (
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestRunID(t *testing.T) {
	h := testHistoryID(t)
	first, second := allocatedRun(t, h, 1), allocatedRun(t, h, 2)
	copied := first
	if first.Validate() != nil || first.HistoryID() != h || !first.Equal(copied) || first == second {
		t.Fatal("allocated run identity or scope")
	}
	if (session.RunID{}).Validate() == nil {
		t.Fatal("zero run identity validated")
	}
}

func TestOccurrenceIDsExposeOnlyIdentityOperations(t *testing.T) {
	types := []reflect.Type{reflect.TypeFor[session.RunID](), reflect.TypeFor[session.UserInputID](), reflect.TypeFor[session.ModelRequestID](), reflect.TypeFor[session.RequestedCallID]()}
	for _, typ := range types {
		t.Run(typ.Name(), func(t *testing.T) {
			if !typ.Comparable() {
				t.Fatal("occurrence identity is not comparable")
			}
			for i := range typ.NumField() {
				field := typ.Field(i)
				if field.IsExported() {
					t.Errorf("public occurrence component: %s", field.Name)
				}
			}
			for _, receiver := range []reflect.Type{typ, reflect.PointerTo(typ)} {
				for i := range receiver.NumMethod() {
					switch name := receiver.Method(i).Name; name {
					case "Equal", "Validate", "HistoryID", "RunID", "RequestID", "String":
					default:
						t.Errorf("non-identity public operation: %s", name)
					}
				}
			}
			for _, other := range types {
				if typ != other && typ.ConvertibleTo(other) {
					t.Errorf("convertible identity kinds: %s and %s", typ, other)
				}
				if typ != other && typ.AssignableTo(other) {
					t.Errorf("interchangeable identity kinds: %s and %s", typ, other)
				}
			}
		})
	}
}
