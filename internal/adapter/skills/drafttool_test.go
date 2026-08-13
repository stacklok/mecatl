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

func draftCall(t *testing.T, m map[string]any) session.ToolCall {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return session.NewToolCall("id-draft", DraftToolName, raw)
}

func newDraftTool(t *testing.T, dir string, existing []Skill) tool.Tool {
	t.Helper()
	return NewDraftTool(NewDirDrafter(dir, existing, WithClock(fixedClock())))
}

func TestDraftToolIsMutating(t *testing.T) {
	tl := newDraftTool(t, t.TempDir(), nil)
	if tl.ReadOnly() {
		t.Fatal("SkillDraft must be mutating (ReadOnly() == false): it writes a file")
	}
}

func TestDraftToolPlanModeFiltered(t *testing.T) {
	cat := tool.NewCatalog()
	cat.MustRegister(newDraftTool(t, t.TempDir(), nil))
	for _, s := range cat.Specs(session.ModePlan) {
		if s.Name == DraftToolName {
			t.Fatal("SkillDraft (mutating) must be filtered out of plan mode")
		}
	}
}

func TestDraftToolExecuteSuccess(t *testing.T) {
	quarantine := t.TempDir()
	tl := newDraftTool(t, quarantine, nil)
	res, err := tl.Execute(context.Background(), draftCall(t, map[string]any{
		"name":        "deploy-to-staging",
		"description": "How to deploy to staging.",
		"body":        "1. Build.\nDone when: green.",
	}), tool.Environment{})
	if err != nil {
		t.Fatalf("Execute: unexpected harness error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned an error result: %s", res.Content)
	}
	if !strings.Contains(res.Content, filepath.Join(quarantine, "deploy-to-staging")) {
		t.Errorf("result should name the quarantine path, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "NOT active this session") {
		t.Errorf("result must tell the model the draft is not active, got %q", res.Content)
	}
}

func TestDraftToolSurfacesSimilarSkills(t *testing.T) {
	existing := []Skill{{Name: "deploy-staging", Description: "How to deploy to staging."}}
	tl := newDraftTool(t, t.TempDir(), existing)
	res, err := tl.Execute(context.Background(), draftCall(t, map[string]any{
		"name":        "deploy-to-staging",
		"description": "How to deploy to staging.",
		"body":        "1. Build.\nDone when: green.",
	}), tool.Environment{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("near-duplicate must NOT block, got error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "deploy-staging") {
		t.Errorf("result should mention the similar skill, got %q", res.Content)
	}
}

func TestDraftToolMalformedArgs(t *testing.T) {
	tl := newDraftTool(t, t.TempDir(), nil)
	res, err := tl.Execute(context.Background(), session.NewToolCall("id", DraftToolName, json.RawMessage("{bad")), tool.Environment{})
	if err != nil {
		t.Fatalf("Execute: unexpected harness error: %v", err)
	}
	if !res.IsError {
		t.Fatal("malformed args must be a model-addressable error result, not a harness error")
	}
}

func TestDraftToolValidationErrorIsResultNotFault(t *testing.T) {
	quarantine := t.TempDir()
	tl := newDraftTool(t, quarantine, nil)
	res, err := tl.Execute(context.Background(), draftCall(t, map[string]any{
		"name":        "Bad Name",
		"description": "d",
		"body":        "b",
	}), tool.Environment{})
	if err != nil {
		t.Fatalf("a validation failure must NOT be a harness error, got: %v", err)
	}
	if !res.IsError {
		t.Fatal("invalid name must yield an error result")
	}
	if entries, _ := os.ReadDir(quarantine); len(entries) != 0 {
		t.Errorf("a rejected draft wrote %d entries", len(entries))
	}
}

func TestNewDraftToolNilPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewDraftTool(nil) must panic")
		}
	}()
	NewDraftTool(nil)
}
