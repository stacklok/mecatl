// Package noopauthority provides the explicit no-enforcement implementation of
// port.AuthorityEvaluator for local and demo deployments.
package noopauthority

import (
	"context"

	"github.com/stacklok/mecatl/engine/port"
)

// Evaluator permits every well-formed request without consulting its capability
// set. Its use is an explicit composition decision, not a fallback for a missing
// evaluator.
type Evaluator struct{}

var _ port.AuthorityEvaluator = (*Evaluator)(nil)

// New constructs a no-op authority evaluator.
func New() *Evaluator { return &Evaluator{} }

// AuthorizeTool permits a well-formed request. A malformed request is denied so
// an incomplete request cannot become an authorization bypass in this mode.
func (*Evaluator) AuthorizeTool(_ context.Context, request port.AuthorityRequest) (port.AuthorityDecision, error) {
	if malformed(request) {
		return port.AuthorityDecision{Reason: "authority request is malformed"}, nil
	}
	return port.AuthorityDecision{Allowed: true}, nil
}

func malformed(request port.AuthorityRequest) bool {
	return request.ToolName == "" || request.Action == "" || request.Principal.Definition == "" || request.Principal.Instance == ""
}
