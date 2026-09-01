package grpcdriver

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
)

// LearningRepositoryCapabilities is the closed set required to select one
// remote distributed-learning backend.
type LearningRepositoryCapabilities struct {
	AttemptRepository        bool
	ProposalRepository       bool
	SkillRepository          bool
	ValidatedSkillActivation bool
}

const learningRepositoryCapabilityTimeout = 5 * time.Second

// ProbeLearningRepositoryCapabilities negotiates the repository set before any
// remote learning repository is composed.
func ProbeLearningRepositoryCapabilities(ctx context.Context, conn grpc.ClientConnInterface) (LearningRepositoryCapabilities, error) {
	probeCtx, cancel := context.WithTimeout(ctx, learningRepositoryCapabilityTimeout)
	defer cancel()
	resp, err := driverv1.NewLearningRepositoryCapabilitiesServiceClient(conn).Capabilities(probeCtx, &driverv1.LearningRepositoryCapabilitiesRequest{})
	if err != nil {
		return LearningRepositoryCapabilities{}, err
	}
	if resp == nil {
		return LearningRepositoryCapabilities{}, errors.New("grpcdriver: learning repository capabilities returned an empty response")
	}
	return LearningRepositoryCapabilities{
		AttemptRepository:        resp.GetAttemptRepository(),
		ProposalRepository:       resp.GetProposalRepository(),
		SkillRepository:          resp.GetSkillRepository(),
		ValidatedSkillActivation: resp.GetValidatedSkillActivation(),
	}, nil
}

// NewLearningRepositoryCapabilitiesServer advertises one driver's complete
// repository set. It does not expose connection, identity, or storage details.
func NewLearningRepositoryCapabilitiesServer(capabilities LearningRepositoryCapabilities) driverv1.LearningRepositoryCapabilitiesServiceServer {
	return &learningRepositoryCapabilitiesServer{capabilities: capabilities}
}

type learningRepositoryCapabilitiesServer struct {
	driverv1.UnimplementedLearningRepositoryCapabilitiesServiceServer
	capabilities LearningRepositoryCapabilities
}

func (s *learningRepositoryCapabilitiesServer) Capabilities(context.Context, *driverv1.LearningRepositoryCapabilitiesRequest) (*driverv1.LearningRepositoryCapabilitiesResponse, error) {
	return &driverv1.LearningRepositoryCapabilitiesResponse{
		AttemptRepository:        s.capabilities.AttemptRepository,
		ProposalRepository:       s.capabilities.ProposalRepository,
		SkillRepository:          s.capabilities.SkillRepository,
		ValidatedSkillActivation: s.capabilities.ValidatedSkillActivation,
	}, nil
}
