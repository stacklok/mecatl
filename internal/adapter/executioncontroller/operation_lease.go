package executioncontroller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type operationLeaseAuthority struct {
	environment, revision, operationID, bindingID, runID, claimID string
	client, owner                                                 string
	epoch, grantGeneration                                        uint64
}

func operationMatches(o *unstructured.Unstructured, authority operationLeaseAuthority, holderID string) bool {
	m, found, err := unstructured.NestedMap(o.Object, "status", "activeOperation")
	return err == nil && found && text(m, "id") == authority.operationID && text(m, "holderID") == holderID &&
		text(m, "claimID") == authority.claimID && text(m, "runID") == authority.runID &&
		intNested(m, "epoch") == int64(authority.epoch) //nolint:gosec // epochs are bounded before persistence.
}

func operationLeaseCurrent(o *unstructured.Unstructured, authority operationLeaseAuthority, holderID string, now time.Time) bool {
	if !operationMatches(o, authority, holderID) || textNested(o.Object, "status", "fenceState") != fenceHealthy {
		return false
	}
	expires, err := time.Parse(time.RFC3339Nano, textNested(o.Object, "status", "activeOperation", "expiresAt"))
	return err == nil && now.Before(expires)
}

func (s *Store) operationAuthorityCurrent(o *unstructured.Unstructured, authority operationLeaseAuthority) bool {
	claim, _, expiry, ok := activeRunFrom(o)
	return ok && s.now().Before(expiry) && intNested(o.Object, "status", "activeRun", "epoch") == int64(authority.epoch) && //nolint:gosec // epochs are bounded before persistence.
		claim.Environment.ID == authority.environment && claim.Environment.Revision == authority.revision &&
		claim.BindingID == authority.bindingID && claim.RunID == authority.runID && claim.ClaimID == authority.claimID &&
		claim.Epoch == authority.epoch && claim.GrantGeneration == authority.grantGeneration &&
		epochMatches(o, authority.epoch) && generationMatches(o, authority.grantGeneration) &&
		textNested(o.Object, "spec", "clientHash") == hashText(authority.client) && textNested(o.Object, "spec", "ownerHash") == authority.owner
}

func (s *Store) renewOperationLease(ctx context.Context, authority operationLeaseAuthority) error {
	interval := s.opTTL / 3
	if interval <= 0 {
		interval = operationLeaseTTL / 3
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			err := s.retryUpdateStatus(ctx, authority.environment, func(o *unstructured.Unstructured) error {
				now := s.now()
				if !operationLeaseCurrent(o, authority, s.holderID, now) || !s.operationAuthorityCurrent(o, authority) {
					return incompatibleOperationError()
				}
				m, _, _ := unstructured.NestedMap(o.Object, "status", "activeOperation")
				m["renewedAt"] = now.Format(time.RFC3339Nano)
				m["expiresAt"] = now.Add(s.opTTL).Format(time.RFC3339Nano)
				return unstructured.SetNestedMap(o.Object, m, "status", "activeOperation")
			})
			if err != nil {
				return err
			}
		}
	}
}

func incompatibleOperationError() error {
	return &operationLeaseError{}
}

type operationLeaseError struct{}

func (*operationLeaseError) Error() string { return "operation lease ownership changed" }
