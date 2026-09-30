package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/rulesfs"
	"github.com/stacklok/mecatl/engine/prompt"
)

func contextCLI(t *testing.T, argv ...string) (string, error) {
	t.Helper()
	res := resolveCommand(append([]string{"mecated", "context"}, argv...))
	if res.err != nil {
		return "", res.err
	}
	if !res.handled || res.run == nil {
		t.Fatal("context fell through to daemon")
	}
	var out bytes.Buffer
	err := res.run(strings.NewReader(""), &out, &bytes.Buffer{})
	return out.String(), err
}
func TestContextScan(t *testing.T) {
	dir := t.TempDir()
	for path, content := range map[string]string{"AGENTS.md": "instructions", "CLAUDE.md": "ignored", ".mecatl/rules/alpha.md": "a", ".claude/rules/alpha.md": "ignored", ".claude/rules/beta.md": "b", ".mecatl/skills/review/SKILL.md": "---\nname: review\n---\nbody", ".claude/agents/helper.md": "agent"} {
		p := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	text, err := contextCLI(t, "scan", dir, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc contextDocument
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != contextVersion || doc.Kind != "scan" || len(doc.Occurrences) != 7 || strings.Contains(text, `"file_bytes":12,"status":"instructions"`) || !strings.Contains(text, `"status":"shadowed by AGENTS.md"`) {
		t.Fatalf("bad inventory: %s", text)
	}
	if doc.Occurrences[1].Status != "shadowed by higher-precedence project rule" {
		t.Fatalf("expected shadowed rule: %+v", doc.Occurrences)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".mecatl/rules/evil.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := contextCLI(t, "scan", dir); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("symlink accepted or leaked: %v", err)
	}
}
func TestContextUserRootAndMCPSnapshot(t *testing.T) {
	project, user := t.TempDir(), t.TempDir()
	for path, body := range map[string]string{
		".mecatl/rules/shared.md":               "project",
		".config/mecatl/rules/shared.md":        "user",
		".config/mecatl/rules/user-only.md":     "user",
		".config/mecatl/skills/review/SKILL.md": "---\nname: review\n---\nbody",
		".config/mecatl/agents/helper.md":       "---\nname: helper\n---\nbody",
		".config/mecatl/soul.md":                "private",
	} {
		root := user
		if strings.HasPrefix(path, ".mecatl") {
			root = project
		}
		file := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(snapshot, []byte(`{"tools":[{"name":"mcp__unsafe/name","description":"reads files","inputSchema":{"type":"object"}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := contextCLI(t, "scan", project, "--user-root", user, "--mcp-snapshot", snapshot, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "private") || strings.Contains(output, "mcp__unsafe/name") || !strings.Contains(output, `"scope":"explicit project + user root + MCP snapshot"`) || !strings.Contains(output, `"source":"mcp-snapshot"`) || !strings.Contains(output, `"status":"snapshot candidate; runtime admission unknown"`) {
		t.Fatalf("unsafe or incomplete output: %s", output)
	}
	var doc contextDocument
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatal(err)
	}
	for _, o := range doc.Occurrences {
		if o.Source == "user:.config/mecatl/rules" && o.Name == "shared" && o.Status != "shadowed by higher-precedence project rule" {
			t.Fatalf("user rule did not lose precedence: %+v", o)
		}
	}
	if err := os.WriteFile(snapshot, []byte(`{"tools":[{"name":"password_lookup","description":"","inputSchema":{"type":"object","properties":{"token":{"type":"string"}}}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := contextCLI(t, "scan", project, "--mcp-snapshot", snapshot); err != nil {
		t.Fatalf("schema parameter named token is not a credential: %v", err)
	}
	for _, bad := range []string{
		`{"tools":[{"name":"read","description":"x","inputSchema":{},"token":"secret"}]}`,
		`{"tools":[{"name":"read","description":"x","inputSchema":{},"unknown":true}]}`,
		`{"tools":[{"name":"read","description":"x","inputSchema":[]}]}`,
	} {
		if err := os.WriteFile(snapshot, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := contextCLI(t, "scan", project, "--mcp-snapshot", snapshot); err == nil {
			t.Fatalf("accepted unsafe snapshot %s", bad)
		}
	}
}

func TestContextRuleRenderedCaps(t *testing.T) {
	dir := t.TempDir()
	rules := filepath.Join(dir, ".mecatl", "rules")
	if err := os.MkdirAll(rules, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"a.md": "---\npaths: ['**/*.go']\n---\n" + strings.Repeat("a", 15000), "b.md": strings.Repeat("b", 15000), "c.md": strings.Repeat("c", 15000), "d.md": "small", "invalid.md": "---\npaths: [broken\n---\nbody"} {
		if err := os.WriteFile(filepath.Join(rules, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	output, err := contextCLI(t, "scan", dir, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc contextDocument
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Method != "o200k_base-local-estimate" {
		t.Fatalf("unexpected method: %s", output)
	}
	want := map[string]string{"a": "eligible eager rule; trust unknown", "b": "eligible eager rule; trust unknown", "c": "omitted by eager rules cap", "d": "omitted by eager rules cap", "invalid": "invalid rule frontmatter"}
	for _, o := range doc.Occurrences {
		if o.Source != ".mecatl/rules" {
			continue
		}
		if o.Status != want[o.Name] {
			t.Errorf("%s status %q want %q", o.Name, o.Status, want[o.Name])
		}
		if o.Name == "a" && (o.Bytes == nil || *o.Bytes <= 15000 || o.EstimatedTokens == nil || *o.EstimatedTokens == 0) {
			t.Errorf("rule not estimated: %+v", o)
		}
		if (o.Name == "c" || o.Name == "d") && o.EstimatedTokens != nil {
			t.Error("omitted rule charged")
		}
	}
}

func TestContextRuleCountAndReadBudget(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		dir := t.TempDir()
		rules := filepath.Join(dir, ".mecatl", "rules")
		if err := os.MkdirAll(rules, 0700); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 34; i++ {
			if err := os.WriteFile(filepath.Join(rules, fmt.Sprintf("r%02d.md", i)), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		doc, err := scanContext(dir)
		if err != nil {
			t.Fatal(err)
		}
		count, omitted := 0, 0
		for _, o := range doc.Occurrences {
			if o.Status == "eligible eager rule; trust unknown" {
				count++
			}
			if o.Status == "omitted by eager rules cap" {
				omitted++
				if o.EstimatedTokens != nil {
					t.Fatal("omitted rule charged")
				}
			}
		}
		if count != 32 || omitted != 2 {
			t.Fatalf("shown %d omitted %d", count, omitted)
		}
	})
	t.Run("aggregate", func(t *testing.T) {
		dir := t.TempDir()
		rules := filepath.Join(dir, ".mecatl", "rules")
		if err := os.MkdirAll(rules, 0700); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 9; i++ {
			if err := os.WriteFile(filepath.Join(rules, fmt.Sprintf("r%02d.md", i)), []byte(strings.Repeat("x", contextMaxFile)), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := scanContext(dir); err == nil || !strings.Contains(err.Error(), "budget") {
			t.Fatalf("expected read budget: %v", err)
		}
	})
}
func TestContextRuleBlock(t *testing.T) {
	block, ok := contextRuleBlock("scoped", []byte("---\npaths: ['**/*.go, **/*.md', 'src/**']\n---\n content \n"))
	if !ok || !strings.Contains(block, "Applies when: **/*.go, **/*.md, src/**\ncontent\n</rule>") {
		t.Fatalf("wrong rendered globs: %q, %v", block, ok)
	}
	file := filepath.Join(t.TempDir(), "scoped.md")
	if err := os.WriteFile(file, []byte("---\npaths: ['**/*.go, **/*.md', 'src/**']\n---\n content \n"), 0600); err != nil {
		t.Fatal(err)
	}
	src, skips, err := rulesfs.NewFSSource(context.Background(), rulesfs.DirSource{Dir: filepath.Dir(file)})
	if err != nil || len(skips) != 0 {
		t.Fatalf("reference source: %v %+v", err, skips)
	}
	messages, err := (prompt.RulesAssembler{Src: src}).Assemble(context.Background())
	if err != nil || len(messages) != 1 || messages[0].Text != prompt.RulesHeader()+block {
		t.Fatalf("render differs from reference: %v %#v", err, messages)
	}
	empty, ok := contextRuleBlock("empty", []byte("  \n"))
	if !ok || !strings.Contains(empty, "Applies when: (always)\n\n</rule>") {
		t.Fatalf("empty body semantics: %q, %v", empty, ok)
	}
	oversized, ok := contextRuleBlock("large", []byte(strings.Repeat("é", contextMaxFile/2)))
	if !ok || len(oversized) < 20000 || len(oversized) > 21000 || !strings.Contains(oversized, "…\n</rule>") {
		t.Fatalf("rune cap: %d %v", len(oversized), ok)
	}
	largeFile := filepath.Join(t.TempDir(), "large.md")
	largeBody := strings.Repeat("é", 15000)
	if err := os.WriteFile(largeFile, []byte(largeBody), 0600); err != nil {
		t.Fatal(err)
	}
	largeSource, _, err := rulesfs.NewFSSource(context.Background(), rulesfs.DirSource{Dir: filepath.Dir(largeFile)})
	if err != nil {
		t.Fatal(err)
	}
	largeMessages, err := (prompt.RulesAssembler{Src: largeSource}).Assemble(context.Background())
	largeBlock, valid := contextRuleBlock("large", []byte(largeBody))
	if err != nil || !valid || len(largeMessages) != 1 || largeMessages[0].Text != prompt.RulesHeader()+largeBlock {
		t.Fatalf("truncated rule differs from reference: %v", err)
	}
	for _, paths := range []string{"null", "TRUE", "[2, 'one two']"} {
		t.Run(paths, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "r.md")
			body := "---\npaths: " + paths + "\n---\nbody"
			if err := os.WriteFile(file, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			source, _, err := rulesfs.NewFSSource(context.Background(), rulesfs.DirSource{Dir: filepath.Dir(file)})
			if err != nil {
				t.Fatal(err)
			}
			messages, err := (prompt.RulesAssembler{Src: source}).Assemble(context.Background())
			block, ok := contextRuleBlock("r", []byte(body))
			if err != nil || !ok || len(messages) != 1 || messages[0].Text != prompt.RulesHeader()+block {
				t.Fatalf("paths %q differ: %v %#v vs %q", paths, err, messages, block)
			}
		})
	}
	if _, ok := contextRuleBlock("bad", []byte("---\npaths: [unterminated\n---\nx")); ok {
		t.Fatal("invalid frontmatter accepted")
	}
	dir := t.TempDir()
	for path, body := range map[string]string{".mecatl/rules/dup.md": "---\npaths: [unterminated\n---\nx", ".claude/rules/dup.md": "fallback"} {
		file := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := scanContext(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range doc.Occurrences {
		if o.Source == ".claude/rules" && o.Status != "eligible eager rule; trust unknown" {
			t.Fatalf("invalid higher precedence claimed name: %+v", o)
		}
	}
}

func TestContextReportAndDiff(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	for p, body := range map[string]string{a: `{"message_count":1,"message_bytes":24,"prompt":[{"kind":"system","provenance":"stable","bytes":8}]}`, b: `{"message_count":1,"message_bytes":24,"token_estimate_method":"local_estimate","estimated_request_tokens":12,"prompt":[{"kind":"system","provenance":"stable","bytes":8,"estimated_tokens":3}],"advertised_tools":[{"name":"read","name_bytes":4,"doc_bytes":0,"schema_bytes":0,"estimated_tokens":1}]}`} {
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old, err := contextCLI(t, "report", "--input", a, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(old, `"method":"unknown"`) || strings.Contains(old, `"estimated_tokens":0`) {
		t.Fatalf("old manifest not unknown: %s", old)
	}
	newer, err := contextCLI(t, "report", "--input", b, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(newer, `"name":"read"`) {
		t.Fatalf("missing tool: %s", newer)
	}
	if _, err := contextCLI(t, "diff", "--before", a, "--after", b); err == nil {
		t.Fatal("mixed methods accepted")
	}
	if err := os.WriteFile(a, []byte(newer), 0600); err != nil {
		t.Fatal(err)
	}
	diff, err := contextCLI(t, "diff", "--before", a, "--after", b, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, `"changes":[]`) {
		t.Fatalf("unexpected diff: %s", diff)
	}
}
func TestContextInstructionFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(" \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("fallback"), 0600); err != nil {
		t.Fatal(err)
	}
	text, err := contextCLI(t, "scan", dir, "--format", "json")
	if err != nil || strings.Contains(text, `"name":"AGENTS.md"`) || !strings.Contains(text, `"name":"CLAUDE.md"`) {
		t.Fatalf("fallback: %v %s", err, text)
	}
}

func TestContextInstructionSymlinkFallbackNotRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("AGENTS.md", filepath.Join(dir, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	text, err := contextCLI(t, "scan", dir, "--format", "json")
	if err != nil || !strings.Contains(text, `"name":"AGENTS.md"`) {
		t.Fatalf("winning instructions should be scannable with an unused symlink fallback: %v %s", err, text)
	}
}

func TestContextScanDiffAndStdin(t *testing.T) {
	dir := t.TempDir()
	before := filepath.Join(t.TempDir(), "before.json")
	after := filepath.Join(t.TempDir(), "after.json")
	for _, p := range []string{before, after} {
		text, err := contextCLI(t, "scan", dir, "--format", "json")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if p == before {
			if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("new instructions"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	diff, err := contextCLI(t, "diff", "--before", before, "--after", after, "--format", "json")
	if err != nil || !strings.Contains(diff, `"name":"AGENTS.md"`) {
		t.Fatalf("scan diff %v: %s", err, diff)
	}
	textDiff, err := contextCLI(t, "diff", "--before", before, "--after", after)
	if err != nil || !strings.Contains(textDiff, "project-instructions/AGENTS.md:") || !strings.Contains(textDiff, "absent ->") {
		t.Fatalf("text diff %v: %s", err, textDiff)
	}
	res := resolveCommand([]string{"mecated", "context", "report", "--input", "-", "--format", "json"})
	var out bytes.Buffer
	if err := res.run(strings.NewReader(`{"message_count":0,"prompt":[]}`), &out, &bytes.Buffer{}); err != nil || !strings.Contains(out.String(), `"kind":"report"`) {
		t.Fatalf("stdin report %v: %s", err, out.String())
	}
}

func TestContextScanArguments(t *testing.T) {
	project := t.TempDir()
	for _, args := range [][]string{
		{"--format", "json", project},
		{project, "--format", "json"},
		{project, "--format=json"},
	} {
		output, err := contextCLI(t, append([]string{"scan"}, args...)...)
		if err != nil || !strings.Contains(output, `"kind":"scan"`) {
			t.Fatalf("scan %q: %v %s", args, err, output)
		}
	}

	snapshot := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(snapshot, []byte(`{"tools":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := contextCLI(t, "scan", project, "--user-root="+t.TempDir(), "--mcp-snapshot="+snapshot); err != nil {
		t.Fatalf("equals flags: %v", err)
	}

	parent := t.TempDir()
	dashProject := filepath.Join(parent, "-project")
	if err := os.Mkdir(dashProject, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(parent)
	if _, err := contextCLI(t, "scan", "--format", "json", "--", "-project"); err != nil {
		t.Fatalf("dash-leading project: %v", err)
	}
}

func TestContextErrors(t *testing.T) {
	for _, argv := range [][]string{{"scan"}, {"scan", ".", "."}} {
		if _, err := contextCLI(t, argv...); err == nil || !strings.Contains(err.Error(), "mecated context scan .") {
			t.Fatalf("missing scan example for %q: %v", argv, err)
		}
	}
	for _, argv := range [][]string{{"report"}, {"diff"}, {"scan", "/missing", "--format", "xml"}, {"scan", "--unknown"}, {"scan", "--project", "/missing"}} {
		if _, err := contextCLI(t, argv...); err == nil {
			t.Fatalf("accepted %q", argv)
		}
	}
	for _, argv := range [][]string{{"--help"}, {"scan", "--help"}, {"report", "--help"}, {"diff", "--help"}} {
		if out, err := contextCLI(t, argv...); err != nil || !strings.Contains(out, "Usage:") {
			t.Fatalf("help %q: %v %q", argv, err, out)
		}
	}
	dir := t.TempDir()
	for _, data := range []string{`{"prompt":[{"kind":"system","provenance":"\u001b[31m","bytes":1}]}`, `{"message_count":-1}`, `{"version":"wrong","kind":"report"}`, `{"message_count":0,"unknown":"secret"}`, `{"message_count":0,"prompt":[],"message_count":100}`, `{"view":"manifest","available":true,"rows":[{"message_count":0,"message_count":1}]}`, strings.Repeat(" ", contextMaxInput+1)} {
		p := filepath.Join(dir, "input")
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := contextCLI(t, "report", "--input", p); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("accepted unsafe input or leaked content: %v", err)
		}
	}
	t.Setenv("HOME", t.TempDir())
	if _, err := contextCLI(t, "scan"); err == nil {
		t.Fatal("ambient home used")
	}
}
