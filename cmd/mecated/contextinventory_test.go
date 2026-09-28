package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestContextReportInventory(t *testing.T) {
	project, user := t.TempDir(), t.TempDir()
	for path, body := range map[string]string{
		filepath.Join(project, "AGENTS.md"):                          "private instructions",
		filepath.Join(user, ".config/mecatl/skills/review/SKILL.md"): "---\nname: review\ndescription: helpful\n---\nprivate skill body",
		filepath.Join(user, ".config/mecatl/agents/helper.md"):       "deferred private body",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	scan, err := contextCLI(t, "scan", project, "--user-root", user, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	inventory := filepath.Join(dir, "scan.json")
	if err := os.WriteFile(inventory, []byte(scan), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := session.RequestManifestPayload{MessageCount: 1, Prompt: []session.RequestPromptComponent{{Kind: "instruction", Provenance: "rules", Bytes: 31, Rules: []session.RequestRuleMetric{{Name: "observed", Origin: "project", RenderedBytes: 19, EstimatedTokens: 4}}}}}
	input := filepath.Join(dir, "event.json")
	writeContextJSONFixture(t, input, session.Event{Type: session.EvRequestManifest, RequestManifest: &manifest})
	output, err := contextCLI(t, "report", "--input", input, "--inventory", inventory, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc contextDocument
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.InventoryScope != "explicit project + user root" || doc.InventoryMethod != "o200k_base-local-estimate" || doc.InventoryCoverage == "" || len(doc.Candidates) < 3 {
		t.Fatalf("missing inventory metadata: %s", output)
	}
	foundSkill, foundAgent := false, false
	for _, o := range doc.Candidates {
		if o.Source == "user:.config/mecatl/skills" && o.Name == "review" {
			foundSkill = true
			if o.FileBytes == nil || o.EstimatedTokens == nil || !strings.Contains(o.Status, "admission unknown") {
				t.Fatalf("skill inventory: %+v", o)
			}
		}
		if o.Source == "user:.config/mecatl/agents" && o.Name == "helper" {
			foundAgent = true
			if o.FileBytes == nil || o.EstimatedTokens != nil || o.Bytes != nil {
				t.Fatalf("deferred body charged: %+v", o)
			}
		}
	}
	if !foundSkill || !foundAgent {
		t.Fatalf("missing user candidates: %s", output)
	}
	for _, o := range doc.Occurrences {
		if strings.HasPrefix(o.Source, "user:") || o.Source == "project-instructions" {
			t.Fatalf("candidate in observed request: %+v", o)
		}
	}
	if !strings.Contains(output, `"source":"rule-project"`) || strings.Contains(output, "private") || strings.Contains(output, "deferred private body") {
		t.Fatalf("invented load or leaked content: %s", output)
	}
	debugRowBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var debugRow map[string]json.RawMessage
	if err := json.Unmarshal(debugRowBytes, &debugRow); err != nil {
		t.Fatal(err)
	}
	for from, to := range map[string]string{"tool_names": "tools", "tool_decisions": "decisions", "prompt": "components"} {
		debugRow[to] = debugRow[from]
		delete(debugRow, from)
	}
	debugFile := filepath.Join(dir, "debugger.json")
	writeContextJSONFixture(t, debugFile, map[string]any{"view": "manifest", "available": true, "authoritative": true, "source": "request.manifest events", "projection_complete": true, "scan_complete": false, "retention_complete": false, "offset": 0, "limit": 1, "rows": []any{debugRow}})
	debugReport, err := contextCLI(t, "report", "--input", debugFile, "--row", "0", "--inventory", inventory, "--format", "json")
	if err != nil || !strings.Contains(debugReport, `"scope":"imported projection row"`) || !strings.Contains(debugReport, `"source":"user:.config/mecatl/agents"`) || !strings.Contains(debugReport, "scan=false") {
		t.Fatalf("debugger+inventory: %v %s", err, debugReport)
	}
	text, err := contextCLI(t, "report", "--input", input, "--inventory", inventory, "--format", "text")
	if err != nil || !strings.Contains(text, "Observed request occurrences") || !strings.Contains(text, "Unverified candidate inventory") || !strings.Contains(text, "user:.config/mecatl/agents/helper") || !strings.Contains(text, "unknown tokens") || !strings.Contains(text, "NOT observed, admitted, or loaded") || strings.Contains(text, "private") {
		t.Fatalf("combined text: %v %s", err, text)
	}
	plain, err := contextCLI(t, "report", "--input", input, "--format", "json")
	if err != nil || strings.Contains(plain, "inventory_scope") || strings.Contains(plain, "candidates") {
		t.Fatalf("default report changed: %v %s", err, plain)
	}
	combined := filepath.Join(dir, "combined.json")
	if err := os.WriteFile(combined, []byte(output), 0600); err != nil {
		t.Fatal(err)
	}
	again, err := contextCLI(t, "report", "--input", combined, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var reimported contextDocument
	if err := json.Unmarshal([]byte(again), &reimported); err != nil {
		t.Fatal(err)
	}
	if len(reimported.Candidates) != len(doc.Candidates) || reimported.InventoryScope != doc.InventoryScope {
		t.Fatalf("round trip lost candidates: %s", again)
	}
	if _, err := contextCLI(t, "diff", "--before", input, "--after", combined); err == nil {
		t.Fatal("combined and plain reports compared")
	}
	second := filepath.Join(dir, "second.json")
	writeContextJSONFixture(t, second, doc)
	if diff, err := contextCLI(t, "diff", "--before", combined, "--after", second, "--format", "json"); err != nil || !strings.Contains(diff, `"changes":[]`) || !strings.Contains(diff, `"inventory_scope":"explicit project + user root"`) {
		t.Fatalf("combined diff: %v %s", err, diff)
	}
	doc.Candidates = doc.Candidates[:len(doc.Candidates)-1]
	writeContextJSONFixture(t, second, doc)
	diff, err := contextCLI(t, "diff", "--before", combined, "--after", second, "--format", "json")
	if err != nil || !strings.Contains(diff, `"candidate_changes":[`) || !strings.Contains(diff, `"changes":[]`) {
		t.Fatalf("candidate delta: %v %s", err, diff)
	}
}

type contextPanicReader struct{}

func (contextPanicReader) Read([]byte) (int, error) { panic("stdin read before collision rejection") }

func TestContextInventoryRejections(t *testing.T) {
	project, user := t.TempDir(), t.TempDir()
	dir := t.TempDir()
	input := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(input, []byte(`{"message_count":0,"prompt":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	scan, err := contextCLI(t, "scan", project, "--user-root", user, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	inventory := filepath.Join(dir, "scan.json")
	if err := os.WriteFile(inventory, []byte(scan), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := contextCLI(t, "report", "--input", "-", "--inventory", "-"); err == nil {
		t.Fatal("stdin collision accepted")
	}
	res := resolveCommand([]string{"mecated", "context", "report", "--input", "-", "--inventory", "-"})
	if res.err != nil {
		t.Fatal(res.err)
	}
	if err := res.run(contextPanicReader{}, io.Discard, io.Discard); err == nil {
		t.Fatal("collision accepted before stdin read")
	}
	if _, err := contextCLI(t, "report", "--input", input, "--inventory", "-"); err == nil {
		t.Fatal("inventory stdin accepted")
	}
	for _, bad := range []string{`{"message_count":0,"prompt":[]}`, `{"version":"mecatl.context/invalid","kind":"scan"}`, `{"version":"mecatl.context/v0alpha1","kind":"scan","method":"o200k_base-local-estimate","scope":"explicit project","coverage":"explicit scan candidates; trust, runtime admission, and activation unknown","occurrences":[{"source":"user:.config/mecatl/agents","name":"\u001b[31m","file_bytes":3,"status":"snapshot candidate; runtime admission unknown"}]}`} {
		if err := os.WriteFile(inventory, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := contextCLI(t, "report", "--input", input, "--inventory", inventory); err == nil {
			t.Fatalf("accepted bad inventory: %s", bad)
		}
	}
	if err := os.WriteFile(inventory, []byte(scan), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(inventory, link); err != nil {
		t.Fatal(err)
	}
	if _, err := contextCLI(t, "report", "--input", input, "--inventory", link); err == nil {
		t.Fatal("symlink accepted")
	}
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := contextCLI(t, "report", "--input", input, "--inventory", fifo); err == nil {
		t.Fatal("FIFO accepted")
	}
	if err := os.WriteFile(inventory, []byte(strings.Repeat(" ", contextMaxInput+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := contextCLI(t, "report", "--input", input, "--inventory", inventory); err == nil {
		t.Fatal("oversized inventory accepted")
	}
	if err := os.WriteFile(inventory, []byte(scan), 0600); err != nil {
		t.Fatal(err)
	}
	combined, err := contextCLI(t, "report", "--input", input, "--inventory", inventory, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc contextDocument
	if err := json.Unmarshal([]byte(combined), &doc); err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(dir, "bad.json")
	doc.InventoryScope = "untrusted injected \x1b[31m"
	writeContextJSONFixture(t, badPath, doc)
	if _, err := contextCLI(t, "report", "--input", badPath); err == nil {
		t.Fatal("untrusted scope reimported")
	}
	doc.InventoryScope = "explicit project + user root"
	doc.Candidates = append(doc.Candidates, contextOccurrence{Source: "user:.config/mecatl/agents", Name: "\x1b[31m", FileBytes: contextPtr(1), Status: "snapshot candidate; runtime admission unknown"})
	writeContextJSONFixture(t, badPath, doc)
	if _, err := contextCLI(t, "report", "--input", badPath); err == nil {
		t.Fatal("hostile candidate reimported")
	}
	projectOnly, err := contextCLI(t, "scan", project, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inventory, []byte(projectOnly), 0600); err != nil {
		t.Fatal(err)
	}
	other, err := contextCLI(t, "report", "--input", input, "--inventory", inventory, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	otherPath := filepath.Join(dir, "other.json")
	if err := os.WriteFile(otherPath, []byte(other), 0600); err != nil {
		t.Fatal(err)
	}
	combinedPath := filepath.Join(dir, "combined.json")
	if err := os.WriteFile(combinedPath, []byte(combined), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := contextCLI(t, "diff", "--before", combinedPath, "--after", otherPath); err == nil {
		t.Fatal("unlike inventory scopes compared")
	}
}

func TestContextInventoryOutputErrors(t *testing.T) {
	project := t.TempDir()
	inventory := filepath.Join(t.TempDir(), "scan.json")
	scan, err := contextCLI(t, "scan", project, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inventory, []byte(scan), 0600); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(input, []byte(`{"message_count":0,"prompt":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"text", "json"} {
		res := resolveCommand([]string{"mecated", "context", "report", "--input", input, "--inventory", inventory, "--format", format})
		if res.err != nil {
			t.Fatal(res.err)
		}
		if err := res.run(strings.NewReader(""), contextBrokenWriter{}, contextBrokenWriter{}); err == nil {
			t.Fatalf("ignored %s output failure", format)
		}
	}
}
