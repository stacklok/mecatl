package skillfs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// sampleSkills is a small fixed set used across the tool tests.
func sampleSkills() []Skill {
	return []Skill{
		{Name: "review", Description: "Run a structured code review.", Body: "Look for correctness, then style."},
		{Name: "commit-style", Description: "Write conventional commits.", Body: "type(scope): subject\nWrap at 72."},
	}
}

// call builds a ToolCall with JSON args marshalled from m.
func call(t *testing.T, m map[string]any) session.ToolCall {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return session.NewToolCall("id-skill", ToolName, raw)
}

// exec runs the tool and fails on a harness-level error.
func exec(t *testing.T, tl tool.Tool, in session.ToolCall) session.ToolResult {
	t.Helper()
	res, err := tl.Execute(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("Execute: unexpected harness error: %v", err)
	}
	return res
}

func TestToolSpecEnumeratesSkills(t *testing.T) {
	tl := newToolOver(t, sampleSkills())
	spec := tl.Spec()
	if spec.Name != ToolName {
		t.Fatalf("Spec().Name = %q, want %q", spec.Name, ToolName)
	}
	// The always-in-context metadata: every skill's name + one-line description
	// must appear in the description.
	for _, s := range sampleSkills() {
		if !strings.Contains(spec.Description, s.Name) {
			t.Errorf("description missing skill name %q", s.Name)
		}
		if !strings.Contains(spec.Description, s.Description) {
			t.Errorf("description missing skill one-liner %q", s.Description)
		}
	}
	// Bodies must NOT be in the description (that is the load-on-activation layer).
	if strings.Contains(spec.Description, "type(scope): subject") {
		t.Error("description leaked a skill body; bodies must load only on activation")
	}
	// Listing must be sorted (commit-style before review) for cache stability.
	if strings.Index(spec.Description, "commit-style") > strings.Index(spec.Description, "review") {
		t.Error("skills not listed in sorted order")
	}
	// The schema must be valid JSON.
	var js any
	if err := json.Unmarshal(spec.Schema, &js); err != nil {
		t.Errorf("schema invalid JSON: %v", err)
	}
}

func TestToolExecuteReturnsBody(t *testing.T) {
	tl := newToolOver(t, sampleSkills())
	res := exec(t, tl, call(t, map[string]any{"name": "review"}))
	if res.IsError {
		t.Fatalf("Execute errored: %s", res.Content)
	}
	// The result is a small header (skill name; base directory when the skill has
	// a source path — these fixtures have none) followed by the full body.
	if !strings.HasPrefix(res.Content, "Skill: review\n") {
		t.Errorf("Execute result should start with the skill header, got %q", res.Content)
	}
	if !strings.HasSuffix(res.Content, "\n\nLook for correctness, then style.") {
		t.Errorf("Execute result should end with the full body after a blank line, got %q", res.Content)
	}
	if strings.Contains(res.Content, "Base directory:") {
		t.Errorf("a skill with no source path must not advertise a base directory, got %q", res.Content)
	}
	if strings.Contains(res.Content, "Compatibility:") {
		t.Errorf("a skill with no compatibility must not surface a compatibility note, got %q", res.Content)
	}
}

// TestToolExecuteSurfacesCompatibilityNote pins the advisory `compatibility`
// frontmatter field surfacing on activation (issue #419): a skill with a
// non-empty Compatibility renders a "Compatibility:" line in the activation
// output, between the base-directory block and the body. It is advisory, never
// a gate.
func TestToolExecuteSurfacesCompatibilityNote(t *testing.T) {
	tl := newToolOver(t, []Skill{
		{Name: "compat", Description: "a skill with compatibility", Body: "BODY", Compatibility: "mecatl >= 0.1"},
	})
	res := exec(t, tl, call(t, map[string]any{"name": "compat"}))
	if res.IsError {
		t.Fatalf("Execute errored: %s", res.Content)
	}
	if !strings.Contains(res.Content, "Compatibility: mecatl >= 0.1\n") {
		t.Errorf("activation output missing the Compatibility note, got %q", res.Content)
	}
	// The note sits BEFORE the blank-line + body separator.
	if !strings.HasSuffix(res.Content, "\n\nBODY") {
		t.Errorf("activation output should end with the body after the note + blank line, got %q", res.Content)
	}
}

