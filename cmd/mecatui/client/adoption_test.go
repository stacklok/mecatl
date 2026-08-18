package client

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type adoptionClientFake struct {
	mecatlv1.HarnessServiceClient
	preflight *mecatlv1.PreflightSessionAdoptionRequest
	adopt     *mecatlv1.AdoptSessionRequest
}

func (f *adoptionClientFake) PreflightSessionAdoption(_ context.Context, req *mecatlv1.PreflightSessionAdoptionRequest, _ ...grpc.CallOption) (*mecatlv1.PreflightSessionAdoptionResponse, error) {
	f.preflight = req
	return &mecatlv1.PreflightSessionAdoptionResponse{Eligible: true, Bindings: req.GetBindings()}, nil
}

func (f *adoptionClientFake) AdoptSession(_ context.Context, req *mecatlv1.AdoptSessionRequest, _ ...grpc.CallOption) (*mecatlv1.AdoptSessionResponse, error) {
	f.adopt = req
	return &mecatlv1.AdoptSessionResponse{SessionId: "target", SourceSessionId: req.GetSourceSessionId(), Capabilities: &mecatlv1.ServerCapabilities{LegacyAdoption: true}, ResolvedModel: &mecatlv1.ResolvedModel{ProviderId: "p", ModelId: "m"}}, nil
}

func TestAdoptionClientCarriesExplicitBindings(t *testing.T) {
	fake := &adoptionClientFake{}
	client := &Client{svc: fake}
	bindings := AdoptionBindings{Workspace: "/ws", EnvironmentKind: "local", EnvironmentID: "/ws", ProviderID: "p", ModelID: "m"}
	preflight, err := client.PreflightSessionAdoption(context.Background(), "source", bindings)
	if err != nil || !preflight.Eligible || fake.preflight.GetBindings().GetEnvironmentId() != "/ws" {
		t.Fatalf("preflight = %+v, %v; request=%+v", preflight, err, fake.preflight)
	}
	result, err := client.AdoptSession(context.Background(), "source", "key", bindings)
	if err != nil || result.SessionID != "target" || !result.Capabilities.LegacyAdoption || result.ResolvedModel.ProviderID != "p" {
		t.Fatalf("adoption = %+v, %v", result, err)
	}
	if fake.adopt.GetIdempotencyKey() != "key" || fake.adopt.GetBindings().GetModelId() != "m" {
		t.Fatalf("adoption request = %+v", fake.adopt)
	}
}
