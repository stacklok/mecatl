package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/tokenizer"
)

type contextRuleFixture []prompt.Rule

func (r contextRuleFixture) ListRules(context.Context) ([]prompt.Rule, error) {
	return []prompt.Rule(r), nil
}

func TestContextObservedRules(t *testing.T) {
	counter, err := tokenizer.New(tokenizer.O200kBase)
	if err != nil {
		t.Fatal(err)
	}
	assembler := prompt.RulesAssembler{Src: contextRuleFixture{
		{Name: "project-rule", Body: "private project payload", Origin: prompt.RuleOriginProject},
		{Name: "user-rule", Body: "private user payload", Origin: prompt.RuleOriginUser},
		{Name: "not-shown", Body: "private omitted payload", Origin: prompt.RuleOriginProject},
	}, MaxCount: 2}
	messages, meta, err := assembler.AssembleWithManifest(context.Background())
	if err != nil || len(messages) != 1 || len(meta) != 1 || meta[0].Rules == nil {
		t.Fatalf("source manifest: %v %+v", err, meta)
	}
	rm := meta[0].Rules
	if rm.OmittedCount != 1 || len(rm.Spans) != 2 {
		t.Fatalf("bad source fixture: %+v", rm)
	}
	component := session.RequestPromptComponent{Kind: meta[0].Kind, Provenance: meta[0].Provenance, Bytes: len(messages[0].Text), OmittedRules: rm.OmittedCount}
	for _, span := range rm.Spans {
		block := messages[0].Text[span.Start:span.End]
		component.Rules = append(component.Rules, session.RequestRuleMetric{Name: span.Name, Origin: span.Origin, RenderedBytes: len(block), EstimatedTokens: counter.Count(block)})
	}
	manifest := session.RequestManifestPayload{MessageCount: 1, Prompt: []session.RequestPromptComponent{component}, TokenEstimateMethod: "local_estimate"}
	dir := t.TempDir()
	event := filepath.Join(dir, "event.json")
	writeContextJSONFixture(t, event, session.Event{Type: session.EvRequestManifest, RequestManifest: &manifest})
	output, err := contextCLI(t, "report", "--input", event, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc contextDocument
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatal(err)
	}
	found := map[string]contextOccurrence{}
	for _, o := range doc.Occurrences {
		found[o.Source] = o
	}
	for source, name := range map[string]string{"rule-project": "project-rule", "rule-user": "user-rule"} {
		o, ok := found[source]
		if !ok || o.Name != name || o.Bytes == nil || o.EstimatedTokens == nil || o.Status != "observed rendered rule; included in rules fragment" {
			t.Fatalf("missing observed %s: %s", source, output)
		}
	}
	if o := found["rule-omitted"]; o.Count == nil || *o.Count != 1 || o.Bytes != nil || o.EstimatedTokens != nil {
		t.Fatalf("omitted fabricated cost: %+v", o)
	}
	if o := found["fragment"]; o.Bytes == nil || *o.Bytes != component.Bytes {
		t.Fatalf("overall fragment lost: %+v", o)
	}
	if strings.Contains(output, "private") || strings.Contains(output, "not-shown") {
		t.Fatalf("rule content or omitted name disclosed: %s", output)
	}
	text, err := contextCLI(t, "report", "--input", event, "--format", "text")
	if err != nil || !strings.Contains(text, "rule-project/project-rule") || !strings.Contains(text, "rule-user/user-rule") || !strings.Contains(text, "omitted rules: 1; cost unknown") || strings.Contains(text, "private") {
		t.Fatalf("text view: %v %s", err, text)
	}
	// Exercise the debugger's distinct components/decisions/tools projection shape.
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var projected map[string]json.RawMessage
	if err := json.Unmarshal(payload, &projected); err != nil {
		t.Fatal(err)
	}
	for from, to := range map[string]string{"tool_names": "tools", "tool_decisions": "decisions", "prompt": "components"} {
		projected[to] = projected[from]
		delete(projected, from)
	}
	projection := filepath.Join(dir, "projection.json")
	writeContextJSONFixture(t, projection, map[string]any{"view": "manifest", "available": true, "authoritative": true, "source": "request.manifest events", "projection_complete": true, "scan_complete": false, "retention_complete": false, "offset": 0, "limit": 1, "rows": []any{projected}})
	projectedOutput, err := contextCLI(t, "report", "--input", projection, "--row", "0", "--format", "json")
	if err != nil || !strings.Contains(projectedOutput, `"source":"rule-user"`) || !strings.Contains(projectedOutput, `"count":1`) || !strings.Contains(projectedOutput, "scan=false") {
		t.Fatalf("debugger projection: %v %s", err, projectedOutput)
	}
	writeContextJSONFixture(t, filepath.Join(dir, "versioned.json"), doc)
	if _, err := contextCLI(t, "report", "--input", filepath.Join(dir, "versioned.json")); err != nil {
		t.Fatalf("versioned report rejected: %v", err)
	}
}
func writeContextJSONFixture(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestContextObservedToolsAndDecisions(t *testing.T) {
	manifest := session.RequestManifestPayload{MessageCount: 1, Prompt: []session.RequestPromptComponent{}, TokenEstimateMethod: "local_estimate",
		AdvertisedTools: []session.RequestToolMetric{
			{Name: "mcp__srv__query", EstimatedTokens: 12, DescriptionBytes: 8, SchemaBytes: 16},
			{Name: "mcp__other__search", EstimatedTokens: 3},
			{Name: "mcp__srv__light", EstimatedTokens: 2},
			{Name: "read", EstimatedTokens: 5},
		}, ToolDecisions: []session.RequestToolDecision{
			{Name: "mcp__srv__query", Source: "mcp", Decision: "advertised"},
			{Name: "mcp__srv__light", Source: "mcp", Decision: "disclosure_hidden"},
			{Name: "read", Source: "catalog", Decision: "advertised"},
			{Name: "write", Source: "catalog", Decision: "mode_filtered"},
			{Name: "shell", Source: "catalog", Decision: "authority_filtered"},
			{Name: "remote", Source: "mcp", Decision: "mount_unavailable"},
			{Name: "mcp__srv__secret", Source: "mcp", Decision: "disclosure_hidden"},
			{Name: "delegate", Source: "overlay", Decision: "shadowed"},
			{Name: "mcp__srv__\x1b[31m", Source: "mcp", Decision: "mode_filtered"},
		}}
	dir := t.TempDir()
	file := filepath.Join(dir, "manifest.json")
	writeContextJSONFixture(t, file, manifest)
	output, err := contextCLI(t, "report", "--input", file, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "\\u001b") || strings.Contains(output, "[31m") {
		t.Fatalf("hostile decision name leaked: %s", output)
	}
	var doc contextDocument
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatal(err)
	}
	seen := map[string]contextOccurrence{}
	for _, o := range doc.Occurrences {
		seen[o.Source+"/"+o.Name] = o
	}
	if o := seen["tool/mcp__srv__query"]; o.Provenance != "mcp-inferred" || o.MCPServer != "srv" || o.EstimatedTokens == nil || *o.EstimatedTokens != 12 {
		t.Fatalf("observed MCP tool: %+v", o)
	}
	if o := seen["tool/mcp__other__search"]; o.Provenance != "mcp-inferred" || o.MCPServer != "other" {
		t.Fatalf("inferred MCP tool: %+v", o)
	}
	if o := seen["tool/mcp__srv__light"]; o.Status != "observed lightweight spec; disclosure hidden" || o.EstimatedTokens == nil || *o.EstimatedTokens != 2 {
		t.Fatalf("hidden measured spec: %+v", o)
	}
	if _, ok := seen["tool-candidate/mcp__srv__light"]; ok {
		t.Fatal("measured lightweight spec also reported as absent")
	}
	if o := seen["tool/mcp__srv__secret"]; o.Status != "observed lightweight spec; cost unknown" || o.EstimatedTokens != nil || o.MCPServer != "srv" {
		t.Fatalf("legacy lightweight spec must not be called absent: %+v", o)
	}
	for _, name := range []string{"write", "shell", "remote", "delegate"} {
		o := seen["tool-candidate/"+name]
		if o.Source != "tool-candidate" || o.EstimatedTokens != nil || o.Bytes != nil || !strings.Contains(o.Status, "cost unknown") {
			t.Fatalf("candidate %s: %+v", name, o)
		}
	}
	text, err := contextCLI(t, "report", "--input", file, "--format", "text")
	if err != nil || !strings.Contains(text, "MCP server: srv") || !strings.Contains(text, "unknown tokens") || strings.Contains(text, "[31m") {
		t.Fatalf("text evidence: %v %s", err, text)
	}
	writeContextJSONFixture(t, file, doc)
	if _, err := contextCLI(t, "report", "--input", file); err != nil {
		t.Fatalf("versioned evidence rejected: %v", err)
	}
}
func TestContextRuleLabelsAndBounds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evidence.json")
	rules := []session.RequestRuleMetric{
		{Name: "shared", Origin: "project", RenderedBytes: 12, EstimatedTokens: 3},
		{Name: "shared", Origin: "user", RenderedBytes: 9, EstimatedTokens: 2},
		{Name: "\x1b[31mINJECT\n", Origin: "driver", RenderedBytes: 5, EstimatedTokens: 1},
		{Name: "unknown", Origin: "user", RenderedBytes: 1, EstimatedTokens: 1},
	}
	writeContextJSONFixture(t, path, session.RequestManifestPayload{MessageCount: 1, Prompt: []session.RequestPromptComponent{{Kind: "instruction", Provenance: "rules", Bytes: 90, Rules: rules}}})
	output, err := contextCLI(t, "report", "--input", path, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "INJECT") || strings.Contains(output, "\\u001b") || !strings.Contains(output, `"source":"rule-user","name":"rule-001"`) || !strings.Contains(output, `"source":"rule-unknown","name":"rule-002"`) || !strings.Contains(output, `"source":"rule-user","name":"rule-003"`) {
		t.Fatalf("unsafe or duplicate rule name: %s", output)
	}
	text, err := contextCLI(t, "report", "--input", path, "--format", "text")
	if err != nil || strings.Contains(text, "INJECT") || strings.Contains(text, "[31m") {
		t.Fatalf("unsafe terminal output: %v %s", err, text)
	}
	// Nested metric arrays must not grow output beyond the bounded occurrence inventory.
	huge := make([]session.RequestRuleMetric, contextMaxEntries)
	for i := range huge {
		huge[i] = session.RequestRuleMetric{Name: "same", Origin: "project", RenderedBytes: 1, EstimatedTokens: 1}
	}
	writeContextJSONFixture(t, path, session.RequestManifestPayload{MessageCount: 1, Prompt: []session.RequestPromptComponent{{Kind: "instruction", Provenance: "rules", Bytes: 90, Rules: huge}}})
	if _, err := contextCLI(t, "report", "--input", path); err == nil {
		t.Fatal("unbounded nested rules accepted")
	}
}
func TestContextMalformedRuleEvidence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evidence.json")
	for _, component := range []string{
		`{"kind":"instruction","provenance":"rules","bytes":10,"omitted_rules":-1}`,
		`{"kind":"instruction","provenance":"rules","bytes":10,"rules":[{"name":"r","origin":"project","rendered_bytes":-1,"estimated_tokens":1}]}`,
		`{"kind":"system","provenance":"stable","bytes":10,"rules":[{"name":"r","origin":"project","rendered_bytes":1,"estimated_tokens":1}]}`,
	} {
		data := `{"message_count":1,"prompt":[` + component + `]}`
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := contextCLI(t, "report", "--input", path); err == nil {
			t.Fatalf("invalid rules accepted: %s", data)
		}
	}
	for _, bad := range []string{
		`{"version":"mecatl.context/v0alpha1","kind":"report","method":"unknown","coverage":"request manifest","occurrences":[{"source":"rule-omitted","name":"fragment-000","status":"observed omitted count; cost unknown","count":-1}]}`,
		`{"version":"mecatl.context/v0alpha1","kind":"report","method":"unknown","coverage":"request manifest","occurrences":[{"source":"tool-candidate","name":"r","status":"decision mode_filtered; cost unknown","estimated_tokens":0}]}`,
	} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := contextCLI(t, "report", "--input", path); err == nil {
			t.Fatalf("malformed versioned evidence accepted: %s", bad)
		}
	}
	// Pre-metadata manifests never imply zero rules or zero omitted cost.
	if err := os.WriteFile(path, []byte(`{"message_count":1,"prompt":[{"kind":"instruction","provenance":"rules","bytes":9}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := contextCLI(t, "report", "--input", path, "--format", "json")
	if err != nil || strings.Contains(output, "rule-project") || strings.Contains(output, "rule-omitted") {
		t.Fatalf("legacy rules misreported: %v %s", err, output)
	}
}
