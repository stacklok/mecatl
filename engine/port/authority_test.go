package port_test

import (
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
)

func TestADR_0233_AuthorityEvaluator_Scenario3_RequestShapeIsNeutralAndCarriesTheSet(t *testing.T) {
	t.Parallel()

	request := port.AuthorityRequest{
		CapabilitySet: governance.CapabilitySet{
			Tools:                    []string{"Read"},
			RemainingDelegationDepth: 1,
			FileSystem:               true,
		},
		ToolName:        "Read",
		Action:          "Read",
		DelegationDepth: 1,
		Principal: port.AuthorityPrincipal{
			Definition:   "code-reviewer",
			Instance:     "subagent-1",
			OwnerIssuer:  "https://issuer.example",
			OwnerSubject: "owner-1",
		},
		Resource: &port.AuthorityResource{
			Kind:      port.AuthorityResourceWorkspaceFile,
			Path:      "/workspace/README.md",
			Workspace: "/workspace",
		},
	}

	if !request.CapabilitySet.AllowsTool(request.ToolName) {
		t.Fatalf("request set %+v does not carry its tool %q", request.CapabilitySet, request.ToolName)
	}
	if request.DelegationDepth != request.CapabilitySet.RemainingDelegationDepth {
		t.Fatalf("request delegation depth = %d, want carried set depth %d", request.DelegationDepth, request.CapabilitySet.RemainingDelegationDepth)
	}
	if request.Principal.Definition == "" || request.Principal.Instance == "" || request.Principal.OwnerIssuer == "" || request.Principal.OwnerSubject == "" {
		t.Fatalf("principal %+v must carry definition, instance, owner issuer, and owner subject", request.Principal)
	}
	if request.Resource == nil || request.Resource.Kind != port.AuthorityResourceWorkspaceFile || request.Resource.Path != "/workspace/README.md" || request.Resource.Workspace != "/workspace" {
		t.Fatalf("resource = %+v, want normalized workspace file", request.Resource)
	}

	requestType := reflect.TypeOf(request)
	for i := range requestType.NumField() {
		field := requestType.Field(i)
		switch field.Name {
		case "Args", "Arguments", "Credentials", "Credential", "Cedar", "CedarRequest":
			t.Fatalf("AuthorityRequest must not carry %s", field.Name)
		}
		if field.Type.PkgPath() == "github.com/cedar-policy/cedar-go" {
			t.Fatalf("AuthorityRequest must not expose a Cedar type: %s", field.Type)
		}
	}
	resourceType := reflect.TypeOf(port.AuthorityResource{})
	for i := range resourceType.NumField() {
		field := resourceType.Field(i)
		switch field.Name {
		case "Args", "Arguments", "Credentials", "Credential", "Cedar", "CedarResource":
			t.Fatalf("AuthorityResource must not carry %s", field.Name)
		}
		if field.Type.PkgPath() == "github.com/cedar-policy/cedar-go" {
			t.Fatalf("AuthorityResource must not expose a Cedar type: %s", field.Type)
		}
	}
}
