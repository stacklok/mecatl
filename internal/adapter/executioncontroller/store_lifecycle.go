//nolint:revive // Private store methods implement adapter-only lifecycle interfaces.
package executioncontroller

import (
	"context"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/stacklok/mecatl/internal/executionenv"
)

type referenceRecord struct {
	BindingID       string
	State           executionenv.ReferenceState
	OperationID     string
	SourceBindingID string
	CreatedAt       time.Time
}

func referenceRecords(o *unstructured.Unstructured) ([]referenceRecord, error) {
	raw, found, err := unstructured.NestedSlice(o.Object, "status", "references")
	if err != nil || !found {
		return nil, &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment references are invalid"}
	}
	out := make([]referenceRecord, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, &executionenv.Error{Code: executionenv.CodeConflict, Message: "legacy environment references require explicit migration"}
		}
		created, parseErr := time.Parse(time.RFC3339Nano, text(m, "createdAt"))
		r := referenceRecord{BindingID: text(m, "bindingID"), State: executionenv.ReferenceState(text(m, "state")), OperationID: text(m, operationIDField), SourceBindingID: text(m, "sourceBindingID"), CreatedAt: created}
		if parseErr != nil || r.BindingID == "" || r.OperationID == "" || (r.State != executionenv.ReferencePendingCreate && r.State != executionenv.ReferencePublished && r.State != executionenv.ReferencePendingDelete) {
			return nil, &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment references are invalid"}
		}
		out = append(out, r)
	}
	return out, nil
}

func setReferenceRecords(o *unstructured.Unstructured, refs []referenceRecord) error {
	sort.Slice(refs, func(i, j int) bool { return refs[i].BindingID < refs[j].BindingID })
	raw := make([]any, len(refs))
	for i, r := range refs {
		raw[i] = map[string]any{"bindingID": r.BindingID, "state": string(r.State), operationIDField: r.OperationID, "sourceBindingID": r.SourceBindingID, "createdAt": r.CreatedAt.UTC().Format(time.RFC3339Nano)}
	}
	return unstructured.SetNestedSlice(o.Object, raw, "status", "references")
}

func referenceOperationMatches(o *unstructured.Unstructured, binding, operation string) bool {
	refs, err := referenceRecords(o)
	if err != nil {
		return false
	}
	for _, r := range refs {
		if r.BindingID == binding {
			return r.OperationID == operation && (r.State == executionenv.ReferencePendingCreate || r.State == executionenv.ReferencePublished)
		}
	}
	return false
}

func attachedReference(refs []referenceRecord, binding string) bool {
	for _, r := range refs {
		if r.BindingID == binding && (r.State == executionenv.ReferencePendingCreate || r.State == executionenv.ReferencePublished) {
			return true
		}
	}
	return false
}

func publishedReference(refs []referenceRecord, binding string) bool {
	for _, r := range refs {
		if r.BindingID == binding && r.State == executionenv.ReferencePublished {
			return true
		}
	}
	return false
}

func (s *Store) mutateReference(ctx context.Context, ref executionenv.EnvironmentRef, client, owner, binding, operation string, from, to executionenv.ReferenceState, remove bool) error {
	return s.retryUpdateStatus(ctx, ref.ID, func(o *unstructured.Unstructured) error {
		if textNested(o.Object, "status", "lifecycleOperation", "id") != "" {
			return &executionenv.Error{Code: executionenv.CodeNotReady, Message: transitioningMessage}
		}
		if textNested(o.Object, "spec", "ownerHash") != owner || textNested(o.Object, "spec", "clientHash") != hashText(client) || textNested(o.Object, "spec", "revision") != ref.Revision {
			return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
		}
		refs, err := referenceRecords(o)
		if err != nil {
			return err
		}
		for i := range refs {
			if refs[i].BindingID != binding {
				continue
			}
			if refs[i].OperationID == operation && refs[i].State == to {
				return nil
			}
			if refs[i].OperationID == operation && refs[i].State == from {
				if remove {
					refs = append(refs[:i], refs[i+1:]...)
				} else {
					refs[i].State = to
				}
				return setReferenceRecords(o, refs)
			}
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "reference transaction mismatch"}
		}
		if remove {
			return nil
		}
		return &executionenv.Error{Code: executionenv.CodeNotFound, Message: "reference not found"}
	})
}

