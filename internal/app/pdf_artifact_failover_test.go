package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/pdfartifact"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type pdfReplicaObjects struct{ data map[string][]byte }

func (o *pdfReplicaObjects) Put(_ context.Context, key string, source io.Reader) error {
	data, err := io.ReadAll(source)
	if err == nil {
		o.data[key] = data
	}
	return err
}

func (o *pdfReplicaObjects) Open(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := o.data[key]
	if !ok {
		return nil, errors.New("missing object")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (o *pdfReplicaObjects) Delete(_ context.Context, key string) error {
	delete(o.data, key)
	return nil
}

func (o *pdfReplicaObjects) ListPrefix(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for key := range o.data {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

type pdfReplicaTool struct{ pdf []byte }

func (pdfReplicaTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "PDF", Description: "returns a PDF", Schema: json.RawMessage(`{"type":"object"}`)}
}

func (pdfReplicaTool) ReadOnly() bool { return true }

func (p pdfReplicaTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResultWithParts(call.ID, "producer summary", []session.Content{
		session.NewTextBlock("ready"),
		{BlockKind: session.BlockEmbeddedResource, MIMEType: "application/pdf", Data: p.pdf},
	}), nil
}

func pdfReplicaRegistry(provider port.LLMProvider, model string) *providerRegistry {
	reg := regForTest(provider, providerOpenAI, model)
	reg.meta = newLiveMetaStore()
	reg.meta.Swap(map[string][]modelEntry{
		providerOpenAI: {{ID: model, InputModalities: []string{"text", "pdf"}}},
	})
	return reg
}

// TestSDKPDFArtifacts_Scenario4_FailoverReferenceOnly drives the real selected
// app factory, engine, persistence, service upload/download, and Redis event log.
// Replica B has a fresh store client and factory over the same object store.
func TestSDKPDFArtifacts_Scenario4_FailoverReferenceOnly(t *testing.T) {
	const (
		model  = "test/pdf-model"
		callID = session.ToolCallID("pdf-call")
	)
	promptPDF := []byte("%PDF-1.7\nreplica-prompt-6748\n%%EOF")
	toolPDF := []byte("%PDF-1.7\nreplica-tool-9765\n%%EOF")
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	newMetadata := func() *redisstore.Store {
		t.Helper()
		store, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	objects := &pdfReplicaObjects{data: make(map[string][]byte)}
	firstMetadata := newMetadata()
	firstArtifacts := pdfartifact.New(firstMetadata, objects)
	var firstRequests []port.LLMRequest
	firstProvider := mockllm.NewWith([]mockllm.Option{
		mockllm.WithCapabilities(port.ProviderCapabilities{PDF: true}),
		mockllm.WithRequestObserver(func(req port.LLMRequest) { firstRequests = append(firstRequests, req) }),
	}, mockllm.ToolCallTurn(session.NewToolCall(callID, "PDF", json.RawMessage(`{}`))), mockllm.TextTurn("created"))
	firstCfg := Config{
		Model: model, UserModelDir: t.TempDir(), NoSoul: true,
		pdfArtifacts: firstArtifacts, pdfResultProcessor: pdfartifact.ResultProcessor{Artifacts: firstArtifacts},
	}
	firstFactory := sessionEngineFactoryWithTools(firstCfg, pdfReplicaRegistry(firstProvider, model), firstProvider,
		pdfPromptRedisStore{Store: firstMetadata, artifacts: firstArtifacts},
		permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), hookexec.New(nil), nil,
		prompt.RootAssembler{}, catalogAssets{}, nil)
	first, err := newTestServerService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog()}),
		Store:  pdfServiceStore(firstMetadata, firstArtifacts), EventLog: firstMetadata,
		PDFArtifacts: firstArtifacts,
		SessionEngine: func(ctx context.Context, sel server.ProviderSelector, specs []mcp.ServerConfig, profile server.SessionProfile, workspace string, mode session.PermissionMode) (server.SessionEngineResult, error) {
			return firstFactory(ctx, sel, specs, profile, workspace, mode, []tool.Tool{pdfReplicaTool{pdf: toolPDF}})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first.Close)
	source, err := first.CreateSessionWithProvider(t.Context(), session.ModeDefault, session.Limits{MaxTurns: 5, MaxToolCalls: 5},
		server.ProviderSelector{ProviderID: providerOpenAI, ModelID: model})
	if err != nil {
		t.Fatal(err)
	}
	promptArtifact, err := first.UploadPdf(t.Context(), source.ID, "prompt.pdf", bytes.NewReader(promptPDF))
	if err != nil {
		t.Fatal(err)
	}
	promptPart, err := session.NewPDFContent(promptArtifact.ID, promptArtifact.Name, promptArtifact.Size, promptArtifact.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	run, err := first.StartRunContent(t.Context(), source.ID, "read the attached PDF", []session.Content{promptPart})
	if err != nil {
		t.Fatal(err)
	}
	var result session.ToolResult
	recorder := server.NewRunEventRecorder(context.WithoutCancel(t.Context()), first, source.ID)
	for ev := range run.Events() {
		recorder.Observe(ev)
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			result = *ev.ToolResult
		}
	}
	recorder.Close()
	first.Persist(t.Context(), source.ID)
	first.FinishRun(source.ID, run)
	if result.CallID != callID || result.IsError || len(result.Parts) != 2 ||
		result.Parts[1].BlockKind != session.BlockPDFArtifact || result.Parts[1].ArtifactID == "" {
		t.Fatalf("replica A did not externalize its actual tool result: %+v", result)
	}
	toolID := result.Parts[1].ArtifactID
	if len(firstRequests) != 2 || len(firstRequests[0].Messages) == 0 ||
		len(firstRequests[0].Messages[0].Parts) != 1 ||
		!bytes.Equal(firstRequests[0].Messages[0].Parts[0].Data, promptPDF) {
		t.Fatalf("replica A did not send the uploaded prompt PDF to its selected model: %+v", firstRequests)
	}
	stored, err := firstMetadata.Load(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Conversation.Messages) < 3 ||
		len(stored.Conversation.Messages[0].Parts) != 1 ||
		stored.Conversation.Messages[0].Parts[0].ArtifactID != promptArtifact.ID ||
		len(stored.Conversation.Messages[0].Parts[0].Data) != 0 ||
		stored.Conversation.Messages[2].ToolResult == nil ||
		len(stored.Conversation.Messages[2].ToolResult.Parts) != 2 ||
		stored.Conversation.Messages[2].ToolResult.Parts[1].ArtifactID != toolID {
		t.Fatalf("replica A persisted incomplete PDF references: %+v", stored.Conversation.Messages)
	}
	assertReplicaPDFRedisReferences(t, mr, source.ID, promptArtifact.ID, toolID, promptPDF, toolPDF)
	first.Close()

	secondMetadata := newMetadata()
	secondArtifacts := pdfartifact.New(secondMetadata, objects)
	var replay port.LLMRequest
	secondProvider := mockllm.NewWith([]mockllm.Option{
		mockllm.WithCapabilities(port.ProviderCapabilities{PDF: true}),
		mockllm.WithRequestObserver(func(req port.LLMRequest) { replay = req }),
	}, mockllm.TextTurn("replayed"))
	secondCfg := Config{
		Model: model, UserModelDir: t.TempDir(), NoSoul: true,
		pdfArtifacts: secondArtifacts, pdfResultProcessor: pdfartifact.ResultProcessor{Artifacts: secondArtifacts},
	}
	selectedFactory := sessionEngineFactory(secondCfg, pdfReplicaRegistry(secondProvider, model), secondProvider,
		pdfPromptRedisStore{Store: secondMetadata, artifacts: secondArtifacts},
		permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), hookexec.New(nil), nil,
		prompt.RootAssembler{}, catalogAssets{}, nil)
	var selected int
	second, err := newTestServerService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog()}),
		Store:  pdfServiceStore(secondMetadata, secondArtifacts), EventLog: secondMetadata,
		PDFArtifacts: secondArtifacts,
		SessionEngine: func(ctx context.Context, sel server.ProviderSelector, specs []mcp.ServerConfig, profile server.SessionProfile, workspace string, mode session.PermissionMode) (server.SessionEngineResult, error) {
			if sel.ProviderID != providerOpenAI || sel.ModelID != model {
				t.Errorf("replica B selected %q/%q, want %q/%q", sel.ProviderID, sel.ModelID, providerOpenAI, model)
			}
			selected++
			return selectedFactory(ctx, sel, specs, profile, workspace, mode)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Close)
	resumed, err := second.StartRun(t.Context(), source.ID, "continue")
	if err != nil {
		t.Fatalf("replica B failed to resume: %v", err)
	}
	for ev := range resumed.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			resumed.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
	}
	second.Persist(t.Context(), source.ID)
	second.FinishRun(source.ID, resumed)
	if selected != 1 {
		t.Fatalf("replica B selected factory calls = %d, want 1", selected)
	}
	var replayedPrompt, replayedTool bool
	for _, message := range replay.Messages {
		if message.Role == session.RoleUser && len(message.Parts) > 0 && message.Parts[0].ArtifactID == promptArtifact.ID {
			replayedPrompt = bytes.Equal(message.Parts[0].Data, promptPDF)
		}
		if message.ToolResult != nil && message.ToolResult.CallID == callID {
			for _, part := range message.ToolResult.Parts {
				if part.BlockKind == session.BlockPDFArtifact && part.ArtifactID == toolID && len(part.Data) == 0 {
					replayedTool = true
				}
			}
		}
	}
	if !replayedPrompt || !replayedTool {
		t.Fatalf("replica B model replay missed prompt PDF or tool reference: prompt=%t tool=%t messages=%+v",
			replayedPrompt, replayedTool, replay.Messages)
	}
	_, reader, err := second.DownloadPdf(t.Context(), source.ID, toolID)
	if err != nil {
		t.Fatalf("replica B tool PDF download: %v", err)
	}
	downloaded, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil || !bytes.Equal(downloaded, toolPDF) {
		t.Fatalf("replica B tool PDF differs: size=%d err=%v", len(downloaded), readErr)
	}
	final, err := secondMetadata.Load(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Conversation.Messages) == 0 || len(final.Conversation.Messages[0].Parts) == 0 ||
		len(final.Conversation.Messages[0].Parts[0].Data) != 0 {
		t.Fatal("replica B provider hydration mutated persisted prompt history")
	}
	assertReplicaPDFRedisReferences(t, mr, source.ID, promptArtifact.ID, toolID, promptPDF, toolPDF)
}

