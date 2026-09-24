package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func TestMecatuiTypedScrollbackModel_Scenario2_ProductionUsesTypedTransitions(t *testing.T) {
	files := parseUIProductionFiles(t)
	legacyWrappers := map[string]bool{
		"syncSnapshot": true, "syncBlock": true, "syncCall": true,
		"changedFiles": true, "changedFilesAppendixID": true,
		"subagentBlock": true, "teamBlock": true, "walkBlocks": true,
		"setSubagentStart": true, "setSubagentRoutingDecision": true,
		"addSubagentTool": true, "setSubagentEnd": true,
		"setTeamStart": true, "addTeamMember": true, "setTeamEnd": true,
		"setTeamTasks": true, "setTeamFindings": true,
	}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			decl, ok := n.(*ast.FuncDecl)
			if ok && decl.Recv != nil && legacyWrappers[decl.Name.Name] {
				t.Errorf("production compatibility projection %s still exists", decl.Name.Name)
			}
			return true
		})
	}

	conversationType := findStruct(t, files, "conversation")
	for _, field := range conversationType.Fields.List {
		for _, name := range field.Names {
			if name.Name == "blocks" {
				t.Fatal("conversation still retains the legacy mutable blocks transcript")
			}
		}
	}
}

func TestMecatuiTypedScrollbackModel_Scenario3_ScrollbackBoundaryIsLogicalOnly(t *testing.T) {
	files := parseUIProductionFiles(t)
	forbiddenBlockInputs := map[string]bool{
		"prepareStructuredBlock": true, "prepareUserBlock": true,
		"prepareNoticeBlock": true, "prepareHookBlock": true,
		"prepareTurnStatBlock": true, "prepareErrorBlock": true,
		"preparePermanentErrorBlock": true, "prepareDeliveryBlock": true,
	}
	foundFrame, foundCacheSeam, foundReasoningSeam := false, false, false
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && forbiddenBlockInputs[fn.Name.Name] {
				t.Errorf("ordinary renderer still accepts ui.block through %s", fn.Name.Name)
			}
			if ok && fn.Name.Name == "renderCachedSnapshot" {
				foundCacheSeam = true
				ast.Inspect(fn.Type.Params, func(n ast.Node) bool {
					if star, ok := n.(*ast.StarExpr); ok {
						if ident, ok := star.X.(*ast.Ident); ok && ident.Name == "block" {
							t.Error("cache seam accepts broad ui.block")
						}
					}
					return true
				})
			}
			if ok && fn.Name.Name == "renderReasoningSnapshot" {
				foundReasoningSeam = true
			}
			if !ok || fn.Name.Name != "renderConversationFrame" {
				continue
			}
			if len(fn.Type.Params.List) < 1 {
				t.Fatal("renderConversationFrame has no typed scrollback input")
			}
			star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
			if !ok {
				t.Fatalf("renderConversationFrame scrollback input = %T, want pointer", fn.Type.Params.List[0].Type)
			}
			sel, ok := star.X.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Conversation" {
				t.Fatalf("renderConversationFrame input is not *scrollback.Conversation: %#v", fn.Type.Params.List[0].Type)
			}
			foundFrame = true
		}
	}
	if !foundFrame {
		t.Fatal("renderConversationFrame not found")
	}
	if !foundCacheSeam {
		t.Error("renderer has no identity/revision cache-output seam")
	}
	if !foundReasoningSeam {
		t.Error("assistant reasoning has no direct typed-snapshot rendering seam")
	}
}

func TestMecatuiTypedScrollbackModel_Scenario3_OrdinaryToolsRenderFromTypedSnapshots(t *testing.T) {
	files := parseUIProductionFiles(t)
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name.Name == "toolBlockFromSnapshot" {
				t.Fatal("ordinary tool snapshots are still converted through ui.block")
			}
		}
	}
}

