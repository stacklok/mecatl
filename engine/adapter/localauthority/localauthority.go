// Package localauthority provides the in-process set-check implementation of
// port.AuthorityEvaluator.
package localauthority

import (
	"context"

	"github.com/stacklok/mecatl/engine/port"
)

// Evaluator authorizes exact tool names from the capability set carried by each
// request. It performs no lookup and therefore cannot widen a run's authority.
type Evaluator struct{}

var _ port.AuthorityEvaluator = (*Evaluator)(nil)

// New constructs a local capability-set evaluator.
func New() *Evaluator { return &Evaluator{} }

// AuthorizeTool permits only a well-formed request whose tool is in its carried
// capability set.
func (*Evaluator) AuthorizeTool(_ context.Context, request port.AuthorityRequest) (port.AuthorityDecision, error) {
	if malformed(request) {
		return port.AuthorityDecision{Reason: "authority request is malformed"}, nil
	}
	if !request.CapabilitySet.AllowsTool(request.ToolName) {
		return port.AuthorityDecision{Reason: "tool is absent from the capability set"}, nil
	}
	return port.AuthorityDecision{Allowed: true}, nil
}

func malformed(request port.AuthorityRequest) bool {
	return request.ToolName == "" || request.Action == "" || request.Principal.Definition == "" || request.Principal.Instance == ""
}
