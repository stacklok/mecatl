package grpcdriver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/proposalconformance"
	"github.com/stacklok/mecatl/engine/learning"
)

func TestCloudNativeLearning_Scenario4_DistributedRepositoriesConform(t *testing.T) {
	proposalconformance.Run(t, func(t *testing.T) learning.ProposalRepository {
		backend := memproposal.New()
		conn := dialBufconn(t, func(server *grpc.Server) {
			driverv1.RegisterProposalRepositoryServiceServer(server, NewProposalRepositoryServer(backend))
		})
		return NewProposalRepository(conn)
	})
}

type proposalRepositoryStub struct {
	learning.ProposalRepository
	stage func(context.Context, learning.ProposalPartition, string, []learning.Candidate, []learning.Signal) ([]learning.ProposalRecord, error)
}

func (s proposalRepositoryStub) StageBatch(ctx context.Context, partition learning.ProposalPartition, digest string, candidates []learning.Candidate, signals []learning.Signal) ([]learning.ProposalRecord, error) {
	return s.stage(ctx, partition, digest, candidates, signals)
}

func TestProposalRepositoryDriverMapsOnlySafeTypedErrors(t *testing.T) {
	partition := learning.ProposalPartition{Principal: "caller", Project: "project"}
	candidate := learning.Candidate{Kind: learning.CandidateProjectFact, Key: "project/test", Value: "Use task test.", Evidence: []learning.EvidenceRef{{SessionID: "session-1", Locator: learning.EvidenceMessage, Digest: strings.Repeat("d", 64)}}}
	digest := strings.Repeat("a", 64)

	for _, backendErr := range []error{learning.ErrInvalidProposal, learning.ErrProposalNotFound, learning.ErrProposalVersionConflict, learning.ErrProposalTransition, learning.ErrProposalLimit} {
		backendErr := backendErr
		t.Run(backendErr.Error(), func(t *testing.T) {
			client := proposalDriverWithStageError(t, backendErr)
			_, err := client.StageBatch(context.Background(), partition, digest, []learning.Candidate{candidate}, nil)
			if !errors.Is(err, backendErr) || err.Error() != backendErr.Error() {
				t.Fatalf("StageBatch error = %v, want closed sentinel %v", err, backendErr)
			}
		})
	}

	client := proposalDriverWithStageError(t, errors.New("database password hunter2"))
	_, err := client.StageBatch(context.Background(), partition, digest, []learning.Candidate{candidate}, nil)
	if status.Code(err) != codes.Internal || status.Convert(err).Message() != "proposal repository driver request failed" {
		t.Fatalf("untyped StageBatch error = %v, want bounded Internal failure", err)
	}
}

func proposalDriverWithStageError(t *testing.T, backendErr error) *ProposalRepository {
	t.Helper()
	backend := proposalRepositoryStub{stage: func(context.Context, learning.ProposalPartition, string, []learning.Candidate, []learning.Signal) ([]learning.ProposalRecord, error) {
		return nil, backendErr
	}}
	conn := dialBufconn(t, func(server *grpc.Server) {
		driverv1.RegisterProposalRepositoryServiceServer(server, NewProposalRepositoryServer(backend))
	})
	return NewProposalRepository(conn)
}
