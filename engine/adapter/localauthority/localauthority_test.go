package localauthority

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/authorityconformance"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
)

func TestEvaluatorConformance(t *testing.T) {
	authorityconformance.Run(t, func(*testing.T) port.AuthorityEvaluator { return New() })
}

func TestEvaluatorDeniesToolOutsideCapabilitySet(t *testing.T) {
	t.Parallel()

	decision, err := New().AuthorizeTool(context.Background(), port.AuthorityRequest{
		CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}},
		ToolName:      "Write",
		Action:        "Write",
		Principal:     port.AuthorityPrincipal{Definition: "definition", Instance: "instance", OwnerIssuer: "issuer", OwnerSubject: "subject"},
	})
	if err != nil {
		t.Fatalf("AuthorizeTool: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("AuthorizeTool allowed a tool absent from the set: %+v", decision)
	}
}
