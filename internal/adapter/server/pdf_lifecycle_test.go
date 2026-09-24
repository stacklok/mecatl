package server_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/pdfartifact"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type pdfFailCopyObjects struct {
	*pdfMemoryObjects
	puts   int
	failAt int
}

func (o *pdfFailCopyObjects) Put(ctx context.Context, key string, source io.Reader) error {
	o.puts++
	if o.puts == o.failAt {
		return errors.New("injected object write failure")
	}
	return o.pdfMemoryObjects.Put(ctx, key, source)
}

// TestSDKPDFArtifacts_Scenario4_FailoverReferenceOnly uses separate Redis
// clients and services over one object store. Replica B replays the prompt
// reference and opens the tool PDF after replica A has published the history.
func TestSDKPDFArtifacts_Scenario4_FailoverReferenceOnly(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	newMetadata := func() *redisstore.Store {
		t.Helper()
		metadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = metadata.Close() })
		return metadata
	}
	firstMetadata := newMetadata()
	objects := &pdfMemoryObjects{data: make(map[string][]byte)}
	firstArtifacts := pdfartifact.New(firstMetadata, objects)
	first, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog()}),
		Store:  firstMetadata, PlacementProvider: testPlacementProvider{}, PlacementScope: "test",
		PDFArtifacts: firstArtifacts, DefaultCapabilities: port.ProviderCapabilities{PDF: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first.Close)
	source, err := firstMetadata.Load(t.Context(), "source")
	if err != nil {
		t.Fatal(err)
	}
	promptBytes := []byte("%PDF-1.7\nreplica-prompt-6748\n%%EOF")
	toolBytes := []byte("%PDF-1.7\nreplica-tool-9765\n%%EOF")
	prompt, err := first.UploadPdf(t.Context(), source.ID, "prompt.pdf", bytes.NewReader(promptBytes))
	if err != nil {
		t.Fatal(err)
	}
	promptPart, err := session.NewPDFContent(prompt.ID, prompt.Name, prompt.Size, prompt.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (pdfartifact.ResultProcessor{Artifacts: firstArtifacts}).ProcessToolResult(t.Context(), source.ID,
		session.NewToolResultWithParts("pdf-call", "", []session.Content{{BlockKind: session.BlockEmbeddedResource, MIMEType: "application/pdf", Data: toolBytes}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Parts) != 1 || result.Parts[0].BlockKind != session.BlockPDFArtifact {
		t.Fatalf("tool PDF was not externalized: %+v", result)
	}
	if err := source.SeedHistory([]session.Message{
		session.NewUserMessageWithParts("read the attached PDF", []session.Content{promptPart}),
		session.NewAssistantMessage("", "", []session.ToolCall{session.NewToolCall(result.CallID, "PDFTool", nil)}),
		session.NewToolMessage(result),
	}); err != nil {
		t.Fatal(err)
	}
	if err := firstMetadata.Save(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if err := firstArtifacts.CommitPrompt(t.Context(), source.ID, []string{prompt.ID}); err != nil {
		t.Fatal(err)
	}

	secondMetadata := newMetadata()
	secondArtifacts := pdfartifact.New(secondMetadata, objects)
	var replay port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { replay = req })}, mockllm.TextTurn("replayed"))
	second, err := newPlacementTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog()}),
		Store:  secondMetadata, PlacementProvider: testPlacementProvider{}, PlacementScope: "test",
		PDFArtifacts: secondArtifacts, DefaultCapabilities: port.ProviderCapabilities{PDF: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Close)
	run, err := second.StartRun(t.Context(), source.ID, "continue")
	if err != nil {
		t.Fatalf("replica B failed to resume: %v", err)
	}
	for range run.Events() {
	}
	second.Persist(t.Context(), source.ID)
	second.FinishRun(source.ID, run)
	if len(replay.Messages) < 3 || len(replay.Messages[0].Parts) != 1 || replay.Messages[0].Parts[0].ArtifactID != prompt.ID || len(replay.Messages[0].Parts[0].Data) != 0 {
		t.Fatalf("replica B did not replay the prompt reference: %+v", replay.Messages)
	}
	if replay.Messages[2].ToolResult == nil || len(replay.Messages[2].ToolResult.Parts) != 1 || replay.Messages[2].ToolResult.Parts[0].ArtifactID != result.Parts[0].ArtifactID {
		t.Fatalf("replica B did not replay the tool PDF reference: %+v", replay.Messages)
	}
	_, reader, err := second.DownloadPdf(t.Context(), source.ID, result.Parts[0].ArtifactID)
	if err != nil {
		t.Fatalf("replica B tool PDF download: %v", err)
	}
	got, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil || !bytes.Equal(got, toolBytes) {
		t.Fatalf("replica B tool PDF differs: size=%d err=%v", len(got), readErr)
	}
}

