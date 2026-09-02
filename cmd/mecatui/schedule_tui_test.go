package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/embed"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
	"github.com/stacklok/mecatl/internal/app"
)

// TestScheduleTool_TuiEmbeddedSchedulerOn pins AC2.4: mecatui's embedded server
// advertises `Scheduling` AND auto-fires on its default per-workspace store
// with NO flag — the on-by-default scheduler (Task 03) the TUI inherits by
// feeding embeddedConfig's SchedulerEnabled (== !--no-scheduler, true by
// default) into Build. The capability bit makes the `/schedule` overlay appear;
// the tick loop makes an in-chat/overlay one-shot fire unattended.
//
// The config half (embeddedConfig feeds SchedulerEnabled=true, no flag) is
// asserted directly against the REAL embeddedConfig; the auto-fire half drives
// embed.Start (the REAL Build composition the TUI hosts) over the mock
// provider with a due one-shot seeded into the store, asserting the tick loop
// claims it and mints a terminal sched-- fire — all offline.
func TestScheduleTool_TuiEmbeddedSchedulerOn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	workspace := t.TempDir()
	storeDir := t.TempDir()

	// The config half: the TUI's embeddedConfig must feed SchedulerEnabled
	// (true by default — the scheduler is on-by-default, AC2.1). This is what
	// makes the embedded server tick with no flag; without it the /schedule
	// overlay's auto-fire would stay dark.
	cfg := embeddedConfig(config{workspace: workspace, model: "mock-model", mock: true, storeDir: storeDir}, port.NopDiagnostics{})
	if !cfg.SchedulerEnabled {
		t.Fatal("embeddedConfig.SchedulerEnabled = false — the TUI's embedded server would never tick (the /schedule overlay's auto-fire would stay dark); want true (on-by-default, no flag)")
	}
	// A fast tick so the auto-fire engages quickly (mecated defaults 30s — the
	// test drives a faster cadence to stay well under the test deadline; the
	// production default is pinned by the mecated cmd tests).
	cfg.SchedulerTickInterval = 50 * time.Millisecond

	// Seed a due one-shot into the store the embedded server will use, BEFORE
	// Start (so the tick loop sees it as due on its first sweep).
	seedStore, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatalf("seed jsonlstore: %v", err)
	}
	schedStore := seedStore.ScheduleStore()
	const schedName = "tui-embedded-oneshot"
	due := time.Now().Add(50 * time.Millisecond)
	if err := schedStore.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name:      schedName,
			Prompt:    "say hello from the embedded scheduler",
			Workspace: workspace,
			Trigger:   port.TriggerSpec{OneShot: due},
		},
		State: port.ScheduleState{NextFireAt: due, Enabled: true},
	}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	// Start the embedded server exactly as the TUI hosts it (the mock provider,
	// offline), feeding the embeddedConfig scheduler knobs through.
	srv, err := embed.Start(ctx, app.Config{
		Workspace:             workspace,
		Model:                 "mock-model",
		UseMock:               true,
		Shell:                 "/bin/sh",
		Compaction:            "heuristic",
		Tokenizer:             "heuristic",
		StoreDir:              storeDir,
		SchedulerEnabled:      cfg.SchedulerEnabled,
		SchedulerTickInterval: cfg.SchedulerTickInterval,
	}, embed.PerfConfig{})
	if err != nil {
		t.Fatalf("embed.Start: %v", err)
	}
	defer func() { _ = srv.Close() }()

	// The capability half: the embedded server advertises `Scheduling` on the
	// default per-workspace store, so the /schedule overlay appears.
	cl, err := client.Dial(client.DialConfig{Server: srv.Target()})
	if err != nil {
		t.Fatalf("dial embedded server: %v", err)
	}
	defer func() { _ = cl.Close() }()
	_, caps, _, err := cl.CreateSession(ctx, client.ModeFromString("default"), client.ModelSelection{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if !caps.Scheduling {
		t.Fatal("embedded server Capabilities.Scheduling = false on the default per-workspace store, want true (the /schedule overlay must appear)")
	}

	// The auto-fire half: the tick loop claims the due one-shot and mints a
	// terminal sched-- fire with NO flag and NO manual FireNow.
	deadline := 15 * time.Second
	if !tuiSchedulerEventually(deadline, func() bool {
		fires, _ := schedStore.ListFires(ctx, schedName)
		for _, f := range fires {
			if f.Stop != "" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("the embedded scheduler did not auto-fire the due one-shot within %v (no flag — the on-by-default tick loop must claim it)", deadline)
	}
	fires, err := schedStore.ListFires(ctx, schedName)
	if err != nil || len(fires) == 0 {
		t.Fatalf("ListFires(%q) = (%v, %d), want the recorded fire", schedName, err, len(fires))
	}
	if !strings.HasPrefix(fires[0].ID, "sched--") {
		t.Fatalf("fire id = %q, want the sched-- minted session", fires[0].ID)
	}
	if fires[0].Stop != session.StopEndTurn {
		t.Fatalf("fire stop = %q, want end_turn (the sched-- fire session ran to a terminal stop)", fires[0].Stop)
	}
}

// tuiSchedulerEventually polls f every 20ms until it holds or d elapses (the
// fire's sched-- session runs a real mockllm turn, so the terminal stop lands
// asynchronously off the tick loop).
func tuiSchedulerEventually(d time.Duration, f func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return f()
}
