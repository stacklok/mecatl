package app

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/memattempt"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/internal/adapter/grpcdriver"
)

func TestLearningDriverCompositionRequiresExplicitCapabilities(t *testing.T) {
	addr := startSourceDriver(t, func(server *grpc.Server) {
		driverv1.RegisterAttemptRepositoryServiceServer(server, grpcdriver.NewAttemptRepositoryServer(memattempt.New(wallclock.Clock{})))
		driverv1.RegisterProposalRepositoryServiceServer(server, grpcdriver.NewProposalRepositoryServer(memproposal.New()))
		driverv1.RegisterSkillRepositoryServiceServer(server, grpcdriver.NewSkillRepositoryServer(memskill.New()))
	})

	_, _, _, closeDriver, err := resolveLearningRepositories(context.Background(), Config{
		LearningStoreURL: addr,
		driverConns:      driverConnsForTest(t),
	})
	if closeDriver != nil {
		closeDriver()
	}
	if err == nil || !strings.Contains(err.Error(), "capabilit") {
		t.Fatalf("resolveLearningRepositories() error = %v, want explicit capability-negotiation failure", err)
	}
}

type captureProposalPartitions struct {
	learning.ProposalRepository
	seen learning.ProposalPartition
}

func (r *captureProposalPartitions) List(ctx context.Context, partition learning.ProposalPartition, query learning.ProposalList) (learning.ProposalPage, error) {
	r.seen = partition
	return r.ProposalRepository.List(ctx, partition, query)
}

type captureSkillPartitions struct {
	learning.SkillRepository
	seen learning.SkillPartition
}

func (r *captureSkillPartitions) List(ctx context.Context, partition learning.SkillPartition, query learning.SkillList) (learning.SkillPage, error) {
	r.seen = partition
	return r.SkillRepository.List(ctx, partition, query)
}

func TestLearningDriverCompositionSmoke(t *testing.T) {
	attempts := memattempt.New(wallclock.Clock{})
	proposals := &captureProposalPartitions{ProposalRepository: memproposal.New()}
	skills := &captureSkillPartitions{SkillRepository: memskill.New()}
	addr := startSourceDriver(t, func(server *grpc.Server) {
		driverv1.RegisterLearningRepositoryCapabilitiesServiceServer(server, grpcdriver.NewLearningRepositoryCapabilitiesServer(grpcdriver.LearningRepositoryCapabilities{
			AttemptRepository: true, ProposalRepository: true, SkillRepository: true, ValidatedSkillActivation: true,
		}))
		driverv1.RegisterAttemptRepositoryServiceServer(server, grpcdriver.NewAttemptRepositoryServer(attempts))
		driverv1.RegisterProposalRepositoryServiceServer(server, grpcdriver.NewProposalRepositoryServer(proposals))
		driverv1.RegisterSkillRepositoryServiceServer(server, grpcdriver.NewSkillRepositoryServer(skills))
	})
	connections := driverConnsForTest(t)

	attemptRepo, proposalRepo, skillRepo, closeDriver, err := resolveLearningRepositories(context.Background(), Config{
		LearningStoreURL: addr,
		driverConns:      connections,
	})
	if err != nil {
		t.Fatalf("resolveLearningRepositories() error = %v", err)
	}
	if attemptRepo == nil || proposalRepo == nil || skillRepo == nil {
		t.Fatal("complete learning driver did not compose all repositories")
	}
	if _, ok := skillRepo.(learning.ValidatedSkillActivator); !ok {
		t.Fatal("advertised validated activation capability was not composed")
	}
	if got := len(connections.conns); got != 1 {
		t.Fatalf("driver connections = %d, want one Build-scoped connection", got)
	}

	proposalPartition := learning.ProposalPartition{Principal: "alice@example.invalid", Project: "/srv/workspaces/secret-project"}
	if _, err := proposalRepo.List(context.Background(), proposalPartition, learning.ProposalList{}); err != nil {
		t.Fatalf("list proposals: %v", err)
	}
	skillPartition := learning.SkillPartition(proposalPartition)
	if _, err := skillRepo.List(context.Background(), skillPartition, learning.SkillList{}); err != nil {
		t.Fatalf("list skills: %v", err)
	}
	wantPrincipal := opaqueLearningPartition(proposalPartition.Principal)
	wantProject := opaqueLearningPartition(proposalPartition.Project)
	if proposals.seen.Principal == proposalPartition.Principal || proposals.seen.Project == proposalPartition.Project {
		t.Fatalf("proposal transport leaked raw partition: %+v", proposals.seen)
	}
	if proposals.seen.Principal != wantPrincipal || proposals.seen.Project != wantProject {
		t.Fatalf("proposal transport partition = %+v, want opaque principal/project", proposals.seen)
	}
	if skills.seen.Principal == skillPartition.Principal || skills.seen.Project == skillPartition.Project {
		t.Fatalf("skill transport leaked raw partition: %+v", skills.seen)
	}
	if skills.seen.Principal != wantPrincipal || skills.seen.Project != wantProject {
		t.Fatalf("skill transport partition = %+v, want opaque principal/project", skills.seen)
	}

	cached := connections.conns[addr].conn
	closeDriver()
	if got := cached.GetState(); got != connectivity.Shutdown {
		t.Fatalf("learning driver connection state after close = %s, want SHUTDOWN", got)
	}
}
