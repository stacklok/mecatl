package server_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// schedule_validation_test.go pins the two NEW create-seam validations the
// on-by-default + in-chat-create posture needs (ADR 0073, schedule-tool task
// 03): the SchedulerMinInterval cadence floor (AC1.3) and the
// provider+model selector rejection (AC1.2c). Both live in the SHARED seam
// (validateScheduleSpec over the Service) so the Schedule tool's create AND
// the REST/gRPC handler inherit them — the composition-side pins in
// internal/app/scheduletool_test.go drive the same rejections THROUGH the
// tool.

// newValidatedScheduleService builds a Service over a jsonlstore with the
// given scheduler cadence floor and model inventory, mirroring how
// composition (internal/app Build) now threads cfg.SchedulerMinInterval +
// the projected selectable-model snapshot into server.Config.
func newValidatedScheduleService(t *testing.T, now time.Time, minInterval time.Duration, models []*mecatlv1.ModelInfo) *server.Service {
	t.Helper()
	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("jsonlstore: %v", err)
	}
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:           engine,
		Store:            store,
		SharedEngineRoot: "/ws",

		Now:    func() time.Time { return now },
		Models: models,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	// Composition (internal/app Build) threads the scheduler cadence floor
	// into the Service create path after construction (the Service owns the
	// create-seam; the floor is a scheduler/composition knob).
	svc.SetScheduleMinInterval(minInterval)
	return svc
}

// TestCreateScheduleEnforcesMinIntervalSeam pins the SERVICE-side half of
// AC1.3: a cadence tighter than the configured SchedulerMinInterval floor is
// rejected fail-closed at the SHARED create-seam (the floor is no longer
// inert), for BOTH the fixed-cron and the @every cadence forms; a cadence at
// or above the floor passes; a zero floor (0 = no floor) consults nothing;
// and UpdateSchedule (the shared-seam sibling) enforces the same floor.
func TestCreateScheduleEnforcesMinIntervalSeam(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ctx := context.Background()

	svc := newValidatedScheduleService(t, now, 5*time.Minute, nil)

	for _, tc := range []struct {
		name string
		cron string
	}{
		{"every-minute cron", "* * * * *"},
		{"@every 1m macro", "@every 1m"},
		{"@every 30s macro", "@every 30s"},
	} {
		spec := port.ScheduleSpec{
			Name: "tight-" + strings.ReplaceAll(tc.name, " ", "-"), Prompt: "p",
			Trigger: port.TriggerSpec{Cron: tc.cron}, Mode: session.ModePlan,
		}
		if _, err := svc.CreateSchedule(ctx, spec); !errors.Is(err, server.ErrInvalidArgument) {
			t.Fatalf("%s: CreateSchedule = %v, want ErrInvalidArgument (a cadence tighter than the 5m floor is rejected fail-closed)", tc.name, err)
		}
		if _, err := svc.GetSchedule(ctx, spec.Name); !errors.Is(err, port.ErrScheduleNotFound) {
			t.Fatalf("%s: the rejected schedule was SAVED (fail-closed means not saved)", tc.name)
		}
	}

	// At and above the floor: accepted.
	for _, cron := range []string{"*/5 * * * *", "@every 5m", "@hourly", "0 9 * * *"} {
		if _, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
			Name: "ok-" + strings.NewReplacer("*", "s", "/", "-", " ", "-", "@", "a").Replace(cron), Prompt: "p",
			Trigger: port.TriggerSpec{Cron: cron}, Mode: session.ModePlan,
		}); err != nil {
			t.Fatalf("cron %q at/above the 5m floor = %v, want accepted", cron, err)
		}
	}

	// A one-shot is a single fire, not a cadence: unaffected by the floor.
	if _, err := svc.CreateSchedule(ctx, port.ScheduleSpec{
		Name: "oneshot", Prompt: "p", Trigger: port.TriggerSpec{OneShot: now.Add(time.Minute)},
		Mode: session.ModePlan,
	}); err != nil {
		t.Fatalf("one-shot create with a floor configured = %v, want accepted (a one-shot has no cadence)", err)
	}

	// The floor is SHARED with UpdateSchedule (the same seam): tightening an
	// existing schedule below the floor is rejected too.
	if _, err := svc.UpdateSchedule(ctx, port.ScheduleSpec{
		Name: "oneshot", Prompt: "p", Trigger: port.TriggerSpec{Cron: "* * * * *"},
		Mode: session.ModePlan,
	}); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("UpdateSchedule to a below-floor cadence = %v, want ErrInvalidArgument (the seam is shared)", err)
	}

	// A zero floor (0 = no floor) consults nothing: the tightest cadence
	// passes (the pre-feature posture).
	noFloor := newValidatedScheduleService(t, now, 0, nil)
	if _, err := noFloor.CreateSchedule(ctx, port.ScheduleSpec{
		Name: "tight-ok", Prompt: "p", Trigger: port.TriggerSpec{Cron: "* * * * *"},
		Mode: session.ModePlan,
	}); err != nil {
		t.Fatalf("create with NO floor configured = %v, want accepted (0 = no floor, byte-identical pre-feature posture)", err)
	}
}

