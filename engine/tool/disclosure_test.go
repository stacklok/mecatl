package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// disclosableTool is a fake Tool implementing Disclosable: its full Spec carries
// a schema while Advertised returns metadata only.
type disclosableTool struct {
	name string
}

func (d disclosableTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        d.name,
		Description: d.name + " full description\nmore detail",
		Schema:      json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`),
	}
}
func (d disclosableTool) Advertised() ToolSpec {
	return ToolSpec{Name: d.name, Description: d.name + " full description"}
}
func (disclosableTool) ReadOnly() bool { return true }
func (disclosableTool) Execute(context.Context, session.ToolCall, Environment) (session.ToolResult, error) {
	return session.ToolResult{}, nil
}

var _ Disclosable = disclosableTool{}

// TestAdvertisedSpecsDefaultsToFullForNonDisclosable verifies that a catalog of
// only non-disclosable tools yields an AdvertisedSpecs result identical to
// Specs, preserving the default (full-spec) disclosure behaviour.
func TestAdvertisedSpecsDefaultsToFullForNonDisclosable(t *testing.T) {
	c := NewCatalog()
	c.MustRegister(fakeTool{name: "Read", readOnly: true})
	c.MustRegister(fakeTool{name: "Write", readOnly: false})

	full := c.Specs(session.ModeDefault)
	adv := c.AdvertisedSpecs(session.ModeDefault)
	if len(full) != len(adv) {
		t.Fatalf("len mismatch: Specs=%d AdvertisedSpecs=%d", len(full), len(adv))
	}
	for i := range full {
		if full[i].Name != adv[i].Name || full[i].Description != adv[i].Description {
			t.Fatalf("AdvertisedSpecs[%d]=%+v, want full %+v", i, adv[i], full[i])
		}
	}
}

// TestAdvertisedSpecsUsesMetadataForDisclosable verifies that a disclosable tool
// is advertised with its metadata-only spec (no schema), while a plain tool
// keeps its full spec.
func TestAdvertisedSpecsUsesMetadataForDisclosable(t *testing.T) {
	c := NewCatalog()
	c.MustRegister(fakeTool{name: "Read", readOnly: true})
	c.MustRegister(disclosableTool{name: "Mcp"})

	adv := c.AdvertisedSpecs(session.ModeDefault)
	byName := map[string]ToolSpec{}
	for _, s := range adv {
		byName[s.Name] = s
	}
	if len(byName["Mcp"].Schema) != 0 {
		t.Fatalf("disclosable tool advertised with a schema: %s", byName["Mcp"].Schema)
	}
	// The full Spec must still carry the schema (hydrate-on-demand).
	mcp, _ := c.Lookup("Mcp")
	if len(mcp.Spec().Schema) == 0 {
		t.Fatalf("disclosable tool full Spec lost its schema")
	}
}

// TestToolSearchHydratesFullSpec verifies the ToolSearch tool returns the FULL
// spec (including schema) for a matching tool and excludes itself.
func TestToolSearchHydratesFullSpec(t *testing.T) {
	c := NewCatalog()
	c.MustRegister(disclosableTool{name: "Mcp"})
	c.MustRegister(fakeTool{name: "Read", readOnly: true})
	search := NewToolSearch(c)
	c.MustRegister(search)

	call := session.NewToolCall("c1", ToolSearchName, json.RawMessage(`{"query":"mcp"}`))
	res, err := search.Execute(context.Background(), call, testEnv())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("ToolSearch returned error: %s", res.Content)
	}
	var got []matchedSpec
	if err := json.Unmarshal([]byte(res.Content), &got); err != nil {
		t.Fatalf("unmarshal results: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d matches, want 1: %+v", len(got), got)
	}
	if got[0].Name != "Mcp" {
		t.Fatalf("matched %q, want Mcp", got[0].Name)
	}
	if len(got[0].Schema) == 0 {
		t.Fatalf("ToolSearch did not return the full schema for Mcp")
	}
}

// TestToolSearchEmptyQueryReturnsAllExceptItself verifies an empty query returns
// every tool except ToolSearch.
func TestToolSearchEmptyQueryReturnsAllExceptItself(t *testing.T) {
	c := NewCatalog()
	c.MustRegister(fakeTool{name: "Read", readOnly: true})
	c.MustRegister(fakeTool{name: "Write", readOnly: false})
	search := NewToolSearch(c)
	c.MustRegister(search)

	call := session.NewToolCall("c1", ToolSearchName, json.RawMessage(`{}`))
	res, _ := search.Execute(context.Background(), call, testEnv())
	var got []matchedSpec
	if err := json.Unmarshal([]byte(res.Content), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (Read, Write; ToolSearch excluded): %+v", len(got), got)
	}
	for _, m := range got {
		if m.Name == ToolSearchName {
			t.Fatalf("ToolSearch included itself in results")
		}
	}
}

// TestToolSearchReadOnly confirms ToolSearch is read-only so it dispatches in the
// read-parallel batch.
func TestToolSearchReadOnly(t *testing.T) {
	if !NewToolSearch(NewCatalog()).ReadOnly() {
		t.Fatalf("ToolSearch must be read-only")
	}
}
