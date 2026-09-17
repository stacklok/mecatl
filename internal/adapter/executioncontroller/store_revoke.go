//nolint:revive // Private store methods implement adapter-only lifecycle interfaces.
package executioncontroller

import (
	"context"
	"math"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/stacklok/mecatl/internal/executionenv"
)

// RevokeEnvironment atomically invalidates every issued grant without claiming
// that an already-dispatched executor operation has stopped.
func (s *Store) RevokeEnvironment(ctx context.Context, ref executionenv.EnvironmentRef, client, owner string, expected uint64) (uint64, error) {
	if expected == 0 || expected >= math.MaxInt64 {
		return 0, &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "grant generation is invalid"}
	}
	var next uint64
	err := s.retryUpdateStatus(ctx, ref.ID, func(o *unstructured.Unstructured) error {
		if textNested(o.Object, "spec", "ownerHash") != owner || textNested(o.Object, "spec", "clientHash") != hashText(client) || textNested(o.Object, "spec", "revision") != ref.Revision {
			return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
		}
		current := intNested(o.Object, "status", "grantGeneration")
		if current <= 0 || uint64(current) != expected { //nolint:gosec // positivity is checked before conversion.
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "grant generation changed concurrently"}
		}
		next = expected + 1
		return unstructured.SetNestedField(o.Object, int64(next), "status", "grantGeneration") //nolint:gosec // expected is bounded below MaxInt64.
	})
	return next, err
}
