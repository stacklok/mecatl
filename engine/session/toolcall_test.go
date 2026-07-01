package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestParseArgs locks the canonical arg-parse mechanic the agent loop and the
// adapter toolkit both delegate to: empty payload → zero-value dst + ok, valid
// JSON decodes, malformed JSON yields a model-facing error string + !ok.
func TestParseArgs(t *testing.T) {
	type payload struct {
		Key string `json:"key"`
	}

	t.Run("valid", func(t *testing.T) {
		var p payload
		msg, ok := ParseArgs(ToolCall{Args: json.RawMessage(`{"key":"v"}`)}, &p)
		if !ok || msg != "" {
			t.Fatalf("valid payload: ok=%v msg=%q", ok, msg)
		}
		if p.Key != "v" {
			t.Fatalf("decoded Key=%q, want v", p.Key)
		}
	})

	t.Run("empty leaves zero value", func(t *testing.T) {
		p := payload{Key: "untouched"}
		// A zero-value (no Args) call must not error; dst is left as-is.
		var fresh payload
		if msg, ok := ParseArgs(ToolCall{}, &fresh); !ok || msg != "" {
			t.Fatalf("empty payload: ok=%v msg=%q", ok, msg)
		}
		if fresh != (payload{}) {
			t.Fatalf("empty payload mutated dst: %+v", fresh)
		}
		_ = p
	})

	t.Run("malformed", func(t *testing.T) {
		var p payload
		msg, ok := ParseArgs(ToolCall{Args: json.RawMessage(`{bad`)}, &p)
		if ok {
			t.Fatal("malformed JSON accepted")
		}
		if msg == "" {
			t.Fatal("malformed JSON produced no model-facing message")
		}
	})
}

// TestToolResultPartsRoundTrip locks the JSON shape of ToolResult.Parts: a Parts
// slice round-trips through marshal/unmarshal, and a zero-value Parts is absent
// from JSON (backward compat with the pre-Parts string-only shape).
func TestToolResultPartsRoundTrip(t *testing.T) {
	parts := []Content{
		NewTextBlock("hello"),
		NewResourceLinkBlock("https://example.com/r", "n", "t", "d", "application/json", 7, []string{"user"}),
	}
	src := NewToolResultWithParts("call-1", "summary", parts)

	out, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ToolResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.CallID != src.CallID || got.Content != src.Content || got.IsError != src.IsError {
		t.Fatalf("round-trip mismatch: %+v != %+v", got, src)
	}
	if !reflect.DeepEqual(got.Parts, src.Parts) {
		t.Fatalf("parts round-trip mismatch:\n got=%+v\n src=%+v", got.Parts, src.Parts)
	}

	// Zero-value Parts absent from JSON (backward compat).
	plain, err := json.Marshal(NewToolResult("call-2", "just text"))
	if err != nil {
		t.Fatalf("marshal plain: %v", err)
	}
	if string(plain) == "" {
		t.Fatal("empty marshal")
	}
	var m map[string]any
	if err := json.Unmarshal(plain, &m); err != nil {
		t.Fatalf("unmarshal plain to map: %v", err)
	}
	if _, ok := m["Parts"]; ok {
		t.Fatalf("zero-value Parts present in JSON: %s", plain)
	}
}

func TestNewToolResultWithParts(t *testing.T) {
	parts := []Content{NewTextBlock("block")}
	r := NewToolResultWithParts("c1", "summary", parts)
	if r.CallID != "c1" {
		t.Fatalf("callid = %q", r.CallID)
	}
	if r.Content != "summary" {
		t.Fatalf("content = %q", r.Content)
	}
	if r.IsError {
		t.Fatal("isError = true, want false")
	}
	if len(r.Parts) != 1 || r.Parts[0].Text != "block" {
		t.Fatalf("parts = %+v", r.Parts)
	}
}