// TestExecuteSurfacesAllowedToolsAdvisory pins the advisory `allowed-tools`
// frontmatter field (agentskills.io, Experimental; issue #419): a skill with a
// non-empty AllowedTools renders an advisory note naming them — which EXPLICITLY
// states that each call still follows normal permission rules, so the model
// does NOT infer pre-approval. It is ADVISORY ONLY — never a permission grant. A
// skill WITHOUT the field renders no note (byte-identical to before, preserving
// the activation golden).
func TestExecuteSurfacesAllowedToolsAdvisory(t *testing.T) {
	t.Run("skill with allowed-tools renders the advisory note", func(t *testing.T) {
		tl := newToolOver(t, []Skill{
			{Name: "tooling", Description: "a skill with allowed-tools", Body: "BODY", AllowedTools: []string{"Bash", "Read", "Grep"}},
		})
		res := exec(t, tl, call(t, map[string]any{"name": "tooling"}))
		if res.IsError {
			t.Fatalf("Execute errored: %s", res.Content)
		}
		want := "This skill declares allowed-tools: Bash, Read, Grep. These are the tools the skill expects to use; each call still follows normal permission rules.\n"
		if !strings.Contains(res.Content, want) {
			t.Errorf("activation output missing the allowed-tools advisory note, got %q", res.Content)
		}
		// The note sits BEFORE the blank-line + body separator.
		if !strings.HasSuffix(res.Content, "\n\nBODY") {
			t.Errorf("activation output should end with the body after the note + blank line, got %q", res.Content)
		}
	})

	t.Run("skill without allowed-tools renders no note (byte-identical)", func(t *testing.T) {
		tl := newToolOver(t, []Skill{
			{Name: "plain", Description: "a skill without allowed-tools", Body: "BODY"},
		})
		res := exec(t, tl, call(t, map[string]any{"name": "plain"}))
		if res.IsError {
			t.Fatalf("Execute errored: %s", res.Content)
		}
		if strings.Contains(res.Content, "allowed-tools") {
			t.Errorf("a skill without allowed-tools must NOT render the note (byte-identical), got %q", res.Content)
		}
		// The plain rendering is exactly the header + blank + body.
		want := "Skill: plain\n\nBODY"
		if res.Content != want {
			t.Errorf("plain activation drifted from the byte-identical rendering:\n got %q\nwant %q", res.Content, want)
		}
	})
}

// TestToolExecuteRendersBaseDirectory pins the runtime-discoverability header for
// a skill that has a source path: the result must carry the skill's CANONICAL
// base directory (the directory holding SKILL.md) and the bundled-files guidance,
// so the model learns the absolute path the workspace read-root carve-out serves
// (a user-scope skill lives outside the workspace; without the path the model
// guesses and the workspace refuses the guess).
func TestToolExecuteRendersBaseDirectory(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "with-files", "---\nname: with-files\ndescription: has bundled files\n---\nUse scripts/run.sh.\n")
	discovered, skips, err := DirSource{Dir: dir}.Skills(context.Background())
	if err != nil || len(skips) != 0 || len(discovered) != 1 {
		t.Fatalf("discover: %v skips=%v n=%d", err, skips, len(discovered))
	}
	tl := newToolOver(t, discovered)
	res := exec(t, tl, call(t, map[string]any{"name": "with-files"}))
	if res.IsError {
		t.Fatalf("Execute errored: %s", res.Content)
	}
	// The advertised dir must be the CANONICAL (abs + symlink-evaluated) form —
	// the exact key the osfs read-root allowlist matches on (t.TempDir on macOS
	// and symlinked-home setups otherwise diverge from the discovery path).
	canon, err := resolveRoot(filepath.Join(dir, "with-files"))
	if err != nil {
		t.Fatalf("resolve %q: %v", dir, err)
	}
	want := "Base directory: " + canon + "\n"
	if !strings.Contains(res.Content, want) {
		t.Errorf("Execute result missing %q, got %q", want, res.Content)
	}
	if !strings.Contains(res.Content, "read them with the Read tool by absolute path") {
		t.Errorf("Execute result missing the bundled-files guidance, got %q", res.Content)
	}
	if !strings.HasSuffix(res.Content, "Use scripts/run.sh.") {
		t.Errorf("Execute result should end with the full body, got %q", res.Content)
	}
}

