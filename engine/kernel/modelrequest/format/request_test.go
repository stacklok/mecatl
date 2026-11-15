package format_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func limits() codec.Limits {
	return codec.Limits{MaxBytes: 1 << 22, MaxStringBytes: 1 << 20, MaxByteStringBytes: 1 << 20, MaxArrayElements: 10000, MaxMapPairs: 100, MaxDepth: 64, MaxNodes: 100000, MaxCollectionElements: 100000}
}

func request() format.RequestV1 { return format.RequestV1{API: "a", Model: "m"} }

func callResponse(ids ...string) format.ConversationItem {
	a := &format.AssistantMessage{}
	for _, id := range ids {
		a.Items = append(a.Items, format.AssistantItem{Call: &format.ToolCall{ProviderCallID: id, Arguments: []byte{0xff, '{', 'x'}}})
	}
	return format.ConversationItem{Assistant: a}
}

func result(id string) format.ConversationItem {
	return format.ConversationItem{ToolResult: &format.ToolResult{ProviderCallID: id, Outcome: format.ToolOutcomeReturnedSuccess}}
}

func TestRequestCanonical(t *testing.T) {
	v := request()
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	// Seven required short keys; there is no version, envelope or embedded identity.
	const want = "a761616161616380616d616d6170806172806173a0617480"
	if hex.EncodeToString(wire) != want {
		t.Fatalf("canonical request = %x", wire)
	}
	empty := v.Clone()
	empty.RootInstructions = []format.ContentBlock{}
	empty.UserContextPrefix = []format.ContentBlock{}
	empty.Tools = []sessionformat.ToolDefinition{}
	empty.Conversation = []format.ConversationItem{}
	other, err := codec.Encode(empty, limits())
	if err != nil || !bytes.Equal(wire, other) || !v.Equal(empty) {
		t.Fatalf("nil/empty: %v", err)
	}
	decoded, err := codec.Decode[format.RequestV1](wire, limits())
	if err != nil || !decoded.Equal(v) {
		t.Fatalf("roundtrip: %v", err)
	}
	// Optional text and envelopes retain meaningful present-empty distinctions.
	v.Conversation = []format.ConversationItem{{User: &format.UserMessage{Parts: []sessionformat.ContentPart{{Text: new("")}}}}, {Assistant: &format.AssistantMessage{Private: &sessionformat.OpaqueProviderData{Producer: "p", Format: "f", Version: 1}}}}
	wire, err = codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = codec.Decode[format.RequestV1](wire, limits())
	if err != nil || !decoded.Equal(v) {
		t.Fatalf("present empty: %v", err)
	}
	if decoded.Conversation[0].User.Parts[0].Text == nil || decoded.Conversation[1].Assistant.Private == nil {
		t.Fatal("lost presence")
	}
	absent := v.Clone()
	absent.Conversation[1].Assistant.Private = nil
	absentWire, err := codec.Encode(absent, limits())
	if err != nil || bytes.Equal(absentWire, wire) {
		t.Fatal("envelope presence collapsed")
	}
}

func TestRequestNestedEmptyCollections(t *testing.T) {
	v := request()
	v.RootInstructions = []format.ContentBlock{{}}
	v.UserContextPrefix = []format.ContentBlock{{}}
	v.Conversation = []format.ConversationItem{{User: &format.UserMessage{}}, {Assistant: &format.AssistantMessage{}}, callResponse("id"), result("id")}
	v.Conversation[2].Assistant.Items[0].Call.Arguments = nil
	empty := v.Clone()
	empty.RootInstructions[0].Parts = []sessionformat.ContentPart{}
	empty.UserContextPrefix[0].Parts = []sessionformat.ContentPart{}
	empty.Conversation[0].User.Parts = []sessionformat.ContentPart{}
	empty.Conversation[1].Assistant.Items = []format.AssistantItem{}
	empty.Conversation[2].Assistant.Items[0].Call.Arguments = []byte{}
	empty.Conversation[3].ToolResult.Parts = []sessionformat.ContentPart{}
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	other, err := codec.Encode(empty, limits())
	if err != nil || !bytes.Equal(wire, other) || !v.Equal(empty) {
		t.Fatalf("nested nil/empty: %v", err)
	}
	decoded, err := codec.Decode[format.RequestV1](wire, limits())
	if err != nil || !decoded.Equal(v) {
		t.Fatalf("nested roundtrip: %v", err)
	}
}

func TestRequestProviderIDsAndOpaqueArguments(t *testing.T) {
	v := request()
	v.Conversation = []format.ConversationItem{callResponse("same", "next"), result("same"), result("next"), callResponse("same"), result("same")}
	calls := v.Conversation[0].Assistant.Items
	v.Conversation[0].Assistant.Items = []format.AssistantItem{calls[0], {Output: &format.ContentBlock{}}, {Refusal: &format.ContentBlock{}}, calls[1]}
	for _, outcome := range []format.ToolOutcome{format.ToolOutcomeReturnedSuccess, format.ToolOutcomeReturnedError, format.ToolOutcomeNotInvoked, format.ToolOutcomeIndeterminate} {
		v.Conversation[1].ToolResult.Outcome = outcome
		wire, err := codec.Encode(v, limits())
		if err != nil || v.Validate() != nil {
			t.Fatalf("valid IDs/outcome: %v", err)
		}
		decoded, err := codec.Decode[format.RequestV1](wire, limits())
		if err != nil || !decoded.Equal(v) {
			t.Fatalf("roundtrip: %v", err)
		}
		if got := decoded.Conversation[0].Assistant.Items[0].Call; got.Name != "" || !bytes.Equal(got.Arguments, []byte{0xff, '{', 'x'}) {
			t.Fatal("rewrote opaque call")
		}
	}
}

