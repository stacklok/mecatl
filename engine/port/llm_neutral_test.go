package port

import (
	"reflect"
	"sort"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestLLMRequestStaysProviderNeutral(t *testing.T) {
	want := []string{"Messages", "Model", "System", "Tools"}
	typ := reflect.TypeOf(LLMRequest{})
	var got []string
	for i := range typ.NumField() {
		got = append(got, typ.Field(i).Name)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LLMRequest fields = %v, want %v; provider-private configuration belongs on adapters", got, want)
	}
}

func TestCanonicalAndProviderContractsStaySeparate(t *testing.T) {
	for _, site := range []struct {
		typ       reflect.Type
		forbidden []string
	}{
		{reflect.TypeOf(session.Message{}), []string{"Reasoning", "ReasoningItemID"}},
		{reflect.TypeOf(session.ToolCall{}), []string{"ItemID", "InvocationID"}},
		{reflect.TypeOf(Chunk{}), []string{"ReasoningItemID"}},
	} {
		for _, name := range site.forbidden {
			if _, ok := site.typ.FieldByName(name); ok {
				t.Errorf("%s still exposes %s", site.typ, name)
			}
		}
	}
	if reflect.TypeOf(LLMRequest{}.Messages).Elem() != reflect.TypeOf(LLMMessage{}) {
		t.Fatal("LLM history must use separate DTOs")
	}
	for _, typ := range []reflect.Type{reflect.TypeOf(LLMToolCall{}), reflect.TypeOf(LLMToolResult{})} {
		for i := range typ.NumField() {
			field := typ.Field(i)
			if field.Anonymous || field.Type == reflect.TypeOf(session.ToolCall{}) || field.Type == reflect.TypeOf(session.ToolResult{}) || field.Type == reflect.TypeOf(session.ToolCallID("")) {
				t.Errorf("%s.%s embeds executable domain identity", typ, field.Name)
			}
		}
	}
}
