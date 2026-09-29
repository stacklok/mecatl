package grpcdriver

import (
	"testing"

	"google.golang.org/grpc"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/skillconformance"
	"github.com/stacklok/mecatl/engine/learning"
)

func TestSkillRepositoryDriverReject(t *testing.T) {
	conn := dialBufconn(t, func(server *grpc.Server) {
		driverv1.RegisterSkillRepositoryServiceServer(server, NewSkillRepositoryServer(memskill.New()))
	})
	repo := NewSkillRepository(conn)
	partition := learning.SkillPartition{Principal: "principal", Project: "project"}
	draft, err := repo.CreateDraft(t.Context(), partition, "agent", learning.SkillBundle{Name: "reject-skill", Description: "Reject through the driver.", Body: "Reject this draft."}, learning.SkillProvenance{Origin: learning.SkillProvenanceLegacyModel})
	if err != nil {
		t.Fatal(err)
	}

	rejected, err := repo.Reject(t.Context(), partition, "agent", draft.ID, draft.Version, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.State != learning.SkillRejected || rejected.Receipts[len(rejected.Receipts)-1].Operation != "reject" {
		t.Fatalf("rejected=%+v", rejected)
	}
}

func TestValidatedSkillRepositoryDriverConforms(t *testing.T) {
	factory := func(t *testing.T) learning.SkillRepository {
		t.Helper()
		conn := dialBufconn(t, func(server *grpc.Server) {
			driverv1.RegisterSkillRepositoryServiceServer(server, NewSkillRepositoryServer(memskill.New()))
		})
		return NewValidatedSkillRepository(conn)
	}

	skillconformance.RunValidatedActivation(t, factory)
}
