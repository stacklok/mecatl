package server

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/stacklok/mecatl/engine/session"
)

// fillStrings walks v and sets every field whose type is EXACTLY string (not a
// named string typedef — those are the closed enum/kind vocabularies) to a value
// containing invalid UTF-8, allocating pointers and seeding one element into
// every nil slice/map so a whole Event tree is populated from a zero value.
func fillStrings(v reflect.Value, path string, hits *[]string) {
	if harnessTokenFields[path] {
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillStrings(v.Elem(), path, hits)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			fillStrings(v.Field(i), path+"."+f.Name, hits)
		}
	case reflect.Slice:
		if v.Type() == reflect.TypeOf(json.RawMessage(nil)) {
			v.Set(reflect.ValueOf(json.RawMessage(`{"k":"` + badUTF8 + `"}`)))
			*hits = append(*hits, path)
			return
		}
		if v.Len() == 0 {
			v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		}
		for i := 0; i < v.Len(); i++ {
			fillStrings(v.Index(i), path+"[]", hits)
		}
	case reflect.Map:
		if v.IsNil() {
			v.Set(reflect.MakeMap(v.Type()))
		}
		k := reflect.New(v.Type().Key()).Elem()
		val := reflect.New(v.Type().Elem()).Elem()
		fillStrings(k, path+"{key}", hits)
		fillStrings(val, path+"{val}", hits)
		v.SetMapIndex(k, val)
	case reflect.String:
		if v.Type() != reflect.TypeOf("") {
			return // named typedef: a closed kind/stop/role vocabulary, never prose
		}
		if v.CanSet() {
			v.SetString(badUTF8)
			*hits = append(*hits, path)
		}
	}
}

// harnessTokenFields are the bare-string fields the mapper deliberately does NOT
// repair because nothing producer-influenced can reach them: harness-minted ids,
// and selectors that come from operator config or the model catalog rather than
// from a tool, a file, or an MCP server.
//
// This list is the POINT of the test. The old fixture-based oracle failed OPEN —
// forget to seed a new field and it passed while the hole shipped. This one fails
// CLOSED: a new domain string is seeded automatically, so it must either be run
// through valid() or be justified HERE. Adding a line to this list is a decision
// a reviewer can see; forgetting to add one to a fixture was invisible.
var harnessTokenFields = map[string]bool{
	// Harness-minted correlation ids.
	".ToolCall.ID": true, ".ToolResult.CallID": true, ".Ask.AskID": true,
	".Ask.Call": true, ".Approval.AskID": true, ".Approval.Call": true,
	".Hook.CallID": true, ".Subagent.ParentCallID": true, ".Subagent.ChildID": true,
	".Team.ParentCallID": true, ".Team.TeamID": true, ".Team.MemberSessionID": true,
	".Parallel.ParentCallID": true, ".Parallel.ChildID": true,
	".Schedule.ScheduleName": true, ".Schedule.FireID": true, ".Schedule.SessionID": true,
	".Team.Tasks[].ID": true, ".Team.Tasks[].State": true,
	// Verdict/disposition vocabularies carried as bare strings (closed sets).
	".Approval.Verdict": true, ".Schedule.Kind": true,
	".Team.Dispositions[].Disposition": true, ".Team.Dispositions[].Reason": true,
	// Model/route selectors: operator config or the model catalog, never a producer.
	".Subagent.Model": true, ".Subagent.RoutedCategory": true, ".Subagent.RoutedModel": true,
	".Team.Roster[].RoutedCategory": true, ".Team.Roster[].RoutedModel": true,
	".Team.Roster[].Model": true,
	".Parallel.Model":      true, ".Parallel.RoutedCategory": true, ".Parallel.RoutedModel": true,
	// RoutingReason (issue #397): harness gate constants / classifier miss codes /
	// operator-authored category names, confined to a closed allowlist and reduced to a
	// generic label otherwise (routingReasonPayload, engine/agent/subagent.go) — never
	// producer/task/classifier prose.
	".Subagent.RoutingReason": true, ".Team.Roster[].RoutingReason": true, ".Parallel.RoutingReason": true,
	// MIME is an IANA machine token, byte-exact by contract (see RepairToolResult).
	".ToolResult.Parts[].MIMEType":                              true,
	".UserPrompt.Parts[].MIMEType":                              true,
	".Steer.Parts[].MIMEType":                                   true,
	".CompactionArchive.Replaced[].Parts[].MIMEType":            true,
	".CompactionArchive.Replaced[].ToolCalls[].ID":              true,
	".CompactionArchive.Replaced[].ToolResult.CallID":           true,
	".CompactionArchive.Replaced[].ToolResult.Parts[].MIMEType": true,
}

// TestToProtoStructuralUTF8Guard is the STRUCTURAL half of the issue-#402
// backstop, and the reason it exists is that its fixture-based sibling cannot
// do this job: TestToProtoNeverFailsMarshalOnInvalidUTF8 hand-builds its
// payloads, so a new string field is covered only if someone remembers to seed
// it — the same remembering the backstop exists to replace.
//
// This test reflects over session.Event, seeds EVERY bare-string field with
// invalid UTF-8, maps the result through toProto, then walks the proto message
// with protoreflect asserting utf8.ValidString on every populated string field
// AND every map key (proto3 validates both). A new domain string field that the
// mapper forgets to run through valid() fails here with no test edit at all.
func TestToProtoStructuralUTF8Guard(t *testing.T) {
	var ev session.Event
	var seeded []string
	fillStrings(reflect.ValueOf(&ev).Elem(), "", &seeded)
	ev.Type = session.EvToolResult // a real kind; toProto keys submessages off the pointers

	if len(seeded) < 40 {
		t.Fatalf("filler seeded only %d fields — it stopped walking the tree", len(seeded))
	}

	var bad []string
	walk(toProto(ev).ProtoReflect(), "Event", &bad)
	if len(bad) > 0 {
		sort.Strings(bad)
		t.Fatalf("proto string fields carry invalid UTF-8 (a marshal here is codes.Internal on the wire):\n  %s\n"+
			"Run the domain value through valid() in mapper.go, or — if the field genuinely cannot\n"+
			"carry producer text — add its DOMAIN path to harnessTokenFields with the reason.",
			strings.Join(bad, "\n  "))
	}
}

// walk asserts valid UTF-8 on every populated string field and map key in m.
func walk(m protoreflect.Message, path string, bad *[]string) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		name := path + "." + string(fd.Name())
		switch {
		case fd.IsMap():
			v.Map().Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
				if fd.MapKey().Kind() == protoreflect.StringKind && !utf8.ValidString(k.String()) {
					*bad = append(*bad, name+"{key}")
				}
				checkLeaf(fd.MapValue(), mv, name+"{val}", bad)
				return true
			})
		case fd.IsList():
			for i := 0; i < v.List().Len(); i++ {
				checkLeaf(fd, v.List().Get(i), name+"[]", bad)
			}
		default:
			checkLeaf(fd, v, name, bad)
		}
		return true
	})
}

func checkLeaf(fd protoreflect.FieldDescriptor, v protoreflect.Value, path string, bad *[]string) {
	switch fd.Kind() {
	case protoreflect.StringKind:
		if !utf8.ValidString(v.String()) {
			*bad = append(*bad, path)
		}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		walk(v.Message(), path, bad)
	}
}
