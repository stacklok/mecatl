//nolint:revive // Private store methods implement adapter-only lifecycle interfaces.
package executioncontroller

import (
	"context"
	"fmt"
	"math"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/stacklok/mecatl/internal/executionenv"
)

// RevokeEnvironment atomically invalidates every issued grant without claiming
// that an already-dispatched executor operation has stopped.
func (s *Store) RevokeEnvironment(ctx context.Context, ref executionenv.EnvironmentRef, client, owner string, expected uint64, operationID string) (uint64, error) {
	if expected == 0 || expected >= math.MaxInt64 || operationID == "" || len(operationID) > executionenv.MaxIdentityBytes {
		return 0, &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "grant generation or operation identity is invalid"}
	}
	requestFingerprint := fingerprint(ref.ID, ref.Revision, client, owner, fmt.Sprint(expected))
	var next uint64
	err := s.retryUpdateStatus(ctx, ref.ID, func(o *unstructured.Unstructured) error {
		if textNested(o.Object, "spec", "ownerHash") != owner || textNested(o.Object, "spec", "clientHash") != hashText(client) || textNested(o.Object, "spec", "revision") != ref.Revision {
			return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
		}
		receipts, found, err := unstructured.NestedSlice(o.Object, "status", "revocationReceipts")
		if err != nil {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "revocation receipt ledger is invalid"}
		}
		if found {
			for _, raw := range receipts {
				receipt, ok := raw.(map[string]any)
				if !ok {
					return &executionenv.Error{Code: executionenv.CodeConflict, Message: "revocation receipt ledger is invalid"}
				}
				if text(receipt, operationIDField) != operationID {
					continue
				}
				if text(receipt, "fingerprint") != requestFingerprint {
					return &executionenv.Error{Code: executionenv.CodeConflict, Message: "operation identity was reused for a different revocation"}
				}
				generation, ok := receipt["grantGeneration"].(int64)
				if !ok || generation <= 0 {
					return &executionenv.Error{Code: executionenv.CodeConflict, Message: "revocation receipt ledger is invalid"}
				}
				next = uint64(generation)
				return nil
			}
		}
		current := intNested(o.Object, "status", "grantGeneration")
		if current <= 0 || uint64(current) != expected { //nolint:gosec // positivity is checked before conversion.
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "grant generation changed concurrently"}
		}
		next = expected + 1
		receipts = append(receipts, map[string]any{operationIDField: operationID, "fingerprint": requestFingerprint, "expectedGrantGeneration": int64(expected), "grantGeneration": int64(next)}) //nolint:gosec // values are bounded above.
		if len(receipts) > 32 {
			receipts = receipts[len(receipts)-32:]
		}
		if err := unstructured.SetNestedSlice(o.Object, receipts, "status", "revocationReceipts"); err != nil {
			return err
		}
		return unstructured.SetNestedField(o.Object, int64(next), "status", "grantGeneration") //nolint:gosec // expected is bounded below MaxInt64.
	})
	return next, err
}
