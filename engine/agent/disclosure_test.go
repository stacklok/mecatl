package agent_test

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// capturingProvider records the LLMRequest of its first Stream call, then ends
// the turn immediately.
type capturingProvider struct {
	req port.LLMRequest
	n   int
}

func (*capturingProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *capturingProvider) Stream(_ context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	if p.n == 0 {
		p.req = req
	}
	p.n++
	return func(yield func(port.Chunk, error) bool) {
		yield(port.Chunk{Kind: port.ChunkText, Text: "done"}, nil)
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

// discTool is a Disclosable tool: full Spec carries a schema; Advertised is
// metadata only.
type discTool struct{ name string }

func (d discTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name:        d.name,
		Description: d.name + " full",
		Schema:      json.RawMessage(`{"type":"object","properties":{"y":{"type":"string"}}}`),
	}
}
func (d discTool) Advertised() tool.ToolSpec {
	return tool.ToolSpec{Name: d.name, Description: d.name + " full"}
}
func (discTool) ReadOnly() bool { return true }
func (discTool) Execute(context.Context, session.ToolCall, tool.Workspace) (session.ToolResult, error) {
	return session.ToolResult{}, nil
}

func specByName(specs []tool.ToolSpec, name string) (tool.ToolSpec, bool) {
	for _, s := range specs {
		if s.Name == name {
			return s, true
		}
	}
	return tool.ToolSpec{}, false
}

// TestProgressiveToolsOffSendsFullSpecs asserts the DEFAULT (ProgressiveTools
// off) sends every tool's full spec and registers no ToolSearch tool.
func TestProgressiveToolsOffSendsFullSpecs(t *testing.T) {
	prov := &capturingProvider{}
	cat := catalogWith(t, discTool{name: "Mcp"})
	e := newEngine(agent.Deps{LLM: prov, Catalog: cat}) // ProgressiveTools defaults false
	sess := newSession(t, session.Limits{})
	drain(e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "hi"}))

	if _, ok := cat.Lookup(tool.ToolSearchName); ok {
		t.Fatalf("ToolSearch must NOT be registered when ProgressiveTools is off")
	}
	mcp, ok := specByName(prov.req.Tools, "Mcp")
	if !ok {
		t.Fatalf("Mcp spec missing from request")
	}
	if len(mcp.Schema) == 0 {
		t.Fatalf("default off must send the FULL schema; got none")
	}
}

// TestProgressiveToolsOnSendsAdvertisedAndToolSearch asserts that when enabled
// the request carries advertised (schema-less) specs for disclosable tools plus
// the ToolSearch tool, and ToolSearch hydrates the full spec.
func TestProgressiveToolsOnSendsAdvertisedAndToolSearch(t *testing.T) {
	prov := &capturingProvider{}
	cat := catalogWith(t, discTool{name: "Mcp"})
	e := newEngine(agent.Deps{LLM: prov, Catalog: cat, ProgressiveTools: true})
	sess := newSession(t, session.Limits{})
	drain(e.Run(context.Background(), sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "hi"}))

	// ToolSearch must be registered and advertised.
	if _, ok := cat.Lookup(tool.ToolSearchName); !ok {
		t.Fatalf("ToolSearch must be registered when ProgressiveTools is on")
	}
	if _, ok := specByName(prov.req.Tools, tool.ToolSearchName); !ok {
		t.Fatalf("request must advertise ToolSearch")
	}
	// Disclosable tool advertised with no schema.
	mcp, ok := specByName(prov.req.Tools, "Mcp")
	if !ok {
		t.Fatalf("Mcp spec missing from request")
	}
	if len(mcp.Schema) != 0 {
		t.Fatalf("progressive disclosure must advertise Mcp without a schema; got %s", mcp.Schema)
	}

	// The model can hydrate the full Mcp spec via ToolSearch.
	ts, _ := cat.Lookup(tool.ToolSearchName)
	call := session.NewToolCall("c1", tool.ToolSearchName, json.RawMessage(`{"query":"mcp"}`))
	res, err := ts.Execute(context.Background(), call, memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("ToolSearch.Execute: %v", err)
	}
	if res.IsError || !json.Valid([]byte(res.Content)) {
		t.Fatalf("ToolSearch returned bad result: err=%v content=%s", res.IsError, res.Content)
	}
	if !strings.Contains(res.Content, "properties") {
		t.Fatalf("ToolSearch did not hydrate the full Mcp schema: %s", res.Content)
	}
}

// TestProgressiveToolsRegistrationIdempotent asserts building two engines over
// the same catalog with disclosure on does not panic on a duplicate ToolSearch.
func TestProgressiveToolsRegistrationIdempotent(t *testing.T) {
	cat := catalogWith(t, discTool{name: "Mcp"})
	_ = newEngine(agent.Deps{LLM: &capturingProvider{}, Catalog: cat, ProgressiveTools: true})
	// Second engine over the same catalog must not panic (ToolSearch already there).
	_ = newEngine(agent.Deps{LLM: &capturingProvider{}, Catalog: cat, ProgressiveTools: true})
	if _, ok := cat.Lookup(tool.ToolSearchName); !ok {
		t.Fatalf("ToolSearch should remain registered")
	}
}
