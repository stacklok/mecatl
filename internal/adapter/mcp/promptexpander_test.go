package mcp

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestParsePromptInvocation(t *testing.T) {
	cases := []struct {
		in         string
		wantOK     bool
		wantServer string
		wantName   string
		wantArgs   map[string]string
	}{
		{in: "/mcp__srv__greet who=Ada", wantOK: true, wantServer: "srv", wantName: "greet", wantArgs: map[string]string{"who": "Ada"}},
		{in: "  /mcp__srv__greet", wantOK: true, wantServer: "srv", wantName: "greet", wantArgs: nil},
		{in: "/mcp__srv__a__b k=v", wantOK: true, wantServer: "srv", wantName: "a__b", wantArgs: map[string]string{"k": "v"}},
		{in: "/review foo.go", wantOK: false},
		{in: "plain text", wantOK: false},
		{in: "/mcp__onlyserver", wantOK: false}, // no prompt segment
		{in: "/mcp____x", wantOK: false},        // empty server
	}
	for _, c := range cases {
		server, name, args, ok := parsePromptInvocation(c.in)
		if ok != c.wantOK {
			t.Errorf("parse(%q) ok = %v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if server != c.wantServer || name != c.wantName {
			t.Errorf("parse(%q) = (%q,%q), want (%q,%q)", c.in, server, name, c.wantServer, c.wantName)
		}
		if !reflect.DeepEqual(args, c.wantArgs) {
			t.Errorf("parse(%q) args = %v, want %v", c.in, args, c.wantArgs)
		}
	}
}

func TestPromptExpanderExpands(t *testing.T) {
	p := &fakeProvider{results: map[string]PromptResult{
		key("srv", "greet"): {Messages: []PromptMessage{
			{Role: "user", Text: "Hello Ada"},
		}},
	}}
	exp := NewPromptExpander(p)

	out, expanded, err := exp.Expand(context.Background(), "/mcp__srv__greet who=Ada")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !expanded {
		t.Fatalf("expected expanded=true")
	}
	if !strings.Contains(out, "user: Hello Ada") {
		t.Errorf("expanded = %q, want role-tagged content", out)
	}
}

func TestPromptExpanderNonMatchPassesThrough(t *testing.T) {
	exp := NewPromptExpander(&fakeProvider{})
	out, expanded, err := exp.Expand(context.Background(), "/review foo.go")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if expanded {
		t.Errorf("non-matching input should not expand")
	}
	if out != "/review foo.go" {
		t.Errorf("non-matching input changed: %q", out)
	}
}

func TestPromptExpanderUnknownPromptDoesNotAbort(t *testing.T) {
	// GetPrompt error → leave input unchanged, expanded=false, no error (so the
	// run is not aborted and the next expander / raw text can flow through).
	exp := NewPromptExpander(&fakeProvider{}) // no results registered → unknown
	out, expanded, err := exp.Expand(context.Background(), "/mcp__srv__missing")
	if err != nil {
		t.Fatalf("Expand returned error, want graceful pass-through: %v", err)
	}
	if expanded {
		t.Errorf("unknown prompt should not report expanded=true")
	}
	if out != "/mcp__srv__missing" {
		t.Errorf("input changed: %q", out)
	}
}
