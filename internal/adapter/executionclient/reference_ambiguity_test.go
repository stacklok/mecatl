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
	commits int
	aborts  int
}

func (r *droppedCommitRPC) CommitReference(context.Context, *executionv1.ReferenceMutationRequest, ...grpc.CallOption) (*emptypb.Empty, error) {
	r.commits++
	return nil, status.Error(codes.Unavailable, "response lost")
}
func (r *droppedCommitRPC) AbortReference(context.Context, *executionv1.ReferenceMutationRequest, ...grpc.CallOption) (*emptypb.Empty, error) {
	r.aborts++
	return &emptypb.Empty{}, nil
}

func TestDefinitePreCommitFailureAbortsReservation(t *testing.T) {
	rpc := &droppedCommitRPC{}
	provider := &Provider{client: &Client{rpc: rpc}, profile: "coding"}
	ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env", Revision: "rev"}
	binding := provider.withReferenceTransaction(server.PlacementBinding{Ref: ref}, executionenv.Owner{Issuer: "issuer", Subject: "alice"}, "session", "operation")
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	if rpc.commits != 0 || rpc.aborts != 1 {
		t.Fatalf("commits=%d aborts=%d", rpc.commits, rpc.aborts)
	}
}

func TestAmbiguousCommitIsRetainedAndNeverAbortedByCleanup(t *testing.T) {
	rpc := &droppedCommitRPC{}
	provider := &Provider{client: &Client{rpc: rpc}, profile: "coding"}
	ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env", Revision: "rev"}
	binding := provider.withReferenceTransaction(server.PlacementBinding{Ref: ref}, executionenv.Owner{Issuer: "issuer", Subject: "alice"}, "session", "operation")
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
