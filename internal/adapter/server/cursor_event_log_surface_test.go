package server

import (
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// gapVocabulary is the words a gap-related field would plausibly be spelled
// with. Matching on a vocabulary rather than one literal is deliberate: the
// invariant is that the CONCEPT stays out of the event taxonomy, and a field
// named "activity_gap", "missed", or "gap_reason" would violate it just as
// surely as one named "gap".
var gapVocabulary = []string{"gap", "missed", "activitygap"}

func mentionsGap(name string) bool {
	flat := strings.ToLower(strings.ReplaceAll(name, "_", ""))
	for _, w := range gapVocabulary {
		if strings.Contains(flat, w) {
			return true
		}
	}
	return false
}

// TestADR_0250_GapAddsNoEventKind is AC6.8: session.Event and the proto Event
// message gain no gap-related field, and the event kind-parity surface is
// unchanged.
//
// This is the structural half of ADR 0250 decision 5. A gap is a log-record
// ENVELOPE variant, and the entire value of that choice is what it keeps out of:
// the event taxonomy, the proto Event message, the kind-parity gate, the
// TypeScript event union, and every consumer that folds events into a session.
// Decision 5 is one refactor away from being undone — "just add an EvGap so
// clients can render it" is a natural-sounding change that would silently
// relocate a delivery concern into the domain — so the absence is asserted
// rather than trusted.
func TestADR_0250_GapAddsNoEventKind(t *testing.T) {
	t.Run("the proto Event message has no gap field", func(t *testing.T) {
		// Walked transitively: a gap field smuggled into a nested payload
		// (Result, Hook, Subagent, …) would reach the wire just as well as one
		// on Event itself.
		seen := map[protoreflect.FullName]bool{}
		var walk func(d protoreflect.MessageDescriptor, path string)
		walk = func(d protoreflect.MessageDescriptor, path string) {
			if seen[d.FullName()] {
				return
			}
			seen[d.FullName()] = true
			fields := d.Fields()
			for i := range fields.Len() {
				f := fields.Get(i)
				where := path + "." + string(f.Name())
				if mentionsGap(string(f.Name())) || mentionsGap(f.JSONName()) {
					t.Errorf("proto field %s is gap-related; a gap is a log-record envelope variant and must not enter the Event message (ADR 0250 decision 5)", where)
				}
				if f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind {
					walk(f.Message(), where)
				}
			}
		}
		walk((&mecatlv1.Event{}).ProtoReflect().Descriptor(), "Event")
	})

	t.Run("session.Event has no gap field", func(t *testing.T) {
		seen := map[reflect.Type]bool{}
		var walk func(t2 reflect.Type, path string)
		walk = func(t2 reflect.Type, path string) {
			for t2.Kind() == reflect.Pointer || t2.Kind() == reflect.Slice || t2.Kind() == reflect.Array {
				t2 = t2.Elem()
			}
			if t2.Kind() != reflect.Struct || seen[t2] {
				return
			}
			seen[t2] = true
			for i := range t2.NumField() {
				f := t2.Field(i)
				where := path + "." + f.Name
				if mentionsGap(f.Name) {
					t.Errorf("session.Event field %s is gap-related; the domain event taxonomy must not carry a delivery concern (ADR 0250 decision 5)", where)
				}
				walk(f.Type, where)
			}
		}
		walk(reflect.TypeOf(session.Event{}), "Event")
	})

	t.Run("the gap vocabulary lives in port, not session", func(t *testing.T) {
		// Where the type is declared IS the invariant: a gap is expressible only
		// as a port-level log-record kind. If this ever became a
		// session.EventType, decision 5 would be reversed no matter what the
		// field walks above report.
		kind := reflect.TypeOf(port.LogRecordGap)
		if got, want := kind.PkgPath(), "github.com/stacklok/mecatl/engine/port"; got != want {
			t.Errorf("port.LogRecordGap is declared in %q, want %q", got, want)
		}
		if got, want := kind.Name(), "LogRecordKind"; got != want {
			t.Errorf("port.LogRecordGap has type %q, want %q", got, want)
		}
		if kind == reflect.TypeOf(session.EventType("")) {
			t.Fatal("the gap kind has become a session.EventType; a gap must never be an event")
		}
	})

	t.Run("no session.EventType spells a gap", func(t *testing.T) {
		// The taxonomy is a closed set of string constants, and the proto `type`
		// field is an open string, so nothing mechanical would reject an
		// "EvGap = \"gap\"" landing beside the others. Assert against the
		// projection the mapper actually produces: a gap value must not be a
		// recognised event kind on either side.
		for _, w := range gapVocabulary {
			ev := session.Event{Type: session.EventType(w)}
			got := toProto(ev)
			if got == nil {
				t.Fatalf("toProto(%q) returned nil", w)
			}
			// The mapper passes `type` through as an open string — that is the
			// documented discipline — so the assertion is that it did NOT grow a
			// structured gap projection alongside it.
			if got.GetHook() != nil || got.GetResult() != nil {
				t.Errorf("toProto(%q) produced a structured payload; a gap-named kind must have no meaning in the event mapper", w)
			}
		}
	})
}