// TestToolExecuteBaseDirectoryResolvesSymlinkAlias pins the CANONICALIZATION
// contract of the base-directory header — the exact bug skillBaseDir's doc
// comment names (a symlinked path prefix, e.g. /home → /var/home). t.TempDir is
// already canonical on most CI hosts, so the divergence is MANUFACTURED here: the
// skill is discovered through a symlink ALIAS of its real directory, and the
// header must advertise the RESOLVED real path — the key the osfs read-root
// allowlist matches on — never the raw alias (printing raw filepath.Dir would
// advertise a path the workspace then refuses).
func TestToolExecuteBaseDirectoryResolvesSymlinkAlias(t *testing.T) {
	realDir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeSkill(t, alias, "aliased", "---\nname: aliased\ndescription: via alias\n---\nBODY\n")

	discovered, skips, err := DirSource{Dir: alias}.Skills(context.Background())
	if err != nil || len(skips) != 0 || len(discovered) != 1 {
		t.Fatalf("discover: %v skips=%v n=%d", err, skips, len(discovered))
	}
	rawDir := filepath.Clean(filepath.Dir(discovered[0].Path))
	resolved, err := resolveRoot(filepath.Join(realDir, "aliased"))
	if err != nil {
		t.Fatalf("resolve real dir: %v", err)
	}
	// Precondition: the discovery path really crosses the symlink, so the raw and
	// resolved forms diverge — otherwise this test cannot detect a regression to
	// the unresolved form.
	if rawDir == resolved {
		t.Fatalf("test setup did not produce a divergent alias: raw %q == resolved %q", rawDir, resolved)
	}

	tl := newToolOver(t, discovered)
	res := exec(t, tl, call(t, map[string]any{"name": "aliased"}))
	if res.IsError {
		t.Fatalf("Execute errored: %s", res.Content)
	}
	if want := "Base directory: " + resolved + "\n"; !strings.Contains(res.Content, want) {
		t.Errorf("header must advertise the RESOLVED dir (%q), got %q", want, res.Content)
	}
	if strings.Contains(res.Content, "Base directory: "+rawDir+"\n") {
		t.Errorf("header advertised the raw symlink-alias dir %q — the allowlist would refuse it; got %q", rawDir, res.Content)
	}
}

func TestToolExecuteUnknownSkill(t *testing.T) {
	tl := newToolOver(t, sampleSkills())
	res := exec(t, tl, call(t, map[string]any{"name": "nope"}))
	if !res.IsError {
		t.Fatal("activating an unknown skill must be an error result")
	}
	// The error must list the available names so the model can recover.
	if !strings.Contains(res.Content, "review") || !strings.Contains(res.Content, "commit-style") {
		t.Errorf("unknown-skill error should list available skills, got %q", res.Content)
	}
}

func TestToolExecuteMissingName(t *testing.T) {
	tl := newToolOver(t, sampleSkills())
	res := exec(t, tl, call(t, map[string]any{}))
	if !res.IsError {
		t.Error("missing name must be an error result")
	}
}

func TestToolExecuteMalformedArgs(t *testing.T) {
	tl := newToolOver(t, sampleSkills())
	res := exec(t, tl, session.NewToolCall("id", ToolName, json.RawMessage("{not json")))
	if !res.IsError {
		t.Error("malformed args must be an error result")
	}
}

func TestToolReadOnlyAvailableInPlanMode(t *testing.T) {
	tl := newToolOver(t, sampleSkills())
	if !tl.ReadOnly() {
		t.Fatal("Skill tool must be read-only")
	}
	// A read-only tool must survive the plan-mode catalog filter.
	cat := tool.NewCatalog()
	cat.MustRegister(tl)
	specs := cat.Specs(session.ModePlan)
	found := false
	for _, s := range specs {
		if s.Name == ToolName {
			found = true
		}
	}
	if !found {
		t.Error("Skill tool must be available in plan mode (it is read-only)")
	}
}

func TestNewToolEmptyDescription(t *testing.T) {
	tl := newToolOver(t, nil)
	if !strings.Contains(tl.Spec().Description, "none configured") {
		t.Errorf("empty skill set should note none configured, got %q", tl.Spec().Description)
	}
	res := exec(t, tl, call(t, map[string]any{"name": "x"}))
	if !res.IsError || !strings.Contains(res.Content, "no skills are available") {
		t.Errorf("empty tool should report no skills, got %q", res.Content)
	}
}

func TestRegisterOptIn(t *testing.T) {
	// Empty dir: nothing registered, no error.
	cat := tool.NewCatalog()
	discovered, _, err := Register(cat, "")
	if err != nil {
		t.Fatalf("Register empty dir: %v", err)
	}
	if len(discovered) != 0 {
		t.Errorf("empty dir should discover nothing, got %d", len(discovered))
	}
	if _, ok := cat.Lookup(ToolName); ok {
		t.Error("Skill tool must NOT be registered when no skills are discovered")
	}

	// A dir with one valid skill: the tool is registered.
	dir := t.TempDir()
	writeSkill(t, dir, "commit-style", validSkill)
	cat2 := tool.NewCatalog()
	discovered, skips, err := Register(cat2, dir)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	if len(discovered) != 1 {
		t.Fatalf("expected 1 skill discovered, got %d", len(discovered))
	}
	if _, ok := cat2.Lookup(ToolName); !ok {
		t.Error("Skill tool should be registered when a valid skill exists")
	}
}

