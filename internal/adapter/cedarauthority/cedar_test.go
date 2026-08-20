package cedarauthority

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/authorityconformance"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
)

func TestADR_0233_AuthorityEvaluator_Scenario7_PolicyIsStaticAndDataIsPerRequest(t *testing.T) {
	t.Parallel()

	evaluator, err := New(DefaultPolicy)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	request := validRequest("Read")
	first, err := evaluator.AuthorizeTool(context.Background(), request)
	if err != nil || !first.Allowed {
		t.Fatalf("first authorization = (%+v, %v), want allowed", first, err)
	}
	request.Principal.Instance = "child-instance"
	second, err := evaluator.AuthorizeTool(context.Background(), request)
	if err != nil || !second.Allowed {
		t.Fatalf("second authorization = (%+v, %v), want allowed", second, err)
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario7_OperatorRuleTightensButCannotGrant(t *testing.T) {
	t.Parallel()

	evaluator, err := New([]byte(`
permit(principal, action, resource);
forbid(principal, action, resource) when { resource.path like "/workspace/vendor/*" };
`))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	request := validRequest("Read")
	request.Resource = &port.AuthorityResource{Kind: port.AuthorityResourceWorkspaceFile, Workspace: "/workspace", Path: "/workspace/vendor/module.go"}
	decision, err := evaluator.AuthorizeTool(context.Background(), request)
	if err != nil {
		t.Fatalf("AuthorizeTool: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("vendor request allowed: %+v", decision)
	}

	decision, err = evaluator.AuthorizeTool(context.Background(), validRequest("Write"))
	if err != nil {
		t.Fatalf("AuthorizeTool absent tool: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("policy granted a tool omitted from the carried set: %+v", decision)
	}
}

func TestADR_0233_AuthorityEvaluator_PrincipalIdentityIsInjective(t *testing.T) {
	t.Parallel()

	evaluator, err := New([]byte(`
permit(principal, action, resource);
forbid(principal, action, resource) when {
    principal.owner_issuer == OwnerIssuer::"issuer:subject"
    && principal.owner_subject == OwnerSubject::"x"
};
`))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	denied := validRequest("Read")
	denied.Principal.OwnerIssuer = "issuer:subject"
	denied.Principal.OwnerSubject = "x"
	decision, err := evaluator.AuthorizeTool(context.Background(), denied)
	if err != nil {
		t.Fatalf("AuthorizeTool denied identity: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("Cedar allowed explicitly forbidden issuer/subject pair: %+v", decision)
	}

	allowed := denied
	allowed.Principal.OwnerIssuer = "issuer"
	allowed.Principal.OwnerSubject = "subject:x"
	decision, err = evaluator.AuthorizeTool(context.Background(), allowed)
	if err != nil {
		t.Fatalf("AuthorizeTool colliding identity: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("Cedar conflated distinct issuer/subject pair: %+v", decision)
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario7_CedarReceivesResourceOperationAction(t *testing.T) {
	t.Parallel()

	evaluator, err := New([]byte(`
permit(principal, action, resource);
forbid(principal, action == Tool::"ReadMcpResource", resource);
`))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	request := validRequest(governance.MCPResourceCapability("resource-only"))
	request.Action = "ReadMcpResource"
	decision, err := evaluator.AuthorizeTool(context.Background(), request)
	if err != nil {
		t.Fatalf("AuthorizeTool: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("Cedar allowed resource read despite action-specific forbid: %+v", decision)
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario7_DefinitionGroupGrantIsRejected(t *testing.T) {
	t.Parallel()

	_, err := New([]byte(`permit(principal in Definition::"reviewer", action, resource);`))
	if err == nil {
		t.Fatal("New accepted a definition-group grant")
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario7_CedarStaysOutOfTheEngineModule(t *testing.T) {
	t.Parallel()

	module, err := os.ReadFile("../../../engine/go.mod")
	if err != nil {
		t.Fatalf("read engine/go.mod: %v", err)
	}
	if strings.Contains(string(module), "cedar-policy") {
		t.Fatal("engine module depends on Cedar")
	}
}

func TestEvaluatorConformance(t *testing.T) {
	authorityconformance.Run(t, func(t *testing.T) port.AuthorityEvaluator {
		t.Helper()
		evaluator, err := New(DefaultPolicy)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return evaluator
	})
}

func validRequest(tool string) port.AuthorityRequest {
	return port.AuthorityRequest{
		CapabilitySet:   governance.CapabilitySet{Tools: []string{"Read"}, RemainingDelegationDepth: 1},
		ToolName:        tool,
		Action:          tool,
		DelegationDepth: 1,
		Principal:       port.AuthorityPrincipal{Definition: "reviewer", Instance: "instance", OwnerIssuer: "issuer", OwnerSubject: "subject"},
	}
}
