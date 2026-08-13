package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
)

// ToolSearchName is the catalog name of the built-in progressive-disclosure
// hydration tool.
const ToolSearchName = "ToolSearch"

// toolSearchSchema is the JSON schema for ToolSearch arguments: an optional
// substring query matched against tool names and descriptions. An empty query
// returns the full specs of every available tool.
const toolSearchSchema = `{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "Case-insensitive substring matched against tool names and descriptions. Empty returns all tools."
    }
  }
}`

const toolSearchDescription = "Search the tool catalog and return the FULL specification (name, " +
	"description, JSON schema) of tools matching a query. Use this to hydrate a " +
	"tool whose full schema is not yet in context (only its name/summary is " +
	"advertised) before you call it. Read-only."

// Search is the built-in ToolSearch hydration tool for progressive tool
// disclosure (pattern 9). Given a substring query it returns the FULL ToolSpec of
// each matching tool in the catalog, so the model can pull a tool's schema into
// context on demand after seeing only its advertised metadata. It is read-only
// and excludes itself from results. It is registered under ToolSearchName.
type Search struct {
	cat *Catalog
}

// NewToolSearch constructs a ToolSearch backed by cat. The composition root (or
// NewEngine, when progressive disclosure is enabled) registers it into the same
// catalog it queries.
func NewToolSearch(cat *Catalog) *Search {
	return &Search{cat: cat}
}

// Spec returns the model-facing specification of the ToolSearch tool.
func (*Search) Spec() ToolSpec {
	return ToolSpec{
		Name:        ToolSearchName,
		Description: toolSearchDescription,
		Schema:      json.RawMessage(toolSearchSchema),
	}
}

// ReadOnly reports that ToolSearch only reads the catalog, so it may run in
// parallel with other read-only tools.
func (*Search) ReadOnly() bool { return true }

// toolSearchArgs is the decoded ToolSearch argument payload.
type toolSearchArgs struct {
	Query string `json:"query"`
}

// matchedSpec is one hydrated result returned by ToolSearch.
type matchedSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

// Execute returns the full specs of catalog tools whose name or description
// contains the query (case-insensitive substring). An empty query returns all
// tools. ToolSearch never returns itself. Results are ordered by name.
func (s *Search) Execute(_ context.Context, in session.ToolCall, _ Environment) (session.ToolResult, error) {
	var args toolSearchArgs
	if len(in.Args) > 0 {
		if err := json.Unmarshal(in.Args, &args); err != nil {
			return session.NewToolError(in.ID, fmt.Sprintf("ToolSearch: invalid args: %v", err)), nil
		}
	}
	q := strings.ToLower(strings.TrimSpace(args.Query))

	var matches []matchedSpec
	for _, t := range s.cat.Tools() {
		spec := t.Spec()
		if spec.Name == ToolSearchName {
			continue
		}
		if q != "" &&
			!strings.Contains(strings.ToLower(spec.Name), q) &&
			!strings.Contains(strings.ToLower(spec.Description), q) {
			continue
		}
		matches = append(matches, matchedSpec(spec))
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Name < matches[j].Name })

	body, err := json.Marshal(matches)
	if err != nil {
		return session.NewToolError(in.ID, fmt.Sprintf("ToolSearch: marshal results: %v", err)), nil
	}
	return session.NewToolResult(in.ID, string(body)), nil
}

// Compile-time assertion that ToolSearch satisfies the Tool contract.
var _ Tool = (*Search)(nil)
