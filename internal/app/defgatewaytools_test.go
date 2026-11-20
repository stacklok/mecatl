package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/definitionloader"
)

// fakeGatewayTool is a session gateway tool of server, named as the harness
// names it.
type fakeGatewayTool struct{ server, name string }

func (f fakeGatewayTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "mcp__" + f.server + "__" + f.name}
}
func (fakeGatewayTool) ReadOnly() bool { return true }
func (fakeGatewayTool) Execute(context.Context, session.ToolCall, tool.Environment) (session.ToolResult, error) {
	return session.ToolResult{}, nil
}
func (f fakeGatewayTool) GatewayServer() string { return f.server }

// TestDefGatewayToolsAgreesWithTheBrokersCeiling is the design's cross-check
// between the harness and the broker: for each definition, the gateway tools
// the harness gives a helper running it (defGatewayTools) are exactly the
// tools the broker's loader resolves its ceiling to allow. The two read the
// same files with their own parsers, so a change to either side's rule that
// the other does not follow fails here.
func TestDefGatewayToolsAgreesWithTheBrokersCeiling(t *testing.T) {
	definitions := map[string]string{
		"one-server":     "mcpServers: [calendar]\n",
		"two-excluded":   "mcpServers: [calendar, github]\ndisallowedTools: [mcp__calendar__echo, mcp__github__merge, Write]\n",
		"inline":         "mcpServers:\n  - name: calendar\n    url: https://inline.example.com/mcp\n",
		"unrouted":       "mcpServers: [jira]\n",
		"tools-only":     "tools: [mcp__calendar__echo]\n",
		"no-servers":     "",
		"other-excluded": "mcpServers: [calendar]\ndisallowedTools: [mcp__github__read]\n",
	}
	dir := t.TempDir()
	for name, frontmatter := range definitions {
		content := "---\nname: " + name + "\ndescription: A " + name + " specialist.\n" + frontmatter + "---\nYou help.\n"
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	servers := []string{"calendar", "github"}
	sessionTools := []tool.Tool{
		fakeGatewayTool{"calendar", "echo"}, fakeGatewayTool{"calendar", "lookup"},
		fakeGatewayTool{"github", "merge"}, fakeGatewayTool{"github", "read"},
	}

	records, _, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}, Servers: servers})
	if err != nil {
		t.Fatal(err)
	}
	ceilings := map[string]identitybroker.Ceiling{}
	for _, r := range records {
		if r.Ref.Source == identitybroker.SourceGlobal {
			ceilings[r.Ref.Name] = r.Ceiling
		}
	}
	discovered, skipped, err := agents.DirSource{Dir: dir}.Agents(t.Context())
	if err != nil || len(skipped) != 0 {
		t.Fatalf("harness discovery: %v, skipped %v", err, skipped)
	}
	if len(discovered) != len(definitions) || len(ceilings) != len(definitions) {
		t.Fatalf("the harness found %d definitions and the broker %d, want %d each", len(discovered), len(ceilings), len(definitions))
	}
	for _, d := range discovered {
		var harness []string
		for _, gt := range defGatewayTools(d.Def, sessionTools) {
			harness = append(harness, gt.Spec().Name)
		}
		var broker []string
		for _, grant := range ceilings[d.Def.Name].Servers() {
			for _, st := range sessionTools {
				ft := st.(fakeGatewayTool)
				if ft.server == grant.Server && !slices.Contains(grant.ExceptTools, ft.name) {
					broker = append(broker, ft.Spec().Name)
				}
			}
		}
		slices.Sort(harness)
		slices.Sort(broker)
		if !slices.Equal(harness, broker) {
			t.Errorf("%s: the harness gives %v, the broker allows %v", d.Def.Name, harness, broker)
		}
	}
}
