package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

type contextBrokenWriter struct{}

func (contextBrokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken output") }
func TestContextOutputErrors(t *testing.T) {
	for _, argv := range [][]string{{"context", "--help"}, {"context", "scan", "--help"}, {"context", "report", "--input", "-", "--format", "json"}, {"context", "report", "--input", "-", "--format", "text"}} {
		res := resolveCommand(append([]string{"mecated"}, argv...))
		if res.err != nil {
			t.Fatal(res.err)
		}
		if err := res.run(strings.NewReader(`{"message_count":0,"prompt":[]}`), contextBrokenWriter{}, contextBrokenWriter{}); err == nil {
			t.Fatalf("did not return writer error: %v", argv)
		}
	}
}
func TestContextOpaqueIdentifiers(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "manifest.json")
	payload := `{"model":"model\u001b[31m","message_count":0,"prompt":[],"advertised_tools":[{"name":"mcp__x/y\u001b[31m"},{"name":"tool-000"},{"name":"custom.tool"}]}`
	if err := os.WriteFile(input, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	text, err := contextCLI(t, "report", "--input", input, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `"model":"model-000"`) || !strings.Contains(text, `"name":"tool-000x"`) || !strings.Contains(text, `"name":"tool-000"`) || !strings.Contains(text, `"name":"custom.tool"`) || strings.Contains(text, "mcp__x/y") || strings.Contains(text, "\\u001b") || strings.Contains(text, "opaque-") {
		t.Fatalf("unsafe output: %s", text)
	}
	if err := os.WriteFile(input, []byte(`{"message_count":0,"prompt":[],"advertised_tools":[{"name":"duplicate"},{"name":"duplicate"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := contextCLI(t, "report", "--input", input); err == nil {
		t.Fatal("duplicate tool IDs accepted")
	}
}
func TestContextEvidenceInputs(t *testing.T) {
	dir := t.TempDir()
	row := `{"model":"mock","context_window":100,"message_count":2,"message_bytes":20,"estimated_request_tokens":30,"token_estimate_method":"local_estimate","tools":["mcp__a/b"],"decisions":[],"components":[{"kind":"system","provenance":"stable","bytes":7,"estimated_tokens":2}],"advertised_tools":[{"name":"mcp__a/b","name_bytes":8,"doc_bytes":12,"schema_bytes":20,"estimated_name_tokens":4,"estimated_doc_tokens":5,"estimated_schema_tokens":6,"estimated_tokens":17}]}`
	projection := `{"view":"manifest","available":true,"authoritative":true,"source":"request.manifest events","projection_complete":false,"scan_complete":false,"retention_complete":false,"offset":12,"limit":1,"next_offset":13,"rows":[` + row + `]}`
	p := filepath.Join(dir, "projection.json")
	if err := os.WriteFile(p, []byte(projection), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := contextCLI(t, "report", "--input", p); err == nil {
		t.Fatal("projection without row accepted")
	}
	output, err := contextCLI(t, "report", "--input", p, "--row", "0", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc contextDocument
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.RowIndex == nil || *doc.RowIndex != 12 || !strings.Contains(doc.Coverage, "scan=false") || doc.Headroom == nil || *doc.Headroom != 70 || doc.MessageCount == nil || *doc.MessageCount != 2 {
		t.Fatalf("bad projection report: %s", output)
	}
	found := false
	for _, o := range doc.Occurrences {
		if o.Source == "tool" && o.Name == "tool-000" {
			found = true
			if o.DocBytes == nil || *o.DocBytes != 12 || o.EstimatedSchemaTokens == nil || *o.EstimatedSchemaTokens != 6 {
				t.Fatalf("missing tool breakdown: %+v", o)
			}
		}
	}
	if !found {
		t.Fatalf("missing safe tool id: %s", output)
	}
	event := `{"Type":"request.manifest","RequestManifest":{"message_count":2,"message_bytes":20,"prompt":[],"model":"mock","context_window":100},"Text":""}`
	eventPath := filepath.Join(dir, "event.json")
	if err := os.WriteFile(eventPath, []byte(event), 0600); err != nil {
		t.Fatal(err)
	}
	text, err := contextCLI(t, "report", "--input", eventPath, "--format", "text")
	if err != nil || !strings.Contains(text, "model=mock") {
		t.Fatalf("event: %v %s", err, text)
	}
	actual, err := json.Marshal(session.Event{Type: session.EvRequestManifest, Seq: 1, Turn: 2, RunID: "run", RequestManifest: &session.RequestManifestPayload{MessageCount: 1, Prompt: []session.RequestPromptComponent{}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eventPath, actual, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := contextCLI(t, "report", "--input", eventPath); err != nil {
		t.Fatalf("actual event envelope: %v", err)
	}
	for _, bad := range []string{`[{"Type":"request.manifest"}]`, `{"Type":"request.manifest","RequestManifest":{"message_count":0,"prompt":[]},"Unexpected":"x"}`, `{"Type":"request.manifest","RequestManifest":{"message_count":0,"prompt":[]},"Text":"injection"}`, `{"Type":"request.manifest","RequestManifest":{"message_count":0,"prompt":[]},"ToolCall":{"Name":"x"}}`} {
		if err := os.WriteFile(eventPath, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := contextCLI(t, "report", "--input", eventPath); err == nil {
			t.Fatalf("accepted bad event %s", bad)
		}
	}
	if err := os.WriteFile(eventPath, []byte(event), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := contextCLI(t, "diff", "--before", p, "--before-row", "0", "--after", eventPath); err == nil {
		t.Fatal("incompatible scopes accepted")
	}
	symlink := filepath.Join(dir, "link")
	if err := os.Symlink(p, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := contextCLI(t, "report", "--input", symlink, "--row", "0"); err == nil {
		t.Fatal("input symlink accepted")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := contextCLI(t, "report", "--input", fifo); err == nil {
		t.Fatal("fifo accepted")
	}
}

func TestContextProcessHelper(t *testing.T) {
	if os.Getenv("MECATL_CONTEXT_TEST_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	res := resolveCommand(append([]string{"mecated"}, args...))
	if res.err != nil {
		t.Fatal(res.err)
	}
	if !res.handled || res.run == nil {
		t.Fatal("command fell through")
	}
	if err := res.run(os.Stdin, os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
}
func contextProcess(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestContextProcessHelper$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "MECATL_CONTEXT_TEST_HELPER=1", "HOME="+t.TempDir())
	return cmd
}
func TestContextProcess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("guidance"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := contextProcess(t, "context", "scan", dir, "--format", "json")
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("scan process: %v %s", err, data)
	}
	if !bytes.Contains(data, []byte(`"version":"mecatl.context/v0alpha1"`)) || bytes.Contains(data, []byte("guidance")) {
		t.Fatalf("bad CLI output: %s", data)
	}
	cmd = contextProcess(t, "context", "report", "--input", "-", "--format", "json")
	cmd.Stdin = strings.NewReader(`{"message_count":0,"prompt":[]}`)
	data, err = cmd.CombinedOutput()
	if err != nil || !bytes.Contains(data, []byte(`"kind":"report"`)) {
		t.Fatalf("report process: %v %s", err, data)
	}
}
