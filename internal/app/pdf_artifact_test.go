package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type pdfLifecycleFixture struct {
	data      []byte
	metadata  server.PDFArtifact
	openedFor session.SessionID
	committed []string
}

type pdfResultProcessorFixture struct{}

func (pdfResultProcessorFixture) ProcessToolResult(_ context.Context, _ session.SessionID, result session.ToolResult) (session.ToolResult, error) {
	return result, nil
}

func (f *pdfLifecycleFixture) Stage(_ context.Context, id session.SessionID, name string, source io.Reader) (server.PDFArtifact, error) {
	if id != "s-pdf" {
		return server.PDFArtifact{}, server.ErrNotFound
	}
	data, err := io.ReadAll(source)
	if err != nil {
		return server.PDFArtifact{}, err
	}
	digest := sha256.Sum256(data)
	f.data = data
	f.metadata = server.PDFArtifact{ID: strings.Repeat("f", 48), Name: name, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}
	return f.metadata, nil
}
func (f *pdfLifecycleFixture) Resolve(_ context.Context, id session.SessionID, artifactID string) (server.PDFArtifact, error) {
	if id != "s-pdf" || artifactID != f.metadata.ID {
		return server.PDFArtifact{}, server.ErrNotFound
	}
	return f.metadata, nil
}
func (f *pdfLifecycleFixture) Open(ctx context.Context, id session.SessionID, artifactID string) (server.PDFArtifact, io.ReadCloser, error) {
	meta, err := f.Resolve(ctx, id, artifactID)
	if err != nil {
		return server.PDFArtifact{}, nil, err
	}
	f.openedFor = id
	return meta, io.NopCloser(bytes.NewReader(f.data)), nil
}
func (f *pdfLifecycleFixture) CommitPrompt(_ context.Context, _ session.SessionID, ids []string) error {
	f.committed = append(f.committed, ids...)
	return nil
}
func (*pdfLifecycleFixture) CopyFork(context.Context, session.SessionID, session.SessionID, []session.Message) ([]session.Message, error) {
	return nil, nil
}
func (*pdfLifecycleFixture) DiscardUnpublished(context.Context, session.SessionID) error {
	return nil
}
func (*pdfLifecycleFixture) Reconcile(context.Context) error { return nil }

func TestPDFReferenceProviderCopiesOnlyModelRequest(t *testing.T) {
	data := []byte("%PDF-1.7\n%%EOF")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	fixture := &pdfLifecycleFixture{data: data, metadata: server.PDFArtifact{ID: "artifact", Name: "report.pdf", Size: int64(len(data)), SHA256: digest}}
	part, err := session.NewPDFContent("artifact", "report.pdf", int64(len(data)), digest)
	if err != nil {
		t.Fatal(err)
	}
	original := []session.Message{session.NewUserMessageWithParts("summarize", []session.Content{part})}
	var observed port.LLMRequest
	inner := mockllm.NewWith([]mockllm.Option{mockllm.WithCapabilities(port.ProviderCapabilities{PDF: true}), mockllm.WithRequestObserver(func(req port.LLMRequest) { observed = req })}, mockllm.TextTurn("ok"))
	wrapped := pdfReferenceProvider{inner: inner, artifacts: fixture}
	stream, err := wrapped.Stream(port.WithSessionID(context.Background(), "s-pdf"), port.LLMRequest{Messages: original})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range stream {
		if err != nil {
			t.Fatal(err)
		}
	}
	if fixture.openedFor != "s-pdf" || !bytes.Equal(observed.Messages[0].Parts[0].Data, fixture.data) {
		t.Fatalf("model request did not receive PDF bytes for exact owner: %+v", observed.Messages)
	}
	if len(original[0].Parts[0].Data) != 0 || original[0].Parts[0].ArtifactID != "artifact" {
		t.Fatal("provider wrapper mutated durable history")
	}
}

func TestPDFPromptCommitStoreMarksSuccessfulSnapshot(t *testing.T) {
	const digest = "acffdf49b58d86b2a91341e976081848a03823302662a85d8ec0b27d89e8db75"
	part, err := session.NewPDFContent("artifact", "report.pdf", 14, digest)
	if err != nil {
		t.Fatal(err)
	}
	sess := session.New("s-pdf", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	sess.Conversation.Append(session.NewUserMessageWithParts("read", []session.Content{part}))
	fixture := &pdfLifecycleFixture{metadata: server.PDFArtifact{ID: "artifact", Name: "report.pdf", Size: 14, SHA256: digest}}
	store := pdfPromptCommitStore{SessionStore: memstore.New(), artifacts: fixture}
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if strings.Join(fixture.committed, ",") != "artifact" {
		t.Fatalf("committed = %v", fixture.committed)
	}
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if strings.Join(fixture.committed, ",") != "artifact" {
		t.Fatalf("unchanged snapshot repeated PDF marker call: %v", fixture.committed)
	}
}

func TestPDFArtifactBuildRejectsMissingResultProcessor(t *testing.T) {
	built, err := buildIsolated(t, t.Context(), Config{
		Workspace: t.TempDir(), Model: "mock", UseMock: true, NoSoul: true,
		pdfArtifacts: &pdfLifecycleFixture{},
	})
	if built != nil {
		built.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "tool-result processor") {
		t.Fatalf("Build without PDF result processor = %v, want composition error", err)
	}
}