// TestSDKPDFArtifacts_Scenario4_ForkAndCleanup pins the successor transaction:
// private copies precede publication, Clear carries no bytes, and a PDF prompt
// cannot be inherited by a model without PDF input.
func TestSDKPDFArtifacts_Scenario4_ForkAndCleanup(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	metadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	objects := &pdfFailCopyObjects{pdfMemoryObjects: &pdfMemoryObjects{data: make(map[string][]byte)}}
	artifacts := pdfartifact.New(metadata, objects)
	ids := []session.SessionID{"pdf-fork", "pdf-clear", "pdf-rejected", "pdf-failed"}
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog()})
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: engine, Store: metadata, PlacementProvider: testPlacementProvider{}, PlacementScope: "test",
		NewID:        func() session.SessionID { id := ids[0]; ids = ids[1:]; return id },
		PDFArtifacts: artifacts, DefaultCapabilities: port.ProviderCapabilities{PDF: true},
		ResolveCapabilities: func(_, model string, _ session.PermissionMode) port.ProviderCapabilities {
			return port.ProviderCapabilities{PDF: model != "text-only"}
		},
		SessionEngine: func(_ context.Context, selected server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
			return server.SessionEngineResult{Engine: engine, ProviderID: selected.ProviderID, ModelID: selected.ModelID, Capabilities: port.ProviderCapabilities{PDF: selected.ModelID != "text-only"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	source, err := metadata.Load(t.Context(), "source")
	if err != nil {
		t.Fatal(err)
	}
	promptBytes := []byte("%PDF-1.7\nprompt-private\n%%EOF")
	toolBytes := []byte("%PDF-1.7\ntool-private\n%%EOF")
	prompt, err := svc.UploadPdf(t.Context(), source.ID, "prompt.pdf", bytes.NewReader(promptBytes))
	if err != nil {
		t.Fatal(err)
	}
	toolPDF, err := svc.UploadPdf(t.Context(), source.ID, "tool.pdf", bytes.NewReader(toolBytes))
	if err != nil {
		t.Fatal(err)
	}
	promptPart, err := session.NewPDFContent(prompt.ID, prompt.Name, prompt.Size, prompt.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	toolPart, err := session.NewPDFArtifactBlock(toolPDF.ID, toolPDF.Name, toolPDF.Size, toolPDF.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	const callID session.ToolCallID = "pdf-call"
	history := []session.Message{
		session.NewUserMessageWithParts("read", []session.Content{promptPart}),
		session.NewAssistantMessage("", "", []session.ToolCall{session.NewToolCall(callID, "PDFTool", nil)}),
		session.NewToolMessage(session.NewToolResultWithParts(callID, "PDF artifact", []session.Content{toolPart})),
	}
	if err := source.SeedHistory(history); err != nil {
		t.Fatal(err)
	}
	if err := metadata.Save(t.Context(), source); err != nil {
		t.Fatal(err)
	}

	forkID, err := svc.ForkSessionSuccessor(t.Context(), server.ForkSuccessorRequest{Source: source.ID})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	fork, err := metadata.Load(t.Context(), forkID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fork.Conversation.Messages) != 3 {
		t.Fatalf("fork history length = %d", len(fork.Conversation.Messages))
	}
	forkPrompt := fork.Conversation.Messages[0].Parts[0].ArtifactID
	forkTool := fork.Conversation.Messages[2].ToolResult.Parts[0].ArtifactID
	if forkPrompt == prompt.ID || forkTool == toolPDF.ID || forkPrompt == forkTool {
		t.Fatalf("fork retained or conflated source references: prompt=%q tool=%q", forkPrompt, forkTool)
	}
	for _, tc := range []struct {
		id   string
		want []byte
	}{
		{id: forkPrompt, want: promptBytes}, {id: forkTool, want: toolBytes},
	} {
		_, reader, err := svc.DownloadPdf(t.Context(), forkID, tc.id)
		if err != nil {
			t.Fatalf("fork download %q: %v", tc.id, err)
		}
		got, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr != nil || !bytes.Equal(got, tc.want) {
			t.Fatalf("fork download %q = %q, %v", tc.id, got, readErr)
		}
		if _, reader, err := svc.DownloadPdf(t.Context(), source.ID, tc.id); reader != nil || !errors.Is(err, server.ErrNotFound) {
			if reader != nil {
				_ = reader.Close()
			}
			t.Fatalf("source gained fork reference %q: %v", tc.id, err)
		}
	}
	if history[0].Parts[0].ArtifactID != prompt.ID || history[2].ToolResult.Parts[0].ArtifactID != toolPDF.ID {
		t.Fatal("fork mutated source history")
	}
	clearID, err := svc.ClearSessionSuccessor(t.Context(), source.ID, server.SuccessorPlacement{})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	cleared, err := metadata.Load(t.Context(), clearID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.Conversation.Messages) != 0 || len(objects.data) != 4 {
		t.Fatalf("clear inherited PDF history or objects: messages=%d objects=%d", len(cleared.Conversation.Messages), len(objects.data))
	}
	if _, err := svc.ForkSessionSuccessor(t.Context(), server.ForkSuccessorRequest{Source: source.ID, ProviderID: "provider", ModelID: "text-only"}); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("PDF prompt fork to text-only model = %v, want invalid argument", err)
	}
	if exists, err := metadata.PDFSessionExists(t.Context(), "pdf-rejected"); err != nil || exists || len(objects.data) != 4 {
		t.Fatalf("rejected fork published or copied: exists=%t objects=%d err=%v", exists, len(objects.data), err)
	}
	objects.failAt = objects.puts + 2 // fail after one private copy has landed
	if _, err := svc.ForkSessionSuccessor(t.Context(), server.ForkSuccessorRequest{Source: source.ID}); err == nil {
		t.Fatal("fork succeeded after a partial private-copy failure")
	}
	if exists, err := metadata.PDFSessionExists(t.Context(), "pdf-failed"); err != nil || exists || len(objects.data) != 4 {
		t.Fatalf("failed fork retained a snapshot or private objects: exists=%t objects=%d err=%v", exists, len(objects.data), err)
	}
}
