package jsonlstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestRearmOneShotFailsClosedOnUnreadableHistory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	schedules := store.ScheduleStore()
	now := time.Unix(1_700_000_000, 0)
	const name = "retry"
	if err := schedules.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: name, Trigger: port.TriggerSpec{OneShot: now}, OneShotRetry: true, OneShotMaxRetries: 2}, State: port.ScheduleState{Enabled: true, NextFireAt: now}}); err != nil {
		t.Fatal(err)
	}
	if _, err := schedules.Claim(ctx, name, now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := schedules.RecordFire(ctx, port.ScheduleFire{ID: "failed", SessionID: "session", ScheduleName: name, FiredAt: now, Stop: session.StopError}); err != nil {
		t.Fatal(err)
	}
	if err := schedules.RecordFire(ctx, port.ScheduleFire{ID: "current", SessionID: "session", ScheduleName: name, FiredAt: now, Stop: session.StopEndTurn}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, scheduleFirePrefix+safeFilePart("current")+scheduleSuffix), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := schedules.Load(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := schedules.(port.ScheduleOneShotReArmer).ReArmOneShot(ctx, name, now.Add(time.Minute)); err == nil {
		t.Fatal("ReArmOneShot unexpectedly succeeded")
	}
	after, err := schedules.Load(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != before.State {
		t.Fatalf("failed ReArmOneShot mutated state: %+v -> %+v", before.State, after.State)
	}
}
