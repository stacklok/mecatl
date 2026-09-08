package mcp

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

// Tool names for the resource meta-tools. These are HARNESS-LEVEL meta-tools
// (they let the model browse/read MCP resources), not proxies for a specific
// remote tool, so they use plain Claude-Code-style names rather than the
// mcp__<server>__<tool> namespace used for proxied remote tools. The plain names
// are fixed and known not to collide with the built-in catalog (Read, Edit,
// Write, Grep, Glob, Bash, Subagent, Skill, ...).
const (
	listResourcesToolName = "ListMcpResources"
	readResourceToolName  = "ReadMcpResource"
)

// listResourcesTool lets the model enumerate the resources advertised by the
// connected MCP servers (optionally filtered to one server). It is read-only:
// listing mutates nothing, so it runs in parallel with other reads and survives
// the plan-mode catalog filter.
type listResourcesTool struct {
	provider Provider
}

var _ tool.Tool = listResourcesTool{}

func (listResourcesTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: listResourcesToolName,
		Description: "List the resources exposed by connected MCP servers. " +
			"Resources are server-provided documents/data addressable by URI. " +
			"Returns each resource's server, URI, name, MIME type, and description. " +
			"Use ReadMcpResource to fetch a resource's contents.",
		Schema: toolkit.Schema(`{
  "type": "object",
  "properties": {
    "server": {"type": "string", "description": "Optional: limit the listing to this MCP server name. Omit to list resources from all servers."}
  }
}`),
	}
}

func (listResourcesTool) ReadOnly() bool { return true }

type listResourcesArgs struct {
	Server string `json:"server"`
}

func (t listResourcesTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return session.ToolResult{}, err
	}
	var args listResourcesArgs
	if msg, ok := toolkit.ParseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	server := strings.TrimSpace(args.Server)
	resources, err := t.provider.ListResources(ctx, server)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return session.ToolResult{}, ctxErr
		}
		return session.NewToolError(in.ID, fmt.Sprintf("list MCP resources failed: %v", err)), nil
	}
	return session.NewToolResult(in.ID, toolkit.Truncate(renderResourceList(resources), toolkit.MaxOutputBytes)), nil
}

// renderResourceList formats a resource slice as a stable, model-readable list.
func renderResourceList(resources []Resource) string {
	if len(resources) == 0 {
		return "No MCP resources available."
	}
	sorted := append([]Resource(nil), resources...)
	slices.SortFunc(sorted, func(a, b Resource) int {
		return cmp.Or(
			cmp.Compare(a.Server, b.Server),
			cmp.Compare(a.URI, b.URI),
		)
	})
	var b strings.Builder
	for i, r := range sorted {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "- [%s] %s", r.Server, r.URI)
		if r.Name != "" {
			fmt.Fprintf(&b, " (%s)", r.Name)
		}
		if r.MIMEType != "" {
			fmt.Fprintf(&b, " [%s]", r.MIMEType)
		}
		if r.Description != "" {
			fmt.Fprintf(&b, ": %s", r.Description)
		}
	}
	return b.String()
}

// readResourceTool lets the model fetch a single MCP resource's contents by
// server + URI. Binary resources are summarized rather than dumped (see
// flattenResourceContents). It is read-only.
type readResourceTool struct {
	provider Provider
}

var _ tool.Tool = readResourceTool{}

func (readResourceTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: readResourceToolName,
		Description: "Read the contents of a single MCP resource by its server and URI " +
			"(discover them with ListMcpResources). Text resources are returned verbatim; " +
			"binary resources are summarized (type and size) rather than dumped.",
		Schema: toolkit.Schema(`{
  "type": "object",
  "properties": {
    "server": {"type": "string", "description": "The MCP server name that owns the resource."},
    "uri": {"type": "string", "description": "The URI of the resource to read."}
  },
  "required": ["server", "uri"]
}`),
	}
}

func (readResourceTool) ReadOnly() bool { return true }

type readResourceArgs struct {
	Server string `json:"server"`
	URI    string `json:"uri"`
}

func (t readResourceTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return session.ToolResult{}, err
	}
	var args readResourceArgs
	if msg, ok := toolkit.ParseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	server := strings.TrimSpace(args.Server)
	uri := strings.TrimSpace(args.URI)
	if server == "" {
		return session.NewToolError(in.ID, `the "server" argument is required`), nil
	}
	if uri == "" {
		return session.NewToolError(in.ID, `the "uri" argument is required`), nil
	}
	contents, err := t.provider.ReadResource(ctx, server, uri)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return session.ToolResult{}, ctxErr
		}
		// A reconnect failure (errReconnectFailed — the server could not be
		// re-established) or a connection-drop after the one reconnect attempt
		// is surfaced as the clear "unavailable after reconnect" message, never
		// the raw transport string — mirroring remoteTool.Execute (tool.go).
		// ReadResource routes through Server.readResource → withSession, so the
		// error can be either. Unknown server / unknown URI / other faults stay
		// verbatim so the model can self-correct, never a hard Go error.
		if message := unavailableMessage(err, "read MCP resource failed", server); message != "" {
			return session.NewToolError(in.ID, message), nil
		}
		return session.NewToolError(in.ID, fmt.Sprintf("read MCP resource failed: %v", err)), nil
	}
	return session.NewToolResult(in.ID, contents.Text), nil
}

// RegisterResourceTools registers the ListMcpResources and ReadMcpResource
// meta-tools into cat, built over the given Provider — but ONLY when at least one
// connected server exposes at least one resource. This mirrors the skills
// "register only when non-empty" gating: there is no value in advertising
// resource tools when no resources exist. It returns whether the tools were
// registered and the first registration error (if any).
func RegisterResourceTools(cat *tool.Catalog, p Provider) (bool, error) {
	if p == nil {
		return false, nil
	}
	resources, err := p.ListResources(context.Background(), "")
	if err != nil {
		return false, fmt.Errorf("mcp: probing resources for tool registration: %w", err)
	}
	if len(resources) == 0 {
		return false, nil
	}
	if rerr := cat.Register(listResourcesTool{provider: p}); rerr != nil {
		return false, fmt.Errorf("mcp: register %q: %w", listResourcesToolName, rerr)
	}
	if rerr := cat.Register(readResourceTool{provider: p}); rerr != nil {
		return true, fmt.Errorf("mcp: register %q: %w", readResourceToolName, rerr)
	}
	return true, nil
}
