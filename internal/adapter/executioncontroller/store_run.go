//nolint:revive // Private store methods implement adapter-only lifecycle interfaces.
package executioncontroller

import (
	"context"
	"math"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func boundedRunTTL(ttl time.Duration) (time.Duration, error) {
	if ttl < executionenv.MinRunTTL || ttl > executionenv.MaxRunTTL {
		return 0, &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "run claim ttl is outside the operator bound"}
	}
	return ttl, nil
}

func generationMatches(o *unstructured.Unstructured, generation uint64) bool {
	return generation > 0 && generation <= math.MaxInt64 && intNested(o.Object, "status", "grantGeneration") == int64(generation)
}

// ValidateRunClaim rechecks durable generation and ownership before any grant-authorized operation.
func (s *Store) ValidateRunClaim(ctx context.Context, client, owner string, rc executionenv.RequestContext) error {
	if rc.Epoch == 0 || rc.Epoch > math.MaxInt64 || rc.GrantGeneration == 0 || rc.GrantGeneration > math.MaxInt64 {
		return &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "run claim identity is invalid"}
	}
	o, err := s.resources.Get(ctx, rc.Environment.ID, metav1.GetOptions{})
	if err != nil {
		return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
	}
	if err := requireCurrentSchema(o); err != nil {
		return err
	}
	claim, _, expiry, ok := activeRunFrom(o)
	if !ok || !s.now().Before(expiry) || claim.Environment != rc.Environment || claim.BindingID != rc.BindingID || claim.RunID != rc.RunID || claim.ClaimID != rc.ClaimID || claim.Epoch != rc.Epoch || claim.GrantGeneration != rc.GrantGeneration || !generationMatches(o, rc.GrantGeneration) {
		return &executionenv.Error{Code: executionenv.CodeConflict, Message: "run claim is not current"}
	}
	if textNested(o.Object, "spec", "ownerHash") != owner || textNested(o.Object, "spec", "clientHash") != hashText(client) {
		return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
	}
	return nil
}

func activeRunFrom(o *unstructured.Unstructured) (executionenv.RunClaim, string, time.Time, bool) {
	m, found, _ := unstructured.NestedMap(o.Object, "status", "activeRun")
	if !found {
		return executionenv.RunClaim{}, "", time.Time{}, false
	}
	expires, err := time.Parse(time.RFC3339Nano, text(m, "expiresAt"))
	epoch, epochOK := epochValue(o)
	generationValue := intNested(m, "grantGeneration")
	if generationValue <= 0 {
		return executionenv.RunClaim{}, "", time.Time{}, false
	}
	generation := uint64(generationValue) //nolint:gosec // positivity is checked immediately above.
	claim := executionenv.RunClaim{Environment: executionenv.EnvironmentRef{ID: o.GetName(), Revision: textNested(o.Object, "spec", "revision")}, BindingID: text(m, "bindingID"), RunID: text(m, "runID"), ClaimID: text(m, "claimID"), Epoch: epoch, GrantGeneration: generation, ExpiresAt: expires}
	return claim, text(m, operationIDField), expires, err == nil && epochOK && generation > 0 && claim.BindingID != "" && claim.RunID != "" && claim.ClaimID != ""
}

