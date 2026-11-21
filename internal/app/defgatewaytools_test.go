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
// between the harness and the broker. Both read the same definition files
// (the harness through agentfs, the broker through its loader, both on
// agentdef) and apply their own rule: the harness picks a helper's gateway
// tools (defGatewayTools), the broker resolves a ceiling (resolveCeiling)
// and checks calls against it (Ceiling.Allows). For each definition, the
// tools the harness gives are exactly the tools the broker allows, so a
// change to either rule that the other does not follow fails here.
//
// It also covers the parent: a helper's gateway tools come from its root's,
// which today is always the built-in main agent (named helpers start no
// helpers, and named roots get no gateway tools, #2938), so its effective
// ceiling is its definition's intersected with system/main's. And it covers
// the built-in helpers, whose catalogs hold every gateway tool.
func TestDefGatewayToolsAgreesWithTheBrokersCeiling(t *testing.T) {
	definitions := map[string]string{
		"one-server":     "mcpServers: [calendar]\n",
		"two-excluded":   "mcpServers: [calendar, github]\ndisallowedTools: [mcp__calendar__echo, mcp__github__merge, Write]\n",
		"inline":         "mcpServers:\n  - name: calendar\n    url: https://inline.example.com/mcp\n",
		"unrouted":       "mcpServers: [jira]\n",
		"tools-only":     "tools: [mcp__calendar__echo]\n",
		"no-servers":     "",
		"other-excluded": "mcpServers: [calendar]\ndisallowedTools: [mcp__github__read]\n",
		"exact-names":    "mcpServers: [calendar, github]\ndisallowedTools: [mcp__github__pr.merge, mcp__GitHub__read, mcp__calendar__Echo, \"mcp__calendar__ lookup\"]\n",
		"ambiguous":      "mcpServers: [git]\ndisallowedTools: [mcp__git___x]\n",
	}
	dir := t.TempDir()
	for name, frontmatter := range definitions {
		content := "---\nname: " + name + "\ndescription: A " + name + " specialist.\n" + frontmatter + "---\nYou help.\n"
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	servers := []string{"calendar", "github", "git"}
	sessionTools := []tool.Tool{
		fakeGatewayTool{"calendar", "echo"}, fakeGatewayTool{"calendar", "lookup"}, fakeGatewayTool{"calendar", "Echo"},
		fakeGatewayTool{"github", "merge"}, fakeGatewayTool{"github", "read"}, fakeGatewayTool{"github", "pr.merge"},
		fakeGatewayTool{"git", "_x"}, fakeGatewayTool{"git", "y"},
	}
	// allowed returns the names of the session tools every ceiling allows.
	allowed := func(ceilings ...identitybroker.Ceiling) []string {
		var out []string
		for _, st := range sessionTools {
			ft := st.(fakeGatewayTool)
			ok := true
			for _, c := range ceilings {
				ok = ok && c.Allows(ft.server, ft.name)
			}
			if ok {
				out = append(out, ft.Spec().Name)
			}
		}
		slices.Sort(out)
		return out
	}
	names := func(tools []tool.Tool) []string {
		var out []string
		for _, gt := range tools {
			out = append(out, gt.Spec().Name)
		}
		slices.Sort(out)
		return out
	}

	records, _, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}, Servers: servers})
	if err != nil {
		t.Fatal(err)
	}
	ceilings := map[string]identitybroker.Ceiling{}
	builtins := map[string]identitybroker.Ceiling{}
	for _, r := range records {
		switch r.Ref.Source {
		case identitybroker.SourceGlobal:
			ceilings[r.Ref.Name] = r.Ceiling
		case identitybroker.SourceSystem:
			builtins[r.Ref.Name] = r.Ceiling
		}
	}
	discovered, skipped, err := agents.DirSource{Dir: dir}.Agents(t.Context())
	if err != nil || len(skipped) != 0 {
		t.Fatalf("harness discovery: %v, skipped %v", err, skipped)
	}
	if len(discovered) != len(definitions) || len(ceilings) != len(definitions) {
		t.Fatalf("the harness found %d definitions and the broker %d, want %d each", len(discovered), len(ceilings), len(definitions))
	}
	main, ok := builtins[identitybroker.BuiltinMain]
	if !ok {
		t.Fatal("the broker loaded no system/main")
	}
	for _, d := range discovered {
		harness := names(defGatewayTools(d.Def, sessionTools))
		if broker := allowed(ceilings[d.Def.Name]); !slices.Equal(harness, broker) {
			t.Errorf("%s: the harness gives %v, the broker allows %v", d.Def.Name, harness, broker)
		}
		if effective := allowed(ceilings[d.Def.Name], main); !slices.Equal(harness, effective) {
			t.Errorf("%s under the main agent: the harness gives %v, the broker's effective ceiling allows %v", d.Def.Name, harness, effective)
		}
	}

	all := names(sessionTools)
	catalogs := map[string]*tool.Catalog{
		"read-only explorer": readOnlyExplorerCatalog(nil, sessionTools),
		"writable explorer":  writableExplorerCatalog(nil, "test catalog", sessionTools),
	}
	for name, ceiling := range builtins {
		if got := allowed(ceiling); !slices.Equal(got, all) {
			t.Errorf("%s: the broker allows %v, want every gateway tool %v", name, got, all)
		}
	}
	for name, cat := range catalogs {
		for _, tn := range all {
			if _, ok := cat.Lookup(tn); !ok {
				t.Errorf("%s lacks gateway tool %s, which the built-in ceiling allows", name, tn)
			}
		}
	}
}
