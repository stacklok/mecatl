package client

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type executionCatalogClient struct{ *fakeModelsClient }

func (*executionCatalogClient) ListExecutionTemplates(context.Context, *mecatlv1.ListExecutionTemplatesRequest, ...grpc.CallOption) (*mecatlv1.ListExecutionTemplatesResponse, error) {
	return &mecatlv1.ListExecutionTemplatesResponse{Items: []*mecatlv1.ExecutionTemplateInfo{{Template: &mecatlv1.ExecutionTemplate{Id: "files-only", Revision: "revision"}, DeclaredExecutionFiles: true, DeclaredBuiltInShell: false}}}, nil
}

func TestExecutionCatalogDeclaredAffordances(t *testing.T) {
	fake := &executionCatalogClient{&fakeModelsClient{caps: &mecatlv1.ServerCapabilities{ExecutionTemplates: true}, features: []string{"execution_templates"}}}
	inventory, err := newFakeClient(fake).ListExecutionTemplates(t.Context())
	if err != nil || len(inventory.Items) != 1 || !inventory.Items[0].DeclaredExecutionFiles || inventory.Items[0].DeclaredBuiltInShell {
		t.Fatalf("catalog projection=%+v, %v", inventory, err)
	}
	if fake.lastCreate != nil {
		t.Fatal("catalog listing created a session")
	}
}

func TestExplicitExecutionRejectedBeforeCreateOnOldServer(t *testing.T) {
	fake := &fakeModelsClient{caps: &mecatlv1.ServerCapabilities{}}
	client := newFakeClient(fake)
	for _, choice := range []ExecutionChoice{{None: true}, {TemplateID: "go", Revision: "v1-rev"}} {
		if _, _, _, err := client.CreateSessionWithExecution(t.Context(), 0, ModelSelection{}, choice); !errors.Is(err, ErrExecutionTemplatesDisabled) || fake.lastCreate != nil {
			t.Fatalf("old server accepted %+v: %v", choice, err)
		}
	}
	fake.features = []string{"execution_templates"}
	if _, _, _, err := client.CreateSessionWithExecution(t.Context(), 0, ModelSelection{}, ExecutionChoice{None: true}); err != nil {
		t.Fatalf("none must work without catalog capability: %v", err)
	}
	fake.lastCreate = nil
	if _, _, _, err := client.CreateSessionWithExecution(t.Context(), 0, ModelSelection{}, ExecutionChoice{TemplateID: "go", Revision: "v1-rev"}); !errors.Is(err, ErrExecutionTemplatesDisabled) || fake.lastCreate != nil {
		t.Fatalf("disabled catalog admitted template: %v", err)
	}
}

func TestCreateSessionExecutionChoice(t *testing.T) {
	fake := &fakeModelsClient{caps: &mecatlv1.ServerCapabilities{ExecutionTemplates: true}, features: []string{"execution_templates"}}
	client := newFakeClient(fake)
	for _, choice := range []ExecutionChoice{{None: true}, {TemplateID: "coding", Revision: "v1-revision"}} {
		if _, _, _, err := client.CreateSessionWithExecution(t.Context(), 0, ModelSelection{}, choice); err != nil {
			t.Fatal(err)
		}
		if choice.None && fake.lastCreate.GetExecution().GetNone() == nil || !choice.None && fake.lastCreate.GetExecution().GetTemplate().GetId() != choice.TemplateID {
			t.Fatalf("wire choice=%v", fake.lastCreate.GetExecution())
		}
	}
	before := fake.lastCreate
	for _, invalid := range []ExecutionChoice{{}, {None: true, TemplateID: "coding", Revision: "revision"}, {TemplateID: "coding"}} {
		if _, _, _, err := client.CreateSessionWithExecution(context.Background(), 0, ModelSelection{}, invalid); err == nil || fake.lastCreate != before {
			t.Fatalf("invalid choice=%+v err=%v", invalid, err)
		}
	}
}