//nolint:gocyclo // Run ownership admission keeps every CAS precondition in one auditable transition.
func (s *Store) AcquireRun(ctx context.Context, ref executionenv.EnvironmentRef, client, owner, binding, runID, operationID string, ttl time.Duration) (executionenv.RunClaim, error) {
	ttl, err := boundedRunTTL(ttl)
	if err != nil {
		return executionenv.RunClaim{}, err
	}
	var out executionenv.RunClaim
	err = s.retryUpdateStatus(ctx, ref.ID, func(o *unstructured.Unstructured) error {
		if textNested(o.Object, "status", "lifecycleOperation", "id") != "" {
			return &executionenv.Error{Code: executionenv.CodeNotReady, Message: transitioningMessage}
		}
		if textNested(o.Object, "spec", "ownerHash") != owner || textNested(o.Object, "spec", "clientHash") != hashText(client) || textNested(o.Object, "spec", "revision") != ref.Revision {
			return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
		}
		refs, parseErr := referenceRecords(o)
		if parseErr != nil {
			return parseErr
		}
		if !publishedReference(refs, binding) {
			return &executionenv.Error{Code: executionenv.CodeNotFound, Message: "published reference not found"}
		}
		if textNested(o.Object, "spec", "desired") != "Active" || !conditionTrue(o, "Ready") || textNested(o.Object, "status", "fenceState") != fenceHealthy {
			return &executionenv.Error{Code: executionenv.CodeNotReady, Message: "environment is not ready"}
		}
		if current, currentOp, expires, found := activeRunFrom(o); found {
			if current.BindingID == binding && current.RunID == runID && currentOp == operationID {
				if !generationMatches(o, current.GrantGeneration) {
					return &executionenv.Error{Code: executionenv.CodeConflict, Message: "run claim was revoked"}
				}
				out = current
				return nil
			}
			if s.now().Before(expires) {
				return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment has an active run"}
			}
			if textNested(o.Object, "status", "activeOperation", "id") != "" {
				return &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "expired run still has an active operation"}
			}
		}
		if textNested(o.Object, "status", "activeOperation", "id") != "" {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment has an active operation"}
		}
		epoch, ok := epochValue(o)
		if !ok || epoch >= math.MaxInt64 {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment epoch is invalid"}
		}
		epoch++
		epochStatus := int64(epoch) //nolint:gosec // the persisted epoch was checked below MaxInt64 before increment.
		claimID, idErr := randomID()
		if idErr != nil {
			return idErr
		}
		expires := s.now().Add(ttl)
		generation := intNested(o.Object, "status", "grantGeneration")
		if generation <= 0 {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "grant generation is invalid"}
		}
		if setErr := unstructured.SetNestedField(o.Object, epochStatus, "status", "epoch"); setErr != nil {
			return setErr
		}
		if setErr := unstructured.SetNestedMap(o.Object, map[string]any{"bindingID": binding, "runID": runID, "claimID": claimID, operationIDField: operationID, "ownerHash": owner, "clientHash": hashText(client), "epoch": epochStatus, "grantGeneration": generation, "expiresAt": expires.Format(time.RFC3339Nano)}, "status", "activeRun"); setErr != nil {
			return setErr
		}
		out = executionenv.RunClaim{Environment: ref, BindingID: binding, RunID: runID, ClaimID: claimID, Epoch: epoch, GrantGeneration: uint64(generation), ExpiresAt: expires}
		return nil
	})
	return out, err
}

func (s *Store) RenewRun(ctx context.Context, ref executionenv.EnvironmentRef, client, owner string, req executionenv.RunClaimRequest) (executionenv.RunClaim, error) {
	ttl, err := boundedRunTTL(req.TTL)
	if err != nil {
		return executionenv.RunClaim{}, err
	}
	var out executionenv.RunClaim
	err = s.retryUpdateStatus(ctx, ref.ID, func(o *unstructured.Unstructured) error {
		cur, _, expiry, ok := activeRunFrom(o)
		if !ok || !s.now().Before(expiry) || textNested(o.Object, "spec", "ownerHash") != owner || textNested(o.Object, "spec", "clientHash") != hashText(client) || cur.Environment != ref || cur.BindingID != req.BindingID || cur.RunID != req.RunID || cur.ClaimID != req.ClaimID || cur.Epoch != req.Epoch || cur.GrantGeneration != req.GrantGeneration || !generationMatches(o, req.GrantGeneration) {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "run claim mismatch"}
		}
		expires := s.now().Add(ttl)
		m, _, _ := unstructured.NestedMap(o.Object, "status", "activeRun")
		m["expiresAt"] = expires.Format(time.RFC3339Nano)
		m["lastRenewOperationID"] = req.OperationID
		if setErr := unstructured.SetNestedMap(o.Object, m, "status", "activeRun"); setErr != nil {
			return setErr
		}
		cur.ExpiresAt = expires
		out = cur
		return nil
	})
	return out, err
}

func (s *Store) ReleaseRun(ctx context.Context, ref executionenv.EnvironmentRef, client, owner string, req executionenv.RunClaimRequest) error {
	return s.retryUpdateStatus(ctx, ref.ID, func(o *unstructured.Unstructured) error {
		cur, _, _, ok := activeRunFrom(o)
		if !ok {
			return nil
		}
		if textNested(o.Object, "status", "lifecycleOperation", "id") != "" {
			return &executionenv.Error{Code: executionenv.CodeNotReady, Message: transitioningMessage}
		}
		if textNested(o.Object, "spec", "ownerHash") != owner || textNested(o.Object, "spec", "clientHash") != hashText(client) || cur.Environment != ref || cur.BindingID != req.BindingID || cur.RunID != req.RunID || cur.ClaimID != req.ClaimID || cur.Epoch != req.Epoch || cur.GrantGeneration != req.GrantGeneration || !generationMatches(o, req.GrantGeneration) {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "run claim mismatch"}
		}
		if textNested(o.Object, "status", "activeOperation", "id") != "" {
			return &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "run has an active operation"}
		}
		unstructured.RemoveNestedField(o.Object, "status", "activeRun")
		return nil
	})
}
