package executionclient

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/executionenv"
)

type droppedCommitRPC struct {
	executionv1.ExecutionProviderServiceClient
	commits       int
	aborts        int
	lastAbort     *executionv1.ReferenceMutationRequest
	lastSuccessor *executionv1.ReserveSuccessorRequest
}

func (r *droppedCommitRPC) CommitReference(context.Context, *executionv1.ReferenceMutationRequest, ...grpc.CallOption) (*emptypb.Empty, error) {
	r.commits++
	return nil, status.Error(codes.Unavailable, "response lost")
}
func (r *droppedCommitRPC) AbortReference(_ context.Context, req *executionv1.ReferenceMutationRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	r.aborts++
	r.lastAbort = req
	return &emptypb.Empty{}, nil
}
func (r *droppedCommitRPC) ReserveSuccessor(_ context.Context, req *executionv1.ReserveSuccessorRequest, _ ...grpc.CallOption) (*executionv1.ReferenceReservationResponse, error) {
	r.lastSuccessor = req
	return &executionv1.ReferenceReservationResponse{}, nil
}
func (*droppedCommitRPC) AttachEnvironment(_ context.Context, req *executionv1.AttachEnvironmentRequest, _ ...grpc.CallOption) (*executionv1.AttachEnvironmentResponse, error) {
	return &executionv1.AttachEnvironmentResponse{Environment: req.GetContext().GetEnvironment(), Epoch: 1, Ready: true, GrantGeneration: 1}, nil
}

func TestExclusiveSuccessorReservationAbortsOnPrepublicationFailure(t *testing.T) {
	rpc := &droppedCommitRPC{}
	provider := &Provider{client: &Client{rpc: rpc}, profile: "coding"}
	ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env", Revision: "rev"}
	binding, err := provider.ReserveSuccessor(t.Context(), server.PlacementSuccessorRequest{
		Ref: ref, Principal: &session.Principal{Issuer: "issuer", Subject: "alice"},
		SourceBindingID: "source", DestinationBindingID: "destination",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	if rpc.commits != 0 || rpc.aborts != 1 {
		t.Fatalf("commits=%d aborts=%d, want one conclusive successor abort", rpc.commits, rpc.aborts)
	}
	if rpc.lastSuccessor == nil || rpc.lastAbort.GetBindingId() != "destination" || rpc.lastAbort.GetOperationId() != rpc.lastSuccessor.GetOperationId() || rpc.lastAbort.GetOperationId() == "" {
		t.Fatalf("reserve=%+v abort binding=%q operation=%q, want exact successor destination and operation", rpc.lastSuccessor, rpc.lastAbort.GetBindingId(), rpc.lastAbort.GetOperationId())
	}
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	if rpc.aborts != 1 {
		t.Fatalf("second close issued another abort: %d", rpc.aborts)
	}
}

func TestSharedInitialReservationIsRetainedOnPrepublicationFailure(t *testing.T) {
	rpc := &droppedCommitRPC{}
	provider := &Provider{client: &Client{rpc: rpc}, profile: "coding"}
	ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env", Revision: "rev"}
	binding := provider.withSharedInitialReferenceTransaction(server.PlacementBinding{Ref: ref}, executionenv.Owner{Issuer: "issuer", Subject: "alice"}, "session", "operation")
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	if rpc.commits != 0 || rpc.aborts != 0 {
		t.Fatalf("commits=%d aborts=%d, shared initial reservation must remain pending", rpc.commits, rpc.aborts)
	}
}

func TestAmbiguousCommitIsRetainedAndNeverAbortedByCleanup(t *testing.T) {
	rpc := &droppedCommitRPC{}
	provider := &Provider{client: &Client{rpc: rpc}, profile: "coding"}
	ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env", Revision: "rev"}
	binding := provider.withSharedInitialReferenceTransaction(server.PlacementBinding{Ref: ref}, executionenv.Owner{Issuer: "issuer", Subject: "alice"}, "session", "operation")
	if err := binding.Commit(t.Context()); err == nil {
		t.Fatal("commit transport ambiguity was hidden")
	}
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	if rpc.commits != 1 || rpc.aborts != 0 {
		t.Fatalf("commits=%d aborts=%d", rpc.commits, rpc.aborts)
	}
}
