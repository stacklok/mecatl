package client

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// TestCreateSessionWithAgentCarriesAgentDefinitionName asserts the proto-build
// point sets agent_definition_name from the given name (ADR 0353), leaving the
// ordinary ModelSelection fields intact alongside it.
func TestCreateSessionWithAgentCarriesAgentDefinitionName(t *testing.T) {
	fake := &fakeModelsClient{}
	cl := newFakeClient(fake)

	_, _, _, err := cl.CreateSessionWithAgent(context.Background(),
		mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT,
		ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}, "release-reviewer")
	if err != nil {
		t.Fatalf("CreateSessionWithAgent: %v", err)
	}
	if fake.lastCreate.GetAgentDefinitionName() != "release-reviewer" {
		t.Fatalf("request agent_definition_name = %q, want release-reviewer", fake.lastCreate.GetAgentDefinitionName())
	}
	if fake.lastCreate.GetProviderId() != "openai" || fake.lastCreate.GetModelId() != "gpt-5" {
		t.Fatalf("request provider:%q model:%q, want openai/gpt-5 to survive alongside the binding",
			fake.lastCreate.GetProviderId(), fake.lastCreate.GetModelId())
	}
}

// TestCreateSessionWithAgentPropagatesUnknownDefinitionError asserts an
// unknown agent_definition_name's server-side InvalidArgument rejection
// propagates and is classifiable via the existing IsInvalidArgument, exactly
// like any other malformed CreateSession request.
func TestCreateSessionWithAgentPropagatesUnknownDefinitionError(t *testing.T) {
	fake := &fakeModelsClient{createErr: status.Error(codes.InvalidArgument, `unknown agent definition "nope"`)}
	cl := newFakeClient(fake)

	_, _, _, err := cl.CreateSessionWithAgent(context.Background(),
		mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, ModelSelection{}, "nope")
	if err == nil {
		t.Fatal("CreateSessionWithAgent: want an error for an unknown definition, got nil")
	}
	if !IsInvalidArgument(err) {
		t.Fatalf("IsInvalidArgument(%v) = false, want true", err)
	}
}
