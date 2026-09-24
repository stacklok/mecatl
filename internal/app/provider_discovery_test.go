package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestProviderModelDiscovery_Scenario1_FirstDemandAndSharedAttempt(t *testing.T) {
	const providerID = "native-discovery-test"
	const modelID = "unknown-window"
	lister := &fakeLister{models: []modelEntry{{ID: modelID, ContextLimit: 262144}}}
	reg := &providerRegistry{
		entries: map[string]providerEntry{providerID: {id: providerID, available: true, nativeEndpoint: true, lister: lister}},
		meta:    newLiveMetaStore(),
	}
	reg.discovery = newProviderDiscovery(reg, Config{})
	reg.meta.owner = reg.discovery
	t.Cleanup(reg.discovery.Close)
	reg.discovery.start(true, 0)
	if lister.calls.Load() != 0 {
		t.Fatal("native startup performed authenticated discovery")
	}
	if err := awaitContextWindowWithin(context.Background(), reg, providerID, modelID, time.Second); err != nil {
		t.Fatalf("first unknown-window demand rejected without discovery: %v (listing calls=%d)", err, lister.calls.Load())
	}
	if got := lister.calls.Load(); got != 1 {
		t.Fatalf("first demand listing calls=%d, want 1", got)
	}
	if got := reg.echoWindowResolver(Config{}, providerID, modelID)(); got != 262144 {
		t.Fatalf("completed demand window=%d, want 262144", got)
	}

	t.Run("eligible callers join", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			release := make(chan struct{})
			defer closeIfOpen(release)
			var calls atomic.Int32
			d := discoveryFixture(t, map[string]providerEntry{"p": {lister: discoveryListerFunc(func(context.Context) ([]modelEntry, error) {
				calls.Add(1)
				<-release
				return []modelEntry{{ID: "m", ContextLimit: 262144}}, nil
			})}})
			done := make(chan error, 3)
			go func() { done <- awaitContextWindow(context.Background(), d.reg, "p", "m") }()
			synctest.Wait()
			for _, reason := range []discoveryReason{discoveryPicker, discoveryStartup} {
				go func() {
					view, err := d.request(context.Background(), "p", reason)
					if err == nil && resolveModelWindow(Config{}, view, "p", "m").tokens != 262144 {
						err = errors.New("joined caller missed completed publication")
					}
					done <- err
				}()
			}
			synctest.Wait()
			if calls.Load() != 1 {
				t.Fatalf("simultaneous requests started %d fetches", calls.Load())
			}
			close(release)
			for range 3 {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			_, discovered := executeDiscovery(t, d, `{}`)
			listed := d.CurrentModelSnapshot().Models
			if len(discovered.Models) != 1 || len(listed) != 1 || discovered.Models[0].ContextLimit != listed[0].ContextLimit || discovered.Models[0].ModelID != listed[0].Id {
				t.Fatal("DiscoverModels and inventory disagree")
			}
		})
	})

	t.Run("real native Build remains demand only", func(t *testing.T) {
		var calls atomic.Int32
		cfg := nativeRegistryConfig("native", &nativeBearerFixture{token: "fixture"}, nativeRoundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"native-model","context_window":222222}]}`))}, nil
		}))
		cfg.MockProvider = nil
		cfg.Workspace = t.TempDir()
		cfg.NoSoul, cfg.NoShell, cfg.NoUserModel = true, true, true
		cfg.envDetector = fakeEnv(nil)
		built, err := buildIsolated(t, context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer built.Close()
		if calls.Load() != 0 {
			t.Fatal("Build performed native authenticated listing")
		}
		view := built.Service.ListModelSnapshot(context.Background())
		if calls.Load() != 1 || len(view.Models) != 1 || view.Models[0].ContextLimit != 222222 {
			t.Fatalf("first demand calls=%d, snapshot=%+v", calls.Load(), view)
		}
	})
}

func TestProviderModelDiscovery_Scenario2_ScheduleInventoryReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		models := []modelEntry{{ID: "m", ContextLimit: 222222}}
		d := discoveryFixture(t, map[string]providerEntry{"p": {lister: discoveryListerFunc(func(context.Context) ([]modelEntry, error) { return models, nil })}})
		store := memstore.New()
		svc, err := newTestServerService(server.Config{
			Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog()}), Store: store, ModelInventory: d,
			ScheduleManager: server.NewScheduleManager(server.ScheduleManagerConfig{Store: store, ScheduleStore: memschedulestore.New()}),
		})
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Close()
		var refreshes atomic.Int32
		svc.SetModelsRefresher(func(context.Context) { refreshes.Add(1) })
		spec := port.ScheduleSpec{Name: "before", Prompt: "work", Mutating: true, Trigger: port.TriggerSpec{Cron: "* * * * *"}, Selector: port.ScheduleProviderSelector{ProviderID: "p", ModelID: "m"}}
		if _, err := svc.CreateSchedule(context.Background(), spec); !errors.Is(err, server.ErrInvalidArgument) {
			t.Fatalf("unlisted selector: %v", err)
		}
		_, _ = d.request(context.Background(), "p", discoveryPicker)
		synctest.Wait()
		spec.Name = "listed"
		if _, err := svc.CreateSchedule(context.Background(), spec); err != nil {
			t.Fatalf("new exact selector: %v", err)
		}
		models = []modelEntry{{ID: "replacement", ContextLimit: 333333}}
		time.Sleep(discoveryCooldown)
		_, _ = d.request(context.Background(), "p", discoveryPicker)
		spec.Name = "removed"
		if _, err := svc.CreateSchedule(context.Background(), spec); !errors.Is(err, server.ErrInvalidArgument) {
			t.Fatalf("removed exact selector: %v", err)
		}
		if refreshes.Load() != 0 {
			t.Fatal("schedule validation called effectful model refresher")
		}
	})
}
