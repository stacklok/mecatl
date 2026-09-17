package executioncontroller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func operationMatches(o *unstructured.Unstructured, operationID, holderID, claimID string, epoch uint64) bool {
	m, found, err := unstructured.NestedMap(o.Object, "status", "activeOperation")
	return err == nil && found && text(m, "id") == operationID && text(m, "holderID") == holderID && text(m, "claimID") == claimID && intNested(m, "epoch") == int64(epoch) //nolint:gosec // epochs are bounded before persistence.
}

func (s *Store) renewOperationLease(ctx context.Context, environment, operationID, claimID string, epoch uint64) error {
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
			err := s.retryUpdateStatus(ctx, environment, func(o *unstructured.Unstructured) error {
				if !operationMatches(o, operationID, s.holderID, claimID, epoch) {
					return incompatibleOperationError()
				}
				m, _, _ := unstructured.NestedMap(o.Object, "status", "activeOperation")
				now := s.now()
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
