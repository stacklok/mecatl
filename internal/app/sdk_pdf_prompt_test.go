package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/provider/openai"
)

// TestSDKPDFArtifacts_Scenario1_RejectInvalidOrUnauthorized guards the native
// endpoint identity and exact-model gate through default and registry re-mints.
func TestSDKPDFArtifacts_Scenario1_RejectInvalidOrUnauthorized(t *testing.T) {
	const model = "test/pdf-model"
	for _, tc := range []struct {
		name    string
		id      string
		baseURL string
		native  bool
		wantPDF bool
	}{
		{name: "canonical OpenAI", id: providerOpenAI, native: true, wantPDF: true},
		{name: "OpenAI-compatible override", id: providerOpenAI, baseURL: "https://compatible.example/v1", native: true},
		{name: "OpenRouter Responses", id: providerOpenRouter, baseURL: openRouterDefaultBaseURL},
		{name: "canonical Anthropic", id: providerAnthropic, native: false, wantPDF: true},
		{name: "Anthropic-compatible override", id: providerAnthropic, baseURL: "https://compatible.example/v1", native: false},
		{name: "OpenRouter Messages", id: providerOpenRouterAnthropic, baseURL: "https://openrouter.ai/api/v1", native: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Model: model, pdfArtifacts: &pdfLifecycleFixture{}}
			var entry providerEntry
			if tc.native {
				entry = newOpenAICompatEntry(cfg, tc.id, "test-key", tc.baseURL)
			} else {
				entry = newAnthropicEntryFor(cfg, tc.id, "test-key", tc.baseURL, newLiveMetaStore(), false)
			}
			reg := &providerRegistry{entries: map[string]providerEntry{tc.id: entry}, defaultID: tc.id, defaultModel: model, meta: newLiveMetaStore()}
			reg.meta.Swap(map[string][]modelEntry{tc.id: {{ID: model, InputModalities: []string{"text", "pdf"}}, {ID: "text-only", InputModalities: []string{"text"}}}})
			if got := entry.provider.Capabilities().PDF; got != tc.wantPDF {
				t.Fatalf("initial adapter PDF capability = %t, want %t", got, tc.wantPDF)
			}
			if got := entry.remint("", port.ProviderCapabilities{PDF: true}).Capabilities().PDF; got != tc.wantPDF {
				t.Fatalf("per-session re-mint PDF capability = %t, want %t", got, tc.wantPDF)
			}
			reg.remintEntry(tc.id, model)
			reminted, _ := reg.Lookup(tc.id)
			if got := reminted.provider.Capabilities().PDF; got != tc.wantPDF {
				t.Fatalf("registry re-mint PDF capability = %t, want %t", got, tc.wantPDF)
			}
			if got := configuredPDFModelCapability(cfg, reg, tc.id, model).PDF; got != tc.wantPDF {
				t.Fatalf("selected model PDF capability = %t, want %t", got, tc.wantPDF)
			}
			if got := configuredPDFModelCapability(cfg, reg, tc.id, "text-only").PDF; got {
				t.Fatal("text-only model advertised PDF input")
			}
		})
	}
}