func (s *Store) CommitReference(ctx context.Context, ref executionenv.EnvironmentRef, client, owner, binding, operation string) error {
	return s.mutateReference(ctx, ref, client, owner, binding, operation, executionenv.ReferencePendingCreate, executionenv.ReferencePublished, false)
}
func (s *Store) AbortReference(ctx context.Context, ref executionenv.EnvironmentRef, client, owner, binding, operation string) error {
	return s.mutateReference(ctx, ref, client, owner, binding, operation, executionenv.ReferencePendingCreate, "", true)
}
func (s *Store) PrepareReferenceDelete(ctx context.Context, ref executionenv.EnvironmentRef, client, owner, binding, operation string) error {
	return s.retryUpdateStatus(ctx, ref.ID, func(o *unstructured.Unstructured) error {
		if textNested(o.Object, "status", "lifecycleOperation", "id") != "" {
			return &executionenv.Error{Code: executionenv.CodeNotReady, Message: transitioningMessage}
		}
		if textNested(o.Object, "spec", "ownerHash") != owner || textNested(o.Object, "spec", "clientHash") != hashText(client) || textNested(o.Object, "spec", "revision") != ref.Revision {
			return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
		}
		refs, err := referenceRecords(o)
		if err != nil {
			return err
		}
		for i := range refs {
			if refs[i].BindingID != binding {
				continue
			}
			if refs[i].State == executionenv.ReferencePendingDelete && refs[i].OperationID == operation {
				return nil
			}
			if refs[i].State != executionenv.ReferencePublished {
				return &executionenv.Error{Code: executionenv.CodeConflict, Message: "reference transaction mismatch"}
			}
			refs[i].State, refs[i].OperationID, refs[i].CreatedAt = executionenv.ReferencePendingDelete, operation, time.Now().UTC()
			return setReferenceRecords(o, refs)
		}
		return &executionenv.Error{Code: executionenv.CodeNotFound, Message: "reference not found"}
	})
}
func (s *Store) ConfirmReferenceDelete(ctx context.Context, ref executionenv.EnvironmentRef, client, owner, binding, operation string) error {
	return s.mutateReference(ctx, ref, client, owner, binding, operation, executionenv.ReferencePendingDelete, "", true)
}
func (s *Store) CancelReferenceDelete(ctx context.Context, ref executionenv.EnvironmentRef, client, owner, binding, operation string) error {
	return s.mutateReference(ctx, ref, client, owner, binding, operation, executionenv.ReferencePendingDelete, executionenv.ReferencePublished, false)
}

func (s *Store) ReserveSuccessor(ctx context.Context, ref executionenv.EnvironmentRef, client, owner, source, destination, operation string) error {
	return s.retryUpdateStatus(ctx, ref.ID, func(o *unstructured.Unstructured) error {
		if textNested(o.Object, "status", "lifecycleOperation", "id") != "" {
			return &executionenv.Error{Code: executionenv.CodeNotReady, Message: transitioningMessage}
		}
		if textNested(o.Object, "spec", "ownerHash") != owner || textNested(o.Object, "spec", "clientHash") != hashText(client) || textNested(o.Object, "spec", "revision") != ref.Revision {
			return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
		}
		refs, err := referenceRecords(o)
		if err != nil {
			return err
		}
		if !publishedReference(refs, source) {
			return &executionenv.Error{Code: executionenv.CodeNotFound, Message: "source reference not found"}
		}
		if textNested(o.Object, "status", "activeOperation", "id") != "" || textNested(o.Object, "status", "activeRun", "claimID") != "" || textNested(o.Object, "status", "fenceState") != fenceHealthy {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment is not quiescent"}
		}
		for _, r := range refs {
			if r.BindingID == destination {
				if r.State == executionenv.ReferencePendingCreate && r.OperationID == operation && r.SourceBindingID == source {
					return nil
				}
				return &executionenv.Error{Code: executionenv.CodeConflict, Message: "destination reference conflicts"}
			}
		}
		if len(refs) >= maxReferences {
			return &executionenv.Error{Code: executionenv.CodeResourceExhausted, Message: "environment reference limit reached"}
		}
		refs = append(refs, referenceRecord{BindingID: destination, State: executionenv.ReferencePendingCreate, OperationID: operation, SourceBindingID: source, CreatedAt: time.Now().UTC()})
		return setReferenceRecords(o, refs)
	})
}

