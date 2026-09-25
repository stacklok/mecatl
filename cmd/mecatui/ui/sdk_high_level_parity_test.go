package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"
)

type sdkBoundaryCategory string

const (
	sdkBacked       sdkBoundaryCategory = "sdk-backed"
	applicationOnly sdkBoundaryCategory = "application/presentation-only"
	operatorOnly    sdkBoundaryCategory = "operator/configuration-only"
	debugOnly       sdkBoundaryCategory = "debug-only"
)

type sdkBoundaryRow struct {
	category               sdkBoundaryCategory
	sdkOperation           string
	additionalSDKOperation string
	applicationDetail      string
	rationale              string
}

// This test-only inventory classifies the reusable outcome of each TUI builtin.
// Its SDK names describe a separate client; the TUI never imports the SDK.
var sdkBoundaryBuiltins = map[string]sdkBoundaryRow{
	"clear":   {category: sdkBacked, sdkOperation: "Session.clear"},
	"title":   {category: sdkBacked, sdkOperation: "Session.rename"},
	"session": {category: sdkBacked, sdkOperation: "Session.snapshot"},
	"retry":   {category: sdkBacked, sdkOperation: "Session.retry"},
	"compact": {category: sdkBacked, sdkOperation: "Session.compact"},
	"mcp": {
		category:               sdkBacked,
		sdkOperation:           "Client.mcp.listSources",
		additionalSDKOperation: "Session.listMcpConnectors",
		applicationDetail:      "The TUI chooses source listing in direct mode or connector inventory in broker mode and renders the result locally.",
	},
	"mcp-refresh": {
		category:               sdkBacked,
		sdkOperation:           "Client.mcp.refresh",
		additionalSDKOperation: "Session.connectWorkspaceServices",
		applicationDetail:      "The TUI chooses direct source refresh or broker workspace enrollment from server capabilities.",
	},
	"agents":      {category: sdkBacked, sdkOperation: "Client.agents.list"},
	"team":        {category: sdkBacked, sdkOperation: "Team.list"},
	"skills":      {category: sdkBacked, sdkOperation: "Client.skills.list"},
	"soul":        {category: sdkBacked, sdkOperation: "Client.soul.get"},
	"usermodel":   {category: sdkBacked, sdkOperation: "Client.userModel.get"},
	"reflections": {category: sdkBacked, sdkOperation: "Client.learningProposals.list"},
	"reflect":     {category: sdkBacked, sdkOperation: "Client.reflection.reflect"},
	"dream":       {category: sdkBacked, sdkOperation: "Client.dreamPlans.generate"},
	"models":      {category: sdkBacked, sdkOperation: "Client.models.list"},
	"effort":      {category: sdkBacked, sdkOperation: "Client.sessions.fork"},
	"worktrees": {
		category:               sdkBacked,
		sdkOperation:           "Session.clear",
		additionalSDKOperation: "Client.worktrees.list",
		applicationDetail:      "The TUI picks a sibling worktree, then passes its selector to Session.clear for the successor handoff.",
	},
	"schedule":             {category: sdkBacked, sdkOperation: "Client.schedules.list"},
	"sessions":             {category: sdkBacked, sdkOperation: "Client.sessions.list"},
	"tools-connect":        {category: sdkBacked, sdkOperation: "Session.connectWorkspaceServices"},
	"tools-cancel":         {category: sdkBacked, sdkOperation: "Session.cancelWorkspaceEnrollment"},
	"posture":              {category: sdkBacked, sdkOperation: "Client.server.compatibility"},
	"help":                 {category: applicationOnly, rationale: "The help overlay describes local keys and panels."},
	"quit":                 {category: applicationOnly, rationale: "Exiting the terminal application has no server outcome."},
	"diagnostics":          {category: applicationOnly, rationale: "The TUI assembles a client-state report and its own diagnostic prompt."},
	"connect":              {category: operatorOnly, rationale: "Saved targets, login, and credential handling belong to the operator client."},
	"learning":             {category: operatorOnly, rationale: "This changes the TUI's local learning-mode setting."},
	"learning-sensitivity": {category: operatorOnly, rationale: "This changes the TUI's local sensitivity setting."},
	"debug-ask":            {category: debugOnly, rationale: "Synthetic permission asks exist only for TUI debugging."},
}