func TestRequestMalformed(t *testing.T) {
	cases := map[string]func(*format.RequestV1){
		"empty API":       func(v *format.RequestV1) { v.API = "" },
		"bad model":       func(v *format.RequestV1) { v.Model = string([]byte{0xff}) },
		"empty effort":    func(v *format.RequestV1) { v.Settings.ReasoningEffort = new("") },
		"zero tokens":     func(v *format.RequestV1) { v.Settings.MaxOutputTokens = new(int64(0)) },
		"negative tokens": func(v *format.RequestV1) { v.Settings.MaxOutputTokens = new(int64(-1)) },
		"empty union":     func(v *format.RequestV1) { v.Conversation = []format.ConversationItem{{}} },
		"two messages": func(v *format.RequestV1) {
			v.Conversation = []format.ConversationItem{{User: &format.UserMessage{}, Assistant: &format.AssistantMessage{}}}
		},
		"three messages": func(v *format.RequestV1) {
			v.Conversation = []format.ConversationItem{{User: &format.UserMessage{}, Assistant: &format.AssistantMessage{}, ToolResult: result("a").ToolResult}}
		},
		"empty assistant union": func(v *format.RequestV1) {
			v.Conversation = []format.ConversationItem{{Assistant: &format.AssistantMessage{Items: []format.AssistantItem{{}}}}}
		},
		"two assistant branches": func(v *format.RequestV1) {
			v.Conversation = []format.ConversationItem{{Assistant: &format.AssistantMessage{Items: []format.AssistantItem{{Output: &format.ContentBlock{}, Refusal: &format.ContentBlock{}}}}}}
		},
		"empty provider ID": func(v *format.RequestV1) { v.Conversation = []format.ConversationItem{callResponse(""), result("")} },
		"duplicate provider ID": func(v *format.RequestV1) {
			v.Conversation = []format.ConversationItem{callResponse("x", "x"), result("x"), result("x")}
		},
		"orphan result":  func(v *format.RequestV1) { v.Conversation = []format.ConversationItem{result("x")} },
		"missing result": func(v *format.RequestV1) { v.Conversation = []format.ConversationItem{callResponse("x")} },
		"interrupted results": func(v *format.RequestV1) {
			v.Conversation = []format.ConversationItem{callResponse("x"), {User: &format.UserMessage{}}, result("x")}
		},
		"duplicate result": func(v *format.RequestV1) {
			v.Conversation = []format.ConversationItem{callResponse("x"), result("x"), result("x")}
		},
		"reordered results": func(v *format.RequestV1) {
			v.Conversation = []format.ConversationItem{callResponse("x", "y"), result("y"), result("x")}
		},
		"unknown outcome": func(v *format.RequestV1) {
			v.Conversation = []format.ConversationItem{callResponse("x"), result("x")}
			v.Conversation[1].ToolResult.Outcome = "other"
		},
		"duplicate tools": func(v *format.RequestV1) {
			v.Tools = []sessionformat.ToolDefinition{{Name: "t", ArgumentSchema: []byte("true")}, {Name: "t", ArgumentSchema: []byte("false")}}
		},
		"invalid part": func(v *format.RequestV1) {
			v.RootInstructions = []format.ContentBlock{{Parts: []sessionformat.ContentPart{{}}}}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			v := request()
			change(&v)
			if v.Validate() == nil {
				t.Fatal("validated malformed request")
			}
			if wire, err := codec.Encode(v, limits()); wire != nil || !errors.Is(err, codec.ErrInvalidData) {
				t.Fatalf("encode: %x %v", wire, err)
			}
			got, err := codec.Decode[format.RequestV1](v.AppendCBOR(nil), limits())
			if !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.RequestV1{}) {
				t.Fatalf("decode: %v", err)
			}
		})
	}
}

func TestRequestSettingsAndClone(t *testing.T) {
	v := request()
	v.Settings = sessionformat.ModelSettings{ReasoningEffort: new(" exact target-native "), MaxOutputTokens: new(int64(9223372036854775807))}
	v.Tools = []sessionformat.ToolDefinition{{Name: "t", Description: "d", ArgumentSchema: []byte(`{"type":"object"}`)}}
	v.Conversation = []format.ConversationItem{callResponse("x"), result("x")}
	clone := v.Clone()
	wire, err := codec.Encode(v, limits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode[format.RequestV1](wire, limits())
	if err != nil || !got.Equal(v) {
		t.Fatalf("settings: %v", err)
	}
	*v.Settings.ReasoningEffort = "changed"
	*v.Settings.MaxOutputTokens = 1
	v.Tools[0].ArgumentSchema[0] = '!'
	v.Conversation[0].Assistant.Items[0].Call.Arguments[0] = 0
	clear(wire)
	if !got.Equal(clone) {
		t.Fatal("clone/decode shares mutable backing")
	}
}
