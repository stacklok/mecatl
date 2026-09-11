package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	sub := filepath.Join(dir, name)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, SkillFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeAsset(t *testing.T, dir, skill, logicalName, content string) {
	t.Helper()
	p := filepath.Join(dir, skill, filepath.FromSlash(logicalName))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const validSkill = `---
name: commit-style
description: How to write conventional commit messages for this repo.
---

# Commit style

Use the Conventional Commits format: type(scope): subject.
Wrap the body at 72 columns.
`

func call(t *testing.T, m map[string]any) session.ToolCall {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return session.NewToolCall("id-skill", ToolName, raw)
}

func exec(t *testing.T, tl tool.Tool, in session.ToolCall) session.ToolResult {
	t.Helper()
	res, err := tl.Execute(context.Background(), in, tool.Environment{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return res
}

const expectedPreamble = `Activate a skill or read one of its bundled textual assets through its logical name.

Skills are curated, reusable playbooks. Only each skill's name and one-line description are shown below. Call with {name} to load its instructions and logical asset inventory. If those instructions need an asset, call again with {name, asset}. Assets are disclosed one at a time; this tool is not a general file browser.

Arguments:
- name (required): the exact name of one of the skills listed below.
- asset (optional): one logical asset name from that skill's activation result.

Available skills:`

func TestFSSkillLogicalActivationGolden(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "full", "---\nname: full\ndescription: every block\ncompatibility: \"mecatl >= 0.1\"\nallowed-tools: \"Shell Read\"\n---\nFull body.\n")
	writeAsset(t, dir, "full", "scripts/run.sh", "#!/bin/sh\necho ok\n")
	writeAsset(t, dir, "full", "references/api.md", "API notes\n")

	src, skips, err := NewFSSource(context.Background(), DirSource{Dir: dir})
	if err != nil || len(skips) != 0 {
		t.Fatalf("NewFSSource: %v skips=%v", err, skips)
	}
	metas, _ := src.ListSkills(context.Background())
	tl := NewTool(metas, src)
	want := "Skill: full\n" +
		"Compatibility: mecatl >= 0.1\n" +
		"This skill declares allowed-tools: Shell, Read. These are the tools the skill expects to use; each call still follows normal permission rules.\n" +
		"Bundled assets (logical names; request one with this Skill tool's asset argument):\n" +
		"  - references/api.md (10 bytes)\n" +
		"  - scripts/run.sh (18 bytes)\n\n" +
		"Full body."
	res := exec(t, tl, call(t, map[string]any{"name": "full"}))
	if res.IsError || res.Content != want {
		t.Fatalf("activation output:\n got %q\nwant %q", res.Content, want)
	}
	if got, wantDesc := tl.Spec().Description, expectedPreamble+"\n- full: every block"; got != wantDesc {
		t.Errorf("description:\n got %q\nwant %q", got, wantDesc)
	}
	for _, forbidden := range []string{"Base directory", filepath.Join(dir, "full"), "absolute path", "Read tool", "via Shell"} {
		if strings.Contains(res.Content, forbidden) {
			t.Errorf("activation leaked forbidden %q: %q", forbidden, res.Content)
		}
	}

	asset := exec(t, tl, call(t, map[string]any{"name": "full", "asset": "references/api.md"}))
	if asset.IsError || asset.Content != "Skill asset: full / references/api.md\n\nAPI notes\n" {
		t.Fatalf("asset output = %#v", asset)
	}
}
