package server_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type changingModelInventory struct{ reads atomic.Int32 }

func (i *changingModelInventory) CurrentModelSnapshot() server.ModelSnapshot {
	n := i.reads.Add(1)
	return server.ModelSnapshot{
		Models:         []*mecatlv1.ModelInfo{{ProviderId: "p", Id: "m", ContextLimit: int64(n)}},
		ProviderStatus: []*mecatlv1.ProviderStatus{{ProviderId: "p", ModelCount: n}},
	}
}

type publishedModelInventory struct {
	view atomic.Pointer[server.ModelSnapshot]
}

func (i *publishedModelInventory) CurrentModelSnapshot() server.ModelSnapshot { return *i.view.Load() }

func discoveryInventoryService(t *testing.T, inventory server.ModelInventory) *server.Service {
	t.Helper()
	store := memstore.New()
	svc, err := newPlacementTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog()}),
		Store:  store, ModelInventory: inventory,
		ScheduleManager: server.NewScheduleManager(server.ScheduleManagerConfig{Store: store, ScheduleStore: memschedulestore.New()}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func TestProviderModelDiscovery_Scenario2_CoherentPublication(t *testing.T) {
	inventory := &changingModelInventory{}
	svc := discoveryInventoryService(t, inventory)
	assert := func(resp *mecatlv1.ListModelsResponse, before int32) {
		t.Helper()
		if len(resp.Models) != 1 || len(resp.ProviderStatus) != 1 || resp.Models[0].ContextLimit != int64(resp.ProviderStatus[0].ModelCount) {
			t.Fatalf("mixed generation: %v", resp)
		}
		if inventory.reads.Load() != before+1 {
			t.Fatalf("response loaded %d snapshots, want one", inventory.reads.Load()-before)
		}
	}
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	before := inventory.reads.Load()
	resp, err := client.ListModels(context.Background(), &mecatlv1.ListModelsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	assert(resp, before)
	httpServer := httptest.NewServer(server.NewHTTPHandler(svc))
	defer httpServer.Close()
	before = inventory.reads.Load()
	var response mecatlv1.ListModelsResponse
	if code := httpGet(t, httpServer, "/v1/models", &response); code != 200 {
		t.Fatalf("status=%d", code)
	}
	assert(&response, before)
}

func TestProviderModelDiscovery_Scenario2_ScheduleInventoryReader(t *testing.T) {
	inventory := &publishedModelInventory{}
	inventory.view.Store(&server.ModelSnapshot{})
	svc := discoveryInventoryService(t, inventory)
	var listings atomic.Int32
	svc.SetModelsRefresher(func(context.Context) { listings.Add(1) })
	spec := testSchedule("before")
	spec.Selector = port.ScheduleProviderSelector{ProviderID: "p", ModelID: "m"}
	if _, err := svc.CreateSchedule(context.Background(), spec); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("unlisted selector: %v", err)
	}
	inventory.view.Store(&server.ModelSnapshot{Models: []*mecatlv1.ModelInfo{{ProviderId: "p", Id: "m"}}})
	spec.Name = "listed"
	if _, err := svc.CreateSchedule(context.Background(), spec); err != nil {
		t.Fatalf("newly listed selector: %v", err)
	}
	inventory.view.Store(&server.ModelSnapshot{})
	spec.Name = "removed"
	if _, err := svc.CreateSchedule(context.Background(), spec); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("removed selector: %v", err)
	}
	// Compatibility setters must never become another writer for a wired owner.
	svc.SetModels([]*mecatlv1.ModelInfo{{ProviderId: "p", Id: "m"}})
	if _, err := svc.CreateSchedule(context.Background(), spec); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("SetModels bypassed owner: %v", err)
	}
	if listings.Load() != 0 {
		t.Fatal("schedule validation requested discovery")
	}
}
