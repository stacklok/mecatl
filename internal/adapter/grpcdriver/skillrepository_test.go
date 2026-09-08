package grpcdriver

import (
	"testing"

	"google.golang.org/grpc"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/skillconformance"
	"github.com/stacklok/mecatl/engine/learning"
)

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