func TestRegisterSourceOptInAndMerge(t *testing.T) {
	// An empty MultiSource registers nothing (preserved opt-in behaviour).
	cat := tool.NewCatalog()
	discovered, _, err := RegisterSource(context.Background(), cat, NewMultiSource())
	if err != nil {
		t.Fatalf("RegisterSource empty: %v", err)
	}
	if len(discovered) != 0 {
		t.Errorf("empty source should discover nothing, got %d", len(discovered))
	}
	if _, ok := cat.Lookup(ToolName); ok {
		t.Error("Skill tool must NOT be registered when no skills are discovered")
	}

	// Two filesystem sources with a colliding name: the EARLIER source wins, and a
	// single Skill tool is registered over the merged set.
	high := t.TempDir()
	low := t.TempDir()
	writeSkill(t, high, "dup", "---\nname: dup\ndescription: high wins\n---\nhigh body\n")
	writeSkill(t, high, "only-high", "---\nname: only-high\ndescription: h\n---\nbody\n")
	writeSkill(t, low, "dup", "---\nname: dup\ndescription: low loses\n---\nlow body\n")
	writeSkill(t, low, "only-low", "---\nname: only-low\ndescription: l\n---\nbody\n")

	cat2 := tool.NewCatalog()
	src := NewMultiSource(DirSource{Dir: high, Label: "explicit"}, DirSource{Dir: low, Label: "project"})
	discovered, skips, err := RegisterSource(context.Background(), cat2, src)
	if err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	if len(discovered) != 3 {
		t.Fatalf("merged set should hold 3 skills (dup once), got %d: %+v", len(discovered), discovered)
	}
	// The kept "dup" must be the high-precedence one.
	var dup Skill
	for _, s := range discovered {
		if s.Name == "dup" {
			dup = s
		}
	}
	if dup.Description != "high wins" {
		t.Errorf("colliding skill should keep the higher-precedence source, got %q", dup.Description)
	}
	if !hasReasonContaining(skips, "shadowed") {
		t.Errorf("a shadow notice was expected for the dropped low-precedence dup, got %v", skips)
	}
	if _, ok := cat2.Lookup(ToolName); !ok {
		t.Error("Skill tool should be registered when the merged set is non-empty")
	}
}

// TestExecuteEnumeratesBundledAssets pins the agentskills.io "should enumerate
// bundled scripts/resources but must not eagerly read them" contract: a skill
// WITH assets renders a "Bundled files:" block listing each asset's logical
// name (sorted, indented); a skill with NO assets renders no such block
// (byte-identical to the pre-enumeration rendering).
func TestExecuteEnumeratesBundledAssets(t *testing.T) {
	t.Run("skill with assets renders the enumeration block", func(t *testing.T) {
		dir := t.TempDir()
		writeSkill(t, dir, "deploy", "---\nname: deploy\ndescription: deploy the app\n---\nRun the deploy script.\n")
		writeAsset(t, dir, "deploy", "scripts/run.sh", "#!/bin/sh\ndeploy\n")
		writeAsset(t, dir, "deploy", "references/api.md", "API notes\n")

		discovered, skips, err := DirSource{Dir: dir}.Skills(context.Background())
		if err != nil || len(skips) != 0 || len(discovered) != 1 {
			t.Fatalf("discover: %v skips=%v n=%d", err, skips, len(discovered))
		}
		tl := newToolOver(t, discovered)
		res := exec(t, tl, call(t, map[string]any{"name": "deploy"}))
		if res.IsError {
			t.Fatalf("Execute errored: %s", res.Content)
		}
		// The enumeration block lists both logical names, sorted.
		wantBlock := "Bundled files:\n" +
			"  - references/api.md\n" +
			"  - scripts/run.sh\n"
		if !strings.Contains(res.Content, wantBlock) {
			t.Errorf("activation output missing the bundled-files enumeration block %q, got %q", wantBlock, res.Content)
		}
		// The block sits AFTER the base-directory guidance and BEFORE the blank
		// line + body.
		guidanceIdx := strings.Index(res.Content, "read them with the Read tool by absolute path")
		enumerIdx := strings.Index(res.Content, "Bundled files:")
		bodyIdx := strings.Index(res.Content, "\n\nRun the deploy script.")
		if guidanceIdx < 0 || enumerIdx < 0 || bodyIdx < 0 {
			t.Fatalf("activation output missing expected sections, got %q", res.Content)
		}
		if guidanceIdx >= enumerIdx || enumerIdx >= bodyIdx {
			t.Errorf("enumeration block out of order: guidance@%d enumeration@%d body@%d", guidanceIdx, enumerIdx, bodyIdx)
		}
		// Names must NOT include the SKILL.md body file itself.
		if strings.Contains(res.Content, "SKILL.md") {
			t.Errorf("enumeration must not list SKILL.md (it is the body, not a payload), got %q", res.Content)
		}
	})

	t.Run("skill without assets renders no enumeration block", func(t *testing.T) {
		tl := newToolOver(t, []Skill{{Name: "plain", Description: "no assets", Body: "BODY"}})
		res := exec(t, tl, call(t, map[string]any{"name": "plain"}))
		if res.IsError {
			t.Fatalf("Execute errored: %s", res.Content)
		}
		if strings.Contains(res.Content, "Bundled files:") {
			t.Errorf("a skill without assets must NOT render the enumeration block, got %q", res.Content)
		}
		// Byte-identical to the pre-enumeration rendering.
		want := "Skill: plain\n\nBODY"
		if res.Content != want {
			t.Errorf("asset-less activation drifted from the byte-identical rendering:\n got %q\nwant %q", res.Content, want)
		}
	})
}