func TestMecatuiTypedScrollbackModel_Scenario3_OrdinaryToolSnapshotAdapterPreservesPresentation(t *testing.T) {
	artifact := client.ContentBlock{Kind: client.ContentBlockResourceLink, Name: "report", URL: "https://example.test/report"}
	cases := []struct {
		name, tool, args, result string
		isError                  bool
		artifacts                []client.ContentBlock
	}{
		{name: "error artifact", tool: "WebFetch", args: `{"url":"https://example.test"}`, result: "request failed", isError: true, artifacts: []client.ContentBlock{artifact}},
		{name: "edit diff", tool: "Edit", args: `{"path":"a.txt","old_string":"old\nline","new_string":"new\nline"}`},
	}
	for _, width := range []int{1, 48} {
		for _, tc := range cases {
			for _, expand := range []bool{false, true} {
				t.Run(tc.name, func(t *testing.T) {
					var c conversation
					c.addTool("call", tc.tool, tc.args)
					if tc.result != "" && !c.resolveTool("call", tc.result, tc.isError, tc.artifacts...) {
						t.Fatal("resolve typed tool")
					}
					snapshot := c.scrollback.SnapshotAt(0)
					payload := snapshot.Payload.(scrollback.ToolCardSnapshot)
					typed := newTestRenderer()
					typed.setWidth(width)
					legacy := newTestRenderer()
					legacy.setWidth(width)
					want := legacy.renderBlock(0, &block{id: uint64(snapshot.ID), rev: rendererRevision(snapshot.Revision), kind: blockTool, toolID: payload.Call.ID, toolName: payload.Call.Name, toolArgs: payload.Call.Arguments, resolved: payload.Resolved, resultBody: payload.Result.Body, resultError: payload.Result.IsError, resultBlocks: contentBlocks(payload.Result.Artifacts)}, expand)
					got := typed.renderSnapshot(0, snapshot, expand)
					if got, want := stripANSIstr(got), stripANSIstr(want); got != want {
						t.Fatalf("typed snapshot adapter changed tool presentation\n got: %q\nwant: %q", got, want)
					}
				})
			}
		}
	}
}

func TestMecatuiTypedScrollbackModel_Scenario3_OrdinaryCardsUseSealedSnapshotAdapters(t *testing.T) {
	files := parseUIProductionFiles(t)
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name.Name == "blockFromSnapshot" {
				t.Fatal("generic scrollback snapshot conversion still exists")
			}
		}
	}
}

var osReadDir = os.ReadDir

func parseUIProductionFiles(t *testing.T) []*ast.File {
	t.Helper()
	entries, err := osReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) < 3 || name[len(name)-3:] != ".go" || len(name) >= 8 && name[len(name)-8:] == "_test.go" {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
	}
	return files
}

func findStruct(t *testing.T, files []*ast.File, name string) *ast.StructType {
	t.Helper()
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != name {
					continue
				}
				st, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					t.Fatalf("%s is not a struct", name)
				}
				return st
			}
		}
	}
	t.Fatalf("struct %s not found", name)
	return nil
}

func TestRenderPassCarriesCachedFrameMetadata(t *testing.T) {
	var c conversation
	c.addNotice("settled")
	r := newCacheRenderer()
	r.renderConversationFrame(&c.scrollback, false)
	loads, prepares, renders := r.snapshotLoads, r.cardPrepares, r.blockRenders

	passes, _ := r.renderPasses(&c.scrollback, false)
	pass := passes[0]
	if pass.id == 0 || pass.revision != 0 || pass.kind != scrollback.KindNotice || pass.text == "" || len(pass.rows) == 0 {
		t.Fatalf("render pass omitted cache/frame metadata: %#v", pass)
	}
	if got := r.snapshotLoads - loads; got != 0 {
		t.Fatalf("settled cache hit loaded %d snapshots", got)
	}
	if got := r.cardPrepares - prepares; got != 0 {
		t.Fatalf("settled cache hit prepared %d cards", got)
	}
	if got := r.blockRenders - renders; got != 0 {
		t.Fatalf("settled cache hit rendered %d cards", got)
	}
}

func TestMecatuiTypedScrollbackModel_Scenario3_DelegationCardsUseTypedPresentationAdapters(t *testing.T) {
	files := parseUIProductionFiles(t)
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name.Name == "delegationBlockFromSnapshot" {
				t.Fatal("Subagent and Team snapshots are still converted through a generic union adapter")
			}
		}
	}
}
