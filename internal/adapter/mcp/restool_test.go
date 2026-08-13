package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// fakeProvider is an in-memory Provider for unit-testing the resource tools and
// the prompt expander without a live MCP server.
type fakeProvider struct {
	resources map[string][]Resource       // server -> resources
	contents  map[string]ResourceContents // "server\x00uri" -> contents
	prompts   map[string][]Prompt         // server -> prompts
	results   map[string]PromptResult     // "server\x00name" -> result
	readErr   error                       // forced ReadResource error
	getErr    error                       // forced GetPrompt error
}

func key(a, b string) string { return a + "\x00" + b }

func (f *fakeProvider) ListResources(_ context.Context, server string) ([]Resource, error) {
	if server == "" {
		var all []Resource
		for _, rs := range f.resources {
			all = append(all, rs...)
		}
		return all, nil
	}
	return f.resources[server], nil
}

func (f *fakeProvider) ReadResource(_ context.Context, server, uri string) (ResourceContents, error) {
	if f.readErr != nil {
		return ResourceContents{}, f.readErr
	}
	c, ok := f.contents[key(server, uri)]
	if !ok {
		return ResourceContents{}, errors.New("unknown resource")
	}
	return c, nil
}

func (f *fakeProvider) ListPrompts(_ context.Context, server string) ([]Prompt, error) {
	if server == "" {
		var all []Prompt
		for _, ps := range f.prompts {
			all = append(all, ps...)
		}
		return all, nil
	}
	return f.prompts[server], nil
}

func (f *fakeProvider) GetPrompt(_ context.Context, server, name string, _ map[string]string) (PromptResult, error) {
	if f.getErr != nil {
		return PromptResult{}, f.getErr
	}
	r, ok := f.results[key(server, name)]
	if !ok {
		return PromptResult{}, errors.New("unknown prompt")
	}
	return r, nil
}

// CallTool is unused by the resource/prompt tool tests; it returns a sentinel
// error so an accidental call surfaces clearly rather than reporting a phantom
// result. The production *Manager implements it against a live session.
func (fakeProvider) CallTool(_ context.Context, server, toolName string, _ json.RawMessage) (CallResult, error) {
	return CallResult{}, fmt.Errorf("%w: %q.%q", ErrUnknownServer, server, toolName)
}

var _ Provider = (*fakeProvider)(nil)

func TestResourceToolsReadOnly(t *testing.T) {
	if !(listResourcesTool{}).ReadOnly() {
		t.Errorf("ListMcpResources must be ReadOnly")
	}
	if !(readResourceTool{}).ReadOnly() {
		t.Errorf("ReadMcpResource must be ReadOnly")
	}
}

func TestRegisterResourceToolsGating(t *testing.T) {
	// Empty provider → no registration.
	empty := &fakeProvider{}
	cat := tool.NewCatalog()
	reg, err := RegisterResourceTools(cat, empty)
	if err != nil {
		t.Fatalf("RegisterResourceTools: %v", err)
	}
	if reg {
		t.Errorf("expected no registration for an empty provider")
	}
	if _, ok := cat.Lookup(listResourcesToolName); ok {
		t.Errorf("ListMcpResources registered despite no resources")
	}

	// Non-empty provider → both tools registered.
	p := &fakeProvider{resources: map[string][]Resource{
		"a": {{Server: "a", URI: "test://x", ReadOnly: true}},
	}}
	cat2 := tool.NewCatalog()
	reg2, err := RegisterResourceTools(cat2, p)
	if err != nil {
		t.Fatalf("RegisterResourceTools: %v", err)
	}
	if !reg2 {
		t.Fatalf("expected registration for a non-empty provider")
	}
	if _, ok := cat2.Lookup(listResourcesToolName); !ok {
		t.Errorf("ListMcpResources not registered")
	}
	if _, ok := cat2.Lookup(readResourceToolName); !ok {
		t.Errorf("ReadMcpResource not registered")
	}
}

func TestListResourcesToolFiltersByServer(t *testing.T) {
	p := &fakeProvider{resources: map[string][]Resource{
		"a": {{Server: "a", URI: "test://a1"}},
		"b": {{Server: "b", URI: "test://b1"}},
	}}
	tl := listResourcesTool{provider: p}

	call := session.NewToolCall("c1", listResourcesToolName, json.RawMessage(`{"server":"a"}`))
	res, err := tl.Execute(context.Background(), call, tool.Environment{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	if !strings.Contains(res.Content, "test://a1") || strings.Contains(res.Content, "test://b1") {
		t.Errorf("filtered list = %q, want only server a", res.Content)
	}
}

func TestReadResourceToolText(t *testing.T) {
	p := &fakeProvider{contents: map[string]ResourceContents{
		key("a", "test://x"): {URI: "test://x", Text: "the body"},
	}}
	tl := readResourceTool{provider: p}
	call := session.NewToolCall("c1", readResourceToolName, json.RawMessage(`{"server":"a","uri":"test://x"}`))
	res, err := tl.Execute(context.Background(), call, tool.Environment{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError || res.Content != "the body" {
		t.Errorf("read result = %+v, want 'the body'", res)
	}
}

func TestReadResourceToolUnknownURIIsToolError(t *testing.T) {
	p := &fakeProvider{readErr: errors.New("unknown resource")}
	tl := readResourceTool{provider: p}
	call := session.NewToolCall("c1", readResourceToolName, json.RawMessage(`{"server":"a","uri":"test://missing"}`))
	res, err := tl.Execute(context.Background(), call, tool.Environment{})
	if err != nil {
		t.Fatalf("Execute returned hard error, want model-facing tool error: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError result for unknown URI, got %+v", res)
	}
}

func TestReadResourceToolRequiresArgs(t *testing.T) {
	tl := readResourceTool{provider: &fakeProvider{}}
	// Missing uri.
	call := session.NewToolCall("c1", readResourceToolName, json.RawMessage(`{"server":"a"}`))
	res, _ := tl.Execute(context.Background(), call, tool.Environment{})
	if !res.IsError {
		t.Errorf("missing uri should be a tool error")
	}
	// Missing server.
	call2 := session.NewToolCall("c2", readResourceToolName, json.RawMessage(`{"uri":"test://x"}`))
	res2, _ := tl.Execute(context.Background(), call2, tool.Environment{})
	if !res2.IsError {
		t.Errorf("missing server should be a tool error")
	}
}

// TestResourceToolsInPlanCatalog asserts the resource tools survive the
// plan-mode filter (which keeps only read-only tools).
func TestResourceToolsInPlanCatalog(t *testing.T) {
	p := &fakeProvider{resources: map[string][]Resource{
		"a": {{Server: "a", URI: "test://x"}},
	}}
	cat := tool.NewCatalog()
	if _, err := RegisterResourceTools(cat, p); err != nil {
		t.Fatalf("RegisterResourceTools: %v", err)
	}
	for _, name := range []string{listResourcesToolName, readResourceToolName} {
		tl, ok := cat.Lookup(name)
		if !ok {
			t.Fatalf("%s not in catalog", name)
		}
		if !tl.ReadOnly() {
			t.Errorf("%s ReadOnly=false, would be dropped from the plan-mode catalog", name)
		}
	}
}
