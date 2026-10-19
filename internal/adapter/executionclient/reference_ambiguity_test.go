package executionclient

import (
	"context"
	"testing"
	"time"

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

type droppedReservationRPC struct {
	executionv1.ExecutionProviderServiceClient
	afterReserve func(*executionv1.ReserveSuccessorRequest)
	lastAbort    *executionv1.ReferenceMutationRequest
	abortErr     error
	abortBounded bool
	commits      int
}

func (r *droppedReservationRPC) ReserveSuccessor(ctx context.Context, req *executionv1.ReserveSuccessorRequest, opts ...grpc.CallOption) (*executionv1.ReferenceReservationResponse, error) {
	if _, err := r.ExecutionProviderServiceClient.ReserveSuccessor(ctx, req, opts...); err != nil {
		return nil, err
	}
	r.afterReserve(req)
	return nil, status.Error(codes.Unavailable, "reservation response lost")
}

func (r *droppedReservationRPC) AbortReference(ctx context.Context, req *executionv1.ReferenceMutationRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	r.lastAbort = req
	r.abortErr = ctx.Err()
	deadline, ok := ctx.Deadline()
	r.abortBounded = ok && time.Until(deadline) > 0 && time.Until(deadline) <= 10*time.Second
	return r.ExecutionProviderServiceClient.AbortReference(ctx, req, opts...)
}

func (r *droppedReservationRPC) CommitReference(ctx context.Context, req *executionv1.ReferenceMutationRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	r.commits++
	return r.ExecutionProviderServiceClient.CommitReference(ctx, req, opts...)
}

type attachFailedRPC struct{ droppedCommitRPC }

func (*attachFailedRPC) AttachEnvironment(context.Context, *executionv1.AttachEnvironmentRequest, ...grpc.CallOption) (*executionv1.AttachEnvironmentResponse, error) {
	return nil, status.Error(codes.Unavailable, "attach failed")
}

func TestExclusiveSuccessorAbortsWhenAttachFailsAfterReservation(t *testing.T) {
	rpc := &attachFailedRPC{}
	provider := &Provider{client: &Client{rpc: rpc}, profile: "coding"}
	_, err := provider.ReserveSuccessor(t.Context(), server.PlacementSuccessorRequest{
		Ref:             session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env", Revision: "rev"},
		Principal:       &session.Principal{Issuer: "issuer", Subject: "alice"},
		SourceBindingID: "origin", DestinationBindingID: "fire",
	})
	if err == nil || rpc.aborts != 1 || rpc.lastAbort.GetBindingId() != "fire" || rpc.lastAbort.GetOperationId() != rpc.lastSuccessor.GetOperationId() {
		t.Fatalf("attach error=%v, aborts=%d, aborted=%v", err, rpc.aborts, rpc.lastAbort)
	}
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

func TestAmbiguousSuccessorCommitIsNeverAbortedByCleanup(t *testing.T) {
	rpc := &droppedCommitRPC{}
	provider := &Provider{client: &Client{rpc: rpc}, profile: "coding"}
	binding, err := provider.ReserveSuccessor(t.Context(), server.PlacementSuccessorRequest{
		Ref:             session.EnvironmentRef{Kind: "kubernetes", ID: "env", Revision: "rev"},
		Principal:       &session.Principal{Issuer: "issuer", Subject: "alice"},
		SourceBindingID: "origin", DestinationBindingID: "fire",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Commit(t.Context()); err == nil {
		t.Fatal("commit transport ambiguity was hidden")
	}
	for range 2 {
		if err := binding.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if rpc.commits != 1 || rpc.aborts != 0 {
		t.Fatalf("commits=%d aborts=%d", rpc.commits, rpc.aborts)
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