func builtinNameExpression(expr ast.Expr) (string, bool) {
	switch value := expr.(type) {
	case *ast.BasicLit:
		if value.Kind != token.STRING {
			return "", false
		}
		name, err := strconv.Unquote(value.Value)
		return name, err == nil
	case *ast.Ident:
		if value.Name == "connectCommand" {
			return connectCommand, true
		}
	}
	return "", false
}

func builtinLiteralName(lit *ast.CompositeLit) (string, bool) {
	for _, element := range lit.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := field.Key.(*ast.Ident)
		if !ok || key.Name != "name" {
			continue
		}
		return builtinNameExpression(field.Value)
	}
	return "", false
}

func declaredBuiltinNames(t *testing.T) []string {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "builtins.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	add := func(lit *ast.CompositeLit) {
		if len(lit.Elts) == 0 { // The zero-value return in debugBuiltin is not a declaration.
			return
		}
		name, ok := builtinLiteralName(lit)
		if !ok {
			t.Fatalf("builtin declaration at %s has no resolvable name", fileSet.Position(lit.Pos()))
		}
		names = append(names, name)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		lit, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		switch kind := lit.Type.(type) {
		case *ast.Ident:
			if kind.Name == "builtin" {
				add(lit)
			}
		case *ast.ArrayType:
			element, ok := kind.Elt.(*ast.Ident)
			if !ok || element.Name != "builtin" {
				break
			}
			for _, entry := range lit.Elts {
				if declaration, ok := entry.(*ast.CompositeLit); ok && declaration.Type == nil {
					add(declaration)
				}
			}
		}
		return true
	})
	return names
}

func TestSDKHighLevelParity_Scenario2_AllBuiltinDeclarationsClassified(t *testing.T) {
	declared := declaredBuiltinNames(t)
	seen := make(map[string]bool, len(declared))
	for _, name := range declared {
		if seen[name] {
			t.Errorf("builtin %q is declared twice", name)
		}
		seen[name] = true
		if _, ok := sdkBoundaryBuiltins[name]; !ok {
			t.Errorf("builtin %q has no SDK boundary classification", name)
		}
	}
	for name, row := range sdkBoundaryBuiltins {
		if !seen[name] {
			t.Errorf("classification %q has no builtin declaration", name)
		}
		switch row.category {
		case sdkBacked:
			if row.sdkOperation == "" || row.rationale != "" {
				t.Errorf("SDK-backed builtin %q needs an operation and no non-SDK rationale", name)
			}
			if (row.additionalSDKOperation == "") != (row.applicationDetail == "") {
				t.Errorf("mixed SDK-backed builtin %q needs both an additional operation and application detail", name)
			}
		case applicationOnly, operatorOnly, debugOnly:
			if row.rationale == "" || row.sdkOperation != "" || row.additionalSDKOperation != "" || row.applicationDetail != "" {
				t.Errorf("non-SDK builtin %q needs a rationale and no SDK operation", name)
			}
		default:
			t.Errorf("builtin %q has unknown category %q", name, row.category)
		}
	}
}

func TestSDKHighLevelParity_Scenario2_BuiltinDispatchUnchanged(t *testing.T) {
	// These declared commands are absent from the separate known-name registry.
	// The coverage audit must not silently change that dispatch policy.
	var absent []string
	for _, name := range declaredBuiltinNames(t) {
		if !isKnownBuiltinName(name) {
			absent = append(absent, name)
		}
	}
	slices.Sort(absent)
	want := []string{"connect", "dream", "mcp-refresh", "reflect", "reflections", "tools-cancel", "tools-connect"}
	if !slices.Equal(absent, want) {
		t.Fatalf("known-name registry drift: absent = %v, want %v", absent, want)
	}
}