func assertReplicaPDFRedisReferences(t *testing.T, mr *miniredis.Miniredis, id session.SessionID, promptID, toolID string, PDFs ...[]byte) {
	t.Helper()
	fields, err := mr.HKeys("mecatl:session:" + string(id))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot strings.Builder
	for _, field := range fields {
		snapshot.WriteString(mr.HGet("mecatl:session:"+string(id), field))
	}
	entries, err := mr.Stream("mecatl:events:" + string(id))
	if err != nil {
		t.Fatal(err)
	}
	var events strings.Builder
	for _, entry := range entries {
		events.WriteString(strings.Join(entry.Values, ""))
	}
	if len(entries) == 0 {
		t.Fatal("Redis event stream is empty")
	}
	for _, artifactID := range []string{promptID, toolID} {
		if !strings.Contains(snapshot.String(), artifactID) || !strings.Contains(events.String(), artifactID) {
			t.Fatalf("Redis snapshot or event stream omitted artifact reference %q", artifactID)
		}
	}
	for _, pdf := range PDFs {
		for _, forbidden := range []string{string(pdf), base64.StdEncoding.EncodeToString(pdf)} {
			if strings.Contains(snapshot.String(), forbidden) || strings.Contains(events.String(), forbidden) {
				t.Fatalf("Redis snapshot or event stream retained PDF payload of %d bytes", len(pdf))
			}
		}
	}
}