func TestToolExecuteBoundsBundledAssetInventory(t *testing.T) {
	assets := make([]tool.SkillAsset, 1_000)
	for i := range assets {
		assets[i] = tool.SkillAsset{Name: fmt.Sprintf("references/%04d.md", i)}
	}
	tl := NewTool(
		[]tool.SkillMeta{{Name: "asset-heavy", Description: "many assets"}},
		assetInventoryActivator{body: "Follow these instructions.", assets: assets},
	)
	res := exec(t, tl, call(t, map[string]any{"name": "asset-heavy"}))
	if res.IsError {
		t.Fatalf("Execute errored: %s", res.Content)
	}
	if !strings.Contains(res.Content, "Follow these instructions.") {
		t.Errorf("asset inventory consumed the activation result before the body: %q", res.Content)
	}
	if !strings.Contains(res.Content, "bundled files omitted.") {
		t.Errorf("large asset inventory must disclose omitted entries, got %q", res.Content)
	}
}

type assetInventoryActivator struct {
	body    string
	baseDir string
	assets  []tool.SkillAsset
}

func (a assetInventoryActivator) Activate(context.Context, string) (Activation, error) {
	return Activation{Body: a.body, BaseDir: a.baseDir, Assets: a.assets}, nil
}

func TestToolBodyTruncated(t *testing.T) {
	big := strings.Repeat("x", 30_000)
	tl := newToolOver(t, []Skill{{Name: "big", Description: "huge", Body: big}})
	res := exec(t, tl, call(t, map[string]any{"name": "big"}))
	if res.IsError {
		t.Fatalf("Execute errored: %s", res.Content)
	}
	if len(res.Content) >= len(big) {
		t.Error("oversized skill body should be truncated to the shared output cap")
	}
	if !strings.Contains(res.Content, "truncated") {
		t.Error("truncated body should carry a truncation marker")
	}
}

func TestToolExecuteActivationHeaderOrder(t *testing.T) {
	tl := NewTool([]tool.SkillMeta{{
		Name: "ordered", Description: "ordered", Compatibility: "mecatl >= 0.1", AllowedTools: []string{"Read", "Bash"},
	}}, assetInventoryActivator{
		body:    "BODY",
		baseDir: "/opt/skills/ordered",
		assets:  []tool.SkillAsset{{Name: "references/api.md"}},
	})
	res := exec(t, tl, call(t, map[string]any{"name": "ordered"}))
	if res.IsError {
		t.Fatalf("Execute errored: %s", res.Content)
	}
	want := "Skill: ordered\n" +
		"Base directory: /opt/skills/ordered\n" +
		"Bundled files (references/, scripts/, assets/) live under the base directory; read them with the Read tool by absolute path, and run bundled scripts via Bash with their absolute path.\n" +
		"Compatibility: mecatl >= 0.1\n" +
		"This skill declares allowed-tools: Read, Bash. These are the tools the skill expects to use; each call still follows normal permission rules.\n" +
		"Bundled files:\n  - references/api.md\n\nBODY"
	if res.Content != want {
		t.Errorf("activation header order drifted:\n got %q\nwant %q", res.Content, want)
	}
}