// TestSDKPDFArtifacts_Scenario1_UploadPromptProvider pins the selected native
// adapter, transient hydration, and reference-only persisted prompt together.
func TestSDKPDFArtifacts_Scenario1_UploadPromptProvider(t *testing.T) {
	const model = "test/pdf-model"
	pdf := []byte("%PDF-1.7\nunique-pdf-payload\n%%EOF")
	artifacts := &pdfLifecycleFixture{}
	meta, err := artifacts.Stage(context.Background(), "s-pdf", "report.pdf", bytes.NewReader(pdf))
	if err != nil || meta.ID == "" || meta.Size != int64(len(pdf)) || len(meta.SHA256) != 64 {
		t.Fatalf("stage = %+v, %v", meta, err)
	}
	var wire []byte
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var readErr error
		wire, readErr = io.ReadAll(req.Body)
		if readErr != nil {
			return nil, readErr
		}
		body := "event: response.output_text.delta\n" +
			`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"done"}` + "\n\n" +
			"event: response.completed\n" +
			`data: {"type":"response.completed","sequence_number":1,"response":{"status":"completed"}}` + "\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	cfg := Config{Model: model, LLMMaxAttempts: 1, pdfArtifacts: artifacts}
	entry := newOpenAICompatEntry(cfg, providerOpenAI, "test-key", "", openai.WithHTTPClient(client))
	reg := &providerRegistry{entries: map[string]providerEntry{providerOpenAI: entry}, defaultID: providerOpenAI, defaultModel: model, meta: newLiveMetaStore()}
	reg.meta.Swap(map[string][]modelEntry{providerOpenAI: {{ID: model, InputModalities: []string{"text", "pdf"}}}})
	reg.remintEntry(providerOpenAI, model)
	entry, _ = reg.Lookup(providerOpenAI)
	if got := modelCapability(reg, providerOpenAI, model); !got.PDF {
		t.Fatalf("selected model capability = %+v, want PDF", got)
	}
	store := memstore.New()
	factory := sessionEngineFactory(cfg, reg, entry.provider, store,
		permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
		prompt.RootAssembler{}, catalogAssets{}, nil)
	result, err := factory(context.Background(), server.ProviderSelector{}, nil, server.ProfileDefault, "/ws", session.ModeDefault)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = result.Close() }()
	if !result.Capabilities.PDF {
		t.Fatal("selected factory did not advertise PDF input")
	}
	part, err := session.NewPDFContent(meta.ID, meta.Name, meta.Size, meta.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	sess := session.New("s-pdf", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Now())
	for range result.Engine.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "read", Parts: []session.Content{part}}).Events() {
	}
	wantData := "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(pdf)
	if !bytes.Contains(wire, []byte(wantData)) || !bytes.Contains(wire, []byte(`"type":"input_file"`)) {
		t.Fatalf("native provider did not receive PDF input: %s", wire)
	}
	if bytes.Contains(wire, []byte(meta.ID)) {
		t.Fatal("private artifact ID leaked to provider")
	}
	recorded, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range recorded.Conversation.Messages {
		if message.Role == session.RoleUser && len(message.Parts) > 0 {
			if got := message.Parts[0]; got.ArtifactID != meta.ID || len(got.Data) != 0 || got.Name != meta.Name || got.SHA256 != meta.SHA256 {
				t.Fatalf("durable prompt = %+v, want metadata-only reference", got)
			}
			return
		}
	}
	t.Fatal("PDF prompt missing from persisted conversation")
}

// TestSDKPDFArtifacts_Scenario1_SystemPromptAffordance pins AC1.4 at the real
// session factory. The assertion reads StablePrefix, where Role instructions
// live, rather than the rendered prompt's incidental inventory text.
func TestSDKPDFArtifacts_Scenario1_SystemPromptAffordance(t *testing.T) {
	for _, tc := range []struct {
		name       string
		modalities []string
		wantPDF    bool
	}{
		{name: "PDF-capable model", modalities: []string{"text", "pdf"}, wantPDF: true},
		{name: "text-only model", modalities: []string{"text"}, wantPDF: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const model = "test/pdf-model"
			var captured port.LLMRequest
			invoked := false
			provider := mockllm.NewWith([]mockllm.Option{
				mockllm.WithCapabilities(port.ProviderCapabilities{PDF: true}),
				mockllm.WithRequestObserver(func(req port.LLMRequest) { captured, invoked = req, true }),
			}, mockllm.TextTurn("ok"))
			reg := regForTest(provider, providerOpenAI, model)
			meta := newLiveMetaStore()
			meta.Swap(map[string][]modelEntry{providerOpenAI: {{ID: model, InputModalities: tc.modalities}}})
			reg.meta = meta
			factory := sessionEngineFactory(Config{Model: model, pdfArtifacts: &pdfLifecycleFixture{}}, reg, provider, memstore.New(),
				permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
				prompt.RootAssembler{}, catalogAssets{}, nil)
			res, err := factory(context.Background(), server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
			if err != nil {
				t.Fatalf("factory: %v", err)
			}
			defer func() { _ = res.Close() }()
			sess := session.New("s-pdf", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Now())
			for range res.Engine.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "hi"}).Events() {
			}
			if !invoked {
				t.Fatal("factory-built engine did not invoke provider")
			}
			if got := modelCapability(reg, providerOpenAI, model).PDF; got != tc.wantPDF {
				t.Fatalf("selected model PDF capability = %t, want %t", got, tc.wantPDF)
			}
			if got := strings.Contains(captured.System.StablePrefix, pdfAttachmentPostureNote); got != tc.wantPDF {
				t.Fatalf("PDF attachment instruction present = %t, want %t", got, tc.wantPDF)
			}
		})
	}
}
