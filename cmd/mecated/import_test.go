package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

func TestRunImportCreatesResumableSessionAndCopiesArtifacts(t *testing.T) {
	root := t.TempDir()
	sourceWorkspace := filepath.Join(root, "source")
	targetWorkspace := filepath.Join(root, "target")
	storeDir := filepath.Join(root, "sessions")
	skillSource := filepath.Join(root, "skills")
	transcriptPath := filepath.Join(root, "session.jsonl")
	writeImportTestFile(t, filepath.Join(sourceWorkspace, "main.go"), "package main")
	writeImportTestFile(t, filepath.Join(sourceWorkspace, ".git", "config"), "omit")
	writeImportTestFile(t, filepath.Join(skillSource, "review", "SKILL.md"), "---\nname: review\n---")
	writeImportTestFile(t, transcriptPath, strings.Join([]string{
		`{"type":"session_meta","timestamp":"2026-03-04T05:06:07Z","payload":{"id":"abc/123","cwd":"` + sourceWorkspace + `"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"continue the migration"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Ready."}]}}`,
	}, "\n"))

	var out strings.Builder
	err := runImport([]string{
		"--from", "codex",
		"--session", transcriptPath,
		"--store-dir", storeDir,
		"--workspace", targetWorkspace,
		"--copy-files",
		"--skills-dir", skillSource,
	}, &out)
	if err != nil {
		t.Fatalf("runImport: %v", err)
	}
	if !strings.Contains(out.String(), `Imported codex session "import-codex-abc-123": 2 messages, 1 workspace files, 1 skills`) {
		t.Fatalf("output = %q", out.String())
	}

	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	got, err := store.Load(context.Background(), session.SessionID("import-codex-abc-123"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != session.StateIdle || got.Workspace != targetWorkspace || got.Title != "continue the migration" {
		t.Fatalf("imported session = %#v", got)
	}
	if len(got.Conversation.Messages) != 2 || got.Conversation.Messages[1].Text != "Ready." {
		t.Fatalf("conversation = %#v", got.Conversation.Messages)
	}
	if _, err := os.Stat(filepath.Join(targetWorkspace, "main.go")); err != nil {
		t.Fatalf("workspace file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetWorkspace, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetWorkspace, ".mecatl", "skills", "review", "SKILL.md")); err != nil {
		t.Fatalf("skill missing: %v", err)
	}

	if err := runImport([]string{"--from", "codex", "--session", transcriptPath, "--store-dir", storeDir, "--workspace", targetWorkspace}, &out); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second runImport error = %v", err)
	}
}

func TestRunImportCopyFilesRequiresExplicitWorkspace(t *testing.T) {
	root := t.TempDir()
	transcript := filepath.Join(root, "session.jsonl")
	writeImportTestFile(t, transcript, `{"type":"event_msg","payload":{"type":"user_message","message":"hello"}}`)
	err := runImport([]string{"--from", "codex", "--session", transcript, "--store-dir", filepath.Join(root, "store"), "--copy-files"}, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "no cwd") {
		t.Fatalf("runImport error = %v", err)
	}
}

func TestRunImportRejectsTranscriptWithNoImportableText(t *testing.T) {
	root := t.TempDir()
	transcript := filepath.Join(root, "session.jsonl")
	// Only system/developer/reasoning records: none yield importable user or
	// assistant text, so the import must fail before touching the store.
	writeImportTestFile(t, transcript, strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"abc","cwd":"/work"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"private instructions"}]}}`,
		`{"type":"response_item","payload":{"type":"reasoning","encrypted_content":"opaque"}}`,
	}, "\n"))
	storeDir := filepath.Join(root, "store")
	err := runImport([]string{
		"--from", "codex",
		"--session", transcript,
		"--store-dir", storeDir,
		"--workspace", filepath.Join(root, "ws"),
	}, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "contains no importable user or assistant text") {
		t.Fatalf("runImport error = %v", err)
	}
	if _, statErr := os.Stat(storeDir); !os.IsNotExist(statErr) {
		t.Fatalf("store created on empty-text import, stat error = %v", statErr)
	}
}

func writeImportTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
