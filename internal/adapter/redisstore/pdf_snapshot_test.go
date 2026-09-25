package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/session"
)

func TestArtifactStorage_SnapshotReferencesTyped(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	st, err := NewWithConfig(Config{Addr: mr.Addr(), AllowPlaintext: true, ArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const id session.SessionID = "typed-reference"
	promptID := strings.Repeat("a", 48)
	toolID := strings.Repeat("b", 48)
	textOnlyID := strings.Repeat("c", 48)
	sha := strings.Repeat("d", 64)
	sess := session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC))
	if err := sess.RecordUserPrompt("the artifact ID is "+promptID, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	assertReference := func(artifactID string, want bool) {
		t.Helper()
		got, err := st.ArtifactReferencedInSnapshot(t.Context(), id, artifactID)
		if err != nil || got != want {
			t.Fatalf("ArtifactReferencedInSnapshot(%q) = %v, %v; want %v", artifactID, got, err, want)
		}
	}
	assertReference(promptID, false)

	promptPart, err := session.NewPDFContent(promptID, "prompt.pdf", 8, sha)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordUserPromptWithParts("attached", []session.Content{promptPart}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	assertReference(promptID, true)

	toolPart, err := session.NewArtifactBlock(toolID, "result.pdf", "application/pdf", 8, sha)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{session.NewToolCall("pdf-call", "make_pdf", nil)})); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordToolResults([]session.ToolResult{session.NewToolResultWithParts("pdf-call", "also mentions "+textOnlyID, []session.Content{toolPart})}); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	assertReference(toolID, true)
	assertReference(textOnlyID, false)

	if err := st.testClient().HSet(t.Context(), sessionKey(id), fieldBlob, "{").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ArtifactReferencedInSnapshot(t.Context(), id, promptID); err == nil {
		t.Fatal("corrupt authoritative snapshot allowed reconciliation to proceed")
	}
	other := session.New("other-session", session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC))
	if err := st.Save(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	wrongBlob, err := st.testClient().HGet(t.Context(), sessionKey(other.ID), fieldBlob).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.testClient().HSet(t.Context(), sessionKey(id), fieldBlob, wrongBlob).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ArtifactReferencedInSnapshot(t.Context(), id, promptID); err == nil {
		t.Fatal("snapshot with mismatched session identity allowed reconciliation to proceed")
	}
}

func TestArtifactStorage_ArtifactRecordsCursorAndErrors(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	st, err := NewWithConfig(Config{Addr: mr.Addr(), AllowPlaintext: true, ArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const id session.SessionID = "pdf-cursor"
	fields := make(map[string]any, 125)
	for i := range 125 {
		artifactID := fmt.Sprintf("%048x", i)
		wire, err := json.Marshal(ArtifactRecord{ID: artifactID, Name: "test.pdf", MIMEType: "application/pdf", State: ArtifactReady, CreatedAt: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		fields[artifactID] = wire
	}
	if err := st.testClient().HSet(t.Context(), artifactKey(id), fields).Err(); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool, len(fields))
	for entry, err := range st.ArtifactRecords(t.Context()) {
		if err != nil {
			t.Fatal(err)
		}
		if entry.SessionID != id || seen[entry.Record.ID] {
			t.Fatalf("unexpected or repeated PDF record: %+v", entry)
		}
		seen[entry.Record.ID] = true
	}
	if len(seen) != len(fields) {
		t.Fatalf("iterated %d PDF records, want %d", len(seen), len(fields))
	}
	if err := st.testClient().HSet(t.Context(), artifactKey(id), "broken", "{").Err(); err != nil {
		t.Fatal(err)
	}
	foundError := false
	for _, err := range st.ArtifactRecords(t.Context()) {
		if err != nil {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Fatal("corrupt PDF record was skipped")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	foundError = false
	for _, err := range st.ArtifactRecords(cancelled) {
		if err != nil {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Fatal("cancelled PDF record scan did not report an error")
	}
}
