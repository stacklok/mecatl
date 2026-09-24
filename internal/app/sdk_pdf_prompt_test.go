package app

import (
	"context"
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
)

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
