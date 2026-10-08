package executioncontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
)

// templateBackend is opt-in. Without a versioned registry the private catalog
// and exact selection are unavailable; no legacy default can satisfy them.
type templateBackend interface {
	CatalogTemplates() []*executionv1.TemplateCatalogItem
	ValidateTemplate(context.Context, string, string) (Profile, error)
	EnsurePendingOwnedRevision(context.Context, string, string, executionenv.Owner, string, string, string, string, string) (Allocation, error)
}

func templateAllowed(c authenticatedClient, id string) bool {
	return c.policy.MayAttestOwner && slices.Contains(c.policy.ExecutionTemplates, id)
}

// CatalogTemplates returns all selectable template revisions.
func (s *Store) CatalogTemplates() []*executionv1.TemplateCatalogItem {
	var items []*executionv1.TemplateCatalogItem
	for id, revisions := range s.profiles.revisions {
		for revision := range revisions {
			if _, ok := s.profiles.selectRevision(id, revision); !ok {
				continue
			}
			display := s.profiles.catalog[id][revision]
			raw, _ := json.Marshal(display) // validated at load, JSON encoding cannot fail
			sum := sha256.Sum256(raw)
			extensions := make(map[string]string)
			for namespace, fields := range display.Extensions {
				for key, value := range fields {
					extensions[namespace+"/"+key] = value
				}
			}
			items = append(items, &executionv1.TemplateCatalogItem{Template: &executionv1.TemplateSelector{Id: id, Revision: revision}, Name: display.Name, Description: display.Description, DisplayToken: "v1-" + hex.EncodeToString(sum[:]), Extensions: extensions})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Template.Id == items[j].Template.Id {
			return items[i].Template.Revision < items[j].Template.Revision
		}
		return items[i].Template.Id < items[j].Template.Id
	})
	return items
}

// ValidateTemplate resolves one exact selectable template revision.
func (s *Store) ValidateTemplate(_ context.Context, id, revision string) (Profile, error) {
	p, ok := s.profiles.selectRevision(id, revision)
	if !ok {
		return Profile{}, &executionenv.Error{Code: executionenv.CodeNotFound, Message: "execution template not found"}
	}
	return Profile{Name: id, Digest: p.Digest, MaxFileBytes: p.Spec.MaxFileBytes, MaxCommandBytes: p.Spec.MaxCommandBytes, MaxCommandDuration: p.Spec.MaxCommandDuration, Capabilities: []string{"filesystem", "foreground-command"}}, nil
}

// ListExecutionTemplates returns the caller's allowed selectable template catalog.
func (h *Handler) ListExecutionTemplates(ctx context.Context, _ *executionv1.ListExecutionTemplatesRequest) (*executionv1.ListExecutionTemplatesResponse, error) {
	c, err := h.client(ctx)
	if err != nil {
		return nil, err
	}
	backend, ok := h.backend.(templateBackend)
	if !ok {
		return nil, wireError(executionenv.CodeNotReady, false)
	}
	items := make([]*executionv1.TemplateCatalogItem, 0)
	for _, item := range backend.CatalogTemplates() {
		if templateAllowed(c, item.GetTemplate().GetId()) {
			items = append(items, item)
		}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return nil, wireError(executionenv.CodeInternal, false)
	}
	sum := sha256.Sum256(raw)
	return &executionv1.ListExecutionTemplatesResponse{Items: items, InventoryRevision: "v1-" + hex.EncodeToString(sum[:])}, nil
}

// ValidateTemplate resolves a caller-authorized exact template revision.
func (h *Handler) ValidateTemplate(ctx context.Context, q *executionv1.ValidateTemplateRequest) (*executionv1.ValidateTemplateResponse, error) {
	c, err := h.client(ctx)
	if err != nil {
		return nil, err
	}
	selector := q.GetTemplate()
	if selector == nil || selector.Id == "" || len(selector.Id) > 63 || len(selector.Revision) != 67 || !templateAllowed(c, selector.Id) {
		return nil, wireError(executionenv.CodeNotFound, false)
	}
	backend, ok := h.backend.(templateBackend)
	if !ok {
		return nil, wireError(executionenv.CodeNotReady, false)
	}
	p, err := backend.ValidateTemplate(ctx, selector.Id, selector.Revision)
	if err != nil {
		return nil, backendError(err)
	}
	return &executionv1.ValidateTemplateResponse{Template: selector, Capabilities: p.Capabilities, MaxFileBytes: p.MaxFileBytes, MaxCommandBytes: p.MaxCommandBytes, MaxCommandDurationMillis: p.MaxCommandDuration.Milliseconds()}, nil
}

// EnsureTemplate allocates a caller-authorized exact template revision.
func (h *Handler) EnsureTemplate(ctx context.Context, q *executionv1.EnsureTemplateRequest) (*executionv1.EnsureEnvironmentResponse, error) {
	c, err := h.client(ctx)
	if err != nil {
		return nil, err
	}
	selector := q.GetTemplate()
	if selector == nil || selector.Id == "" || len(selector.Id) > 63 || len(selector.Revision) != 67 || !templateAllowed(c, selector.Id) {
		return nil, wireError(executionenv.CodeNotFound, false)
	}
	owner, valid := ownerFromProto(q.GetOwner())
	if !valid || !validBinding(q.GetBindingId()) || !validOperationID(q.GetOperationId()) {
		return nil, wireError(executionenv.CodeInvalidArgument, false)
	}
	backend, ok := h.backend.(templateBackend)
	if !ok {
		return nil, wireError(executionenv.CodeNotReady, false)
	}
	// Authorization and revision eligibility are checked again by Ensure's store
	// immediately before allocation, not inferred from a previous catalog read.
	fp := fingerprint(c.id, ownerHash(owner), q.BindingId, selector.Id, selector.Revision)
	a, err := backend.EnsurePendingOwnedRevision(ctx, c.id, ownerHash(owner), owner, q.BindingId, selector.Id, selector.Revision, fp, q.OperationId)
	if err != nil {
		return nil, backendError(err)
	}
	return ensureResponse(a), nil
}
