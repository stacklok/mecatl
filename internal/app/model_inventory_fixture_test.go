package app

import (
	"sync/atomic"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"google.golang.org/protobuf/proto"
)

// modelInventoryFixture drives consumer-only tests. Discovery lifecycle tests
// use real providerDiscovery requests instead of publishing through this fixture.
type modelInventoryFixture struct {
	view atomic.Pointer[server.ModelSnapshot]
}

func newTestModelInventory(models []*mecatlv1.ModelInfo) *modelInventoryFixture {
	f := &modelInventoryFixture{}
	f.publish(models)
	return f
}
func (f *modelInventoryFixture) publish(models []*mecatlv1.ModelInfo) {
	f.view.Store(&server.ModelSnapshot{Models: cloneModelInfos(models)})
}
func (f *modelInventoryFixture) CurrentModelSnapshot() server.ModelSnapshot {
	view := f.view.Load()
	return server.ModelSnapshot{Models: cloneModelInfos(view.Models), ProviderStatus: cloneProviderStatuses(view.ProviderStatus)}
}

func cloneModelInfos(models []*mecatlv1.ModelInfo) []*mecatlv1.ModelInfo {
	cloned := make([]*mecatlv1.ModelInfo, len(models))
	for i, model := range models {
		if model != nil {
			cloned[i] = proto.Clone(model).(*mecatlv1.ModelInfo)
		}
	}
	return cloned
}

func cloneProviderStatuses(statuses []*mecatlv1.ProviderStatus) []*mecatlv1.ProviderStatus {
	cloned := make([]*mecatlv1.ProviderStatus, len(statuses))
	for i, status := range statuses {
		if status != nil {
			cloned[i] = proto.Clone(status).(*mecatlv1.ProviderStatus)
		}
	}
	return cloned
}