func (s *Store) FindReferenceIntent(ctx context.Context, ref executionenv.EnvironmentRef, client, owner, binding string) (executionenv.ReferenceIntent, error) {
	o, err := s.resources.Get(ctx, ref.ID, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return executionenv.ReferenceIntent{}, &executionenv.Error{Code: executionenv.CodeNotFound, Message: "reference intent not found"}
	}
	if err != nil {
		return executionenv.ReferenceIntent{}, err
	}
	if textNested(o.Object, "spec", "revision") != ref.Revision || textNested(o.Object, "spec", "ownerHash") != owner || textNested(o.Object, "spec", "clientHash") != hashText(client) {
		return executionenv.ReferenceIntent{}, &executionenv.Error{Code: executionenv.CodeNotFound, Message: "reference intent not found"}
	}
	refs, err := referenceRecords(o)
	if err != nil {
		return executionenv.ReferenceIntent{}, err
	}
	for _, r := range refs {
		if r.BindingID == binding && r.State != executionenv.ReferencePublished {
			return executionenv.ReferenceIntent{Environment: ref, BindingID: r.BindingID, State: r.State, OperationID: r.OperationID, SourceBindingID: r.SourceBindingID, CreatedAt: r.CreatedAt}, nil
		}
	}
	return executionenv.ReferenceIntent{}, &executionenv.Error{Code: executionenv.CodeNotFound, Message: "reference intent not found"}
}

func (s *Store) ListReferenceIntentsForClient(ctx context.Context, client string, limit int) ([]executionenv.ReferenceIntent, error) {
	if limit <= 0 || limit > maxReferences {
		limit = maxReferences
	}
	list, err := s.resources.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]executionenv.ReferenceIntent, 0)
	for i := range list.Items {
		o := &list.Items[i]
		if textNested(o.Object, "spec", "clientHash") != hashText(client) {
			continue
		}
		owner := executionenv.Owner{Issuer: textNested(o.Object, "spec", "ownerIssuer"), Subject: textNested(o.Object, "spec", "ownerSubject")}
		if owner.Issuer == "" || owner.Subject == "" || ownerHash(owner) != textNested(o.Object, "spec", "ownerHash") {
			return nil, &executionenv.Error{Code: executionenv.CodeConflict, Message: "reference intent owner attestation is unavailable"}
		}
		refs, parseErr := referenceRecords(o)
		if parseErr != nil {
			return nil, parseErr
		}
		for _, r := range refs {
			if r.State == executionenv.ReferencePublished {
				continue
			}
			out = append(out, executionenv.ReferenceIntent{Environment: executionenv.EnvironmentRef{ID: o.GetName(), Revision: textNested(o.Object, "spec", "revision")}, Owner: owner, BindingID: r.BindingID, State: r.State, OperationID: r.OperationID, SourceBindingID: r.SourceBindingID, CreatedAt: r.CreatedAt})
			if len(out) == limit {
				return out, nil
			}
		}
	}
	return out, nil
}

func (s *Store) ListReferenceIntents(ctx context.Context, client, owner string, limit int) ([]executionenv.ReferenceIntent, error) {
	if limit <= 0 || limit > maxReferences {
		limit = maxReferences
	}
	list, err := s.resources.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]executionenv.ReferenceIntent, 0)
	for i := range list.Items {
		o := &list.Items[i]
		if textNested(o.Object, "spec", "ownerHash") != owner || textNested(o.Object, "spec", "clientHash") != hashText(client) {
			continue
		}
		refs, parseErr := referenceRecords(o)
		if parseErr != nil {
			return nil, parseErr
		}
		for _, r := range refs {
			if r.State == executionenv.ReferencePublished {
				continue
			}
			out = append(out, executionenv.ReferenceIntent{Environment: executionenv.EnvironmentRef{ID: o.GetName(), Revision: textNested(o.Object, "spec", "revision")}, BindingID: r.BindingID, State: r.State, OperationID: r.OperationID, SourceBindingID: r.SourceBindingID, CreatedAt: r.CreatedAt})
			if len(out) == limit {
				return out, nil
			}
		}
	}
	return out, nil
}
