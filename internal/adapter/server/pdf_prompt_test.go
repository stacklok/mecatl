package server_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type pdfPromptLifecycle struct {
	meta     server.Artifact
	resolved int
}

func (*pdfPromptLifecycle) Stage(context.Context, session.SessionID, string, string, io.Reader) (server.Artifact, error) {
	return server.Artifact{}, nil
}
func (p *pdfPromptLifecycle) Resolve(_ context.Context, id session.SessionID, artifactID string) (server.Artifact, error) {
	p.resolved++
	if id != "s-pdf" || artifactID != p.meta.ID {
		return server.Artifact{}, server.ErrNotFound
	}
	return p.meta, nil
}
func (*pdfPromptLifecycle) Open(context.Context, session.SessionID, string) (server.Artifact, io.ReadCloser, error) {
	return server.Artifact{}, nil, errors.New("unused")
}
func (*pdfPromptLifecycle) CommitPrompt(context.Context, session.SessionID, []string) error {
	return nil
}
func (*pdfPromptLifecycle) CopyFork(context.Context, session.SessionID, session.SessionID, []session.Message) ([]session.Message, error) {
	return nil, nil
}
func (*pdfPromptLifecycle) DiscardUnpublished(context.Context, session.SessionID) error { return nil }
func (*pdfPromptLifecycle) Reconcile(context.Context) error                             { return nil }

func TestPDFPromptReferencesResolveBeforeRecording(t *testing.T) {
	const digest = "acffdf49b58d86b2a91341e976081848a03823302662a85d8ec0b27d89e8db75"
	lifecycle := &pdfPromptLifecycle{meta: server.Artifact{ID: "artifact", Name: "report.pdf", MIMEType: "application/pdf", Size: 14, SHA256: digest}}
	llm := mockllm.New(mockllm.TextTurn("done"))
	store := memstore.New()
	engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(allowRules(), nil), Store: store})
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: engine, Store: store, PlacementProvider: testPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "s-pdf" }, Artifacts: lifecycle,
		DefaultCapabilities: port.ProviderCapabilities{PDF: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	raw := session.Content{Kind: session.MediaPDF, MIMEType: "application/pdf", ArtifactID: "artifact"}
	if _, err := svc.StartRunContent(context.Background(), sess.ID, "read", []session.Content{{Kind: session.MediaPDF, MIMEType: "application/pdf", ArtifactID: "foreign"}}); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("foreign artifact = %v, want concealed not-found", err)
	}
	if llm.Calls() != 0 {
		t.Fatal("rejected PDF started model call")
	}
	run, err := svc.StartRunContent(context.Background(), sess.ID, "read", []session.Content{raw})
	if err != nil {
		t.Fatal(err)
	}
	var events []session.Event
	for event := range run.Events() {
		events = append(events, event)
	}
	svc.FinishRun(sess.ID, run)
	reloaded, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got session.Content
	for _, message := range reloaded.Conversation.Messages {
		if message.Role == session.RoleUser && len(message.Parts) != 0 {
			got = message.Parts[0]
		}
	}
	if got.ArtifactID != "artifact" || got.Name != "report.pdf" || got.Size != 14 || got.SHA256 != digest || len(got.Data) != 0 || !strings.EqualFold(got.MIMEType, "application/pdf") {
		t.Fatalf("recorded PDF reference = %+v; history=%+v events=%+v", got, reloaded.Conversation.Messages, events)
	}
}