// TestCreateScheduleRejectsUnknownSelectorSeam pins the SERVICE-side half of
// AC1.2c: a non-empty selector naming a provider the deployment never
// configured, or a model the provider does not serve (not in the projected
// selectable-model inventory), is rejected fail-closed at the SHARED
// create-seam — a schedule fire must not silently target an unknown provider
// and surface hours later as a fire-time failure. An empty selector (the
// deployment default) is ALWAYS valid, as is a known provider+model pair.
func TestCreateScheduleRejectsUnknownSelectorSeam(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ctx := context.Background()

	models := []*mecatlv1.ModelInfo{
		{Id: "gpt-5", ProviderId: "openai"},
		{Id: "glm-5", ProviderId: "openrouter"},
	}
	svc := newValidatedScheduleService(t, now, 0, models)

	base := port.ScheduleSpec{
		Prompt: "p", Trigger: port.TriggerSpec{Cron: "0 9 * * *"},
		Mode: session.ModePlan,
	}

	// Unknown provider: rejected, never saved.
	unknownProvider := base
	unknownProvider.Name = "bad-provider"
	unknownProvider.Selector = port.ScheduleProviderSelector{ProviderID: "nonexistent", ModelID: "gpt-5"}
	if _, err := svc.CreateSchedule(ctx, unknownProvider); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("unknown provider selector: CreateSchedule = %v, want ErrInvalidArgument (fail-closed, like an invalid cron)", err)
	}
	if _, err := svc.GetSchedule(ctx, "bad-provider"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Fatal("the unknown-provider schedule was SAVED (fail-closed means not saved)")
	}

	// Known provider, uncatalogued model: rejected.
	unknownModel := base
	unknownModel.Name = "bad-model"
	unknownModel.Selector = port.ScheduleProviderSelector{ProviderID: "openai", ModelID: "no-such-model"}
	if _, err := svc.CreateSchedule(ctx, unknownModel); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("uncatalogued model selector: CreateSchedule = %v, want ErrInvalidArgument", err)
	}
	if _, err := svc.GetSchedule(ctx, "bad-model"); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Fatal("the uncatalogued-model schedule was SAVED (fail-closed means not saved)")
	}

	// A model id catalogued under a DIFFERENT provider is not served by this
	// one: rejected.
	crossProvider := base
	crossProvider.Name = "cross-provider"
	crossProvider.Selector = port.ScheduleProviderSelector{ProviderID: "openai", ModelID: "glm-5"}
	if _, err := svc.CreateSchedule(ctx, crossProvider); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("cross-provider model selector: CreateSchedule = %v, want ErrInvalidArgument", err)
	}

	// A known provider+model pair: accepted.
	known := base
	known.Name = "known"
	known.Selector = port.ScheduleProviderSelector{ProviderID: "openai", ModelID: "gpt-5"}
	if _, err := svc.CreateSchedule(ctx, known); err != nil {
		t.Fatalf("known provider+model selector = %v, want accepted", err)
	}

	// An empty selector (the deployment default) is ALWAYS valid.
	empty := base
	empty.Name = "default-selector"
	if _, err := svc.CreateSchedule(ctx, empty); err != nil {
		t.Fatalf("empty selector (deployment default) = %v, want always valid", err)
	}
}
