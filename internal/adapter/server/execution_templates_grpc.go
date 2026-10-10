package server

import (
	"context"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// ListExecutionTemplates returns the caller's execution template catalog.
func (h *HarnessServer) ListExecutionTemplates(ctx context.Context, _ *mecatlv1.ListExecutionTemplatesRequest) (*mecatlv1.ListExecutionTemplatesResponse, error) {
	items, revision, err := h.svc.ListExecutionTemplates(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	out := &mecatlv1.ListExecutionTemplatesResponse{InventoryRevision: revision}
	for _, item := range items {
		out.Items = append(out.Items, &mecatlv1.ExecutionTemplateInfo{Template: &mecatlv1.ExecutionTemplate{Id: item.ID, Revision: item.Revision}, Name: item.Name, Description: item.Description, DisplayToken: item.DisplayToken, Extensions: item.Extensions, DeclaredExecutionFiles: item.DeclaredExecutionFiles, DeclaredBuiltInShell: item.DeclaredBuiltInShell})
	}
	return out, nil
}
