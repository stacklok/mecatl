package client

import (
	"context"
	"errors"
	"fmt"
	"slices"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// ExecutionChoice is a client-side, path-free selection. Nil uses the default.
type ExecutionChoice struct {
	None       bool
	TemplateID string
	Revision   string
}

// CreateSessionWithExecution binds a no-filesystem session or exact template.
// Existing CreateSession still selects the deployment default.
func (c *Client) CreateSessionWithExecution(ctx context.Context, mode mecatlv1.PermissionMode, model ModelSelection, choice ExecutionChoice) (string, Capabilities, ResolvedModel, error) {
	var selection *mecatlv1.ExecutionSelection
	switch {
	case choice.None && choice.TemplateID == "" && choice.Revision == "":
		selection = &mecatlv1.ExecutionSelection{None: &mecatlv1.ExecutionNone{}}
	case !choice.None && choice.TemplateID != "" && choice.Revision != "":
		selection = &mecatlv1.ExecutionSelection{Template: &mecatlv1.ExecutionTemplate{Id: choice.TemplateID, Revision: choice.Revision}}
	default:
		return "", Capabilities{}, ResolvedModel{}, errors.New("execution must select none or an exact template")
	}
	info, err := c.svc.GetCompatibilityInfo(ctx, &mecatlv1.GetCompatibilityInfoRequest{})
	if err != nil {
		return "", Capabilities{}, ResolvedModel{}, fmt.Errorf("execution selection compatibility: %w", err)
	}
	if !slices.Contains(info.GetFeatures(), "execution_templates") || (!choice.None && !info.GetCapabilities().GetExecutionTemplates()) {
		return "", Capabilities{}, ResolvedModel{}, ErrExecutionTemplatesDisabled
	}
	return c.createSession(ctx, &mecatlv1.CreateSessionRequest{Mode: mode, ProviderId: model.ProviderID, ModelId: model.ModelID, ReasoningEffort: model.ReasoningEffort, Execution: selection})
}

// ExecutionTemplate is inert display data, not a reusable placement authority.
type ExecutionTemplate struct {
	ID, Revision, Name, Description, DisplayToken string
	Extensions                                    map[string]string
}

// ExecutionTemplateInventory is a caller-filtered, bounded catalog snapshot.
type ExecutionTemplateInventory struct {
	Items    []ExecutionTemplate
	Revision string
}

// ErrExecutionTemplatesDisabled reports that feature and capability negotiation
// did not enable authenticated catalog discovery.
var ErrExecutionTemplatesDisabled = errors.New("execution templates are disabled")

// ListExecutionTemplates requests the optional authenticated catalog without
// creating a session; the server rechecks eligibility on every bind.
func (c *Client) ListExecutionTemplates(ctx context.Context) (ExecutionTemplateInventory, error) {
	info, err := c.svc.GetCompatibilityInfo(ctx, &mecatlv1.GetCompatibilityInfoRequest{})
	if err != nil {
		return ExecutionTemplateInventory{}, fmt.Errorf("execution templates compatibility: %w", err)
	}
	if !info.GetCapabilities().GetExecutionTemplates() || !slices.Contains(info.GetFeatures(), "execution_templates") {
		return ExecutionTemplateInventory{}, ErrExecutionTemplatesDisabled
	}
	response, err := c.svc.ListExecutionTemplates(ctx, &mecatlv1.ListExecutionTemplatesRequest{})
	if err != nil {
		return ExecutionTemplateInventory{}, fmt.Errorf("list execution templates: %w", err)
	}
	if len(response.GetItems()) > 64 {
		return ExecutionTemplateInventory{}, errors.New("execution template inventory exceeds bound")
	}
	out := ExecutionTemplateInventory{Items: make([]ExecutionTemplate, 0, len(response.GetItems())), Revision: response.GetInventoryRevision()}
	for _, item := range response.GetItems() {
		extensions := make(map[string]string, len(item.GetExtensions()))
		for key, value := range item.GetExtensions() {
			extensions[key] = value
		}
		out.Items = append(out.Items, ExecutionTemplate{ID: item.GetTemplate().GetId(), Revision: item.GetTemplate().GetRevision(), Name: item.GetName(), Description: item.GetDescription(), DisplayToken: item.GetDisplayToken(), Extensions: extensions})
	}
	return out, nil
}
