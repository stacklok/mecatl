package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestScheduleTool_WireSurvivesSettingsCLIRemoval pins AC3.3 (schedule-tool
// acceptance plan): removing the settings.yaml `schedules:` block and the
// `mecated schedules` CLI (clean removal — the in-chat Schedule tool replaces
// them) does NOT touch the gRPC `ScheduleService` or the REST `/v1/schedules`
// routes — the mecatui /schedule overlay's transport and the out-of-band
// management surface keep serving create/list/get/update/delete/pause/resume/
// fire/fires. Driven over the REAL seams: NewHTTPHandler mounted on an
// httptest server + NewScheduleServer invoked directly, both over a
// jsonlstore-backed Service (a store that exposes a ScheduleStore).
func TestScheduleTool_WireSurvivesSettingsCLIRemoval(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	svc, schedStore := newScheduleService(t, now)
	ctx := context.Background()

	// --- gRPC ScheduleService -------------------------------------------------
	gsrv := server.NewScheduleServer(svc)

	if _, err := gsrv.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{Spec: &mecatlv1.ScheduleSpec{
		Name:      "wire-keep",
		Prompt:    "summarize commits",
		Workspace: "/repo",
		Mutating:  true,
		Trigger:   &mecatlv1.TriggerSpec{Cron: "0 9 * * *"},
	}}); err != nil {
		t.Fatalf("gRPC CreateSchedule: %v", err)
	}
	if _, err := gsrv.GetSchedule(ctx, &mecatlv1.GetScheduleRequest{Name: "wire-keep"}); err != nil {
		t.Fatalf("gRPC GetSchedule: %v", err)
	}
	if _, err := gsrv.ListSchedules(ctx, &mecatlv1.ListSchedulesRequest{}); err != nil {
		t.Fatalf("gRPC ListSchedules: %v", err)
	}
	if _, err := gsrv.UpdateSchedule(ctx, &mecatlv1.UpdateScheduleRequest{Spec: &mecatlv1.ScheduleSpec{
		Name:      "wire-keep",
		Prompt:    "summarize commits (updated)",
		Workspace: "/repo",
		Mutating:  true,
		Trigger:   &mecatlv1.TriggerSpec{Cron: "0 10 * * *"},
	}}); err != nil {
		t.Fatalf("gRPC UpdateSchedule: %v", err)
	}
	if _, err := gsrv.PauseSchedule(ctx, &mecatlv1.PauseScheduleRequest{Name: "wire-keep"}); err != nil {
		t.Fatalf("gRPC PauseSchedule: %v", err)
	}
	if _, err := gsrv.ResumeSchedule(ctx, &mecatlv1.ResumeScheduleRequest{Name: "wire-keep"}); err != nil {
		t.Fatalf("gRPC ResumeSchedule: %v", err)
	}
	// fires: seed one fire record directly in the store (a REAL FireNow needs
	// the running scheduler, pinned separately by the FireNow e2e), then the
	// read verbs must serve it.
	if err := schedStore.RecordFire(ctx, port.ScheduleFire{
		ID:           "sched--wire-keep-1",
		ScheduleName: "wire-keep",
		SessionID:    "sched--wire-keep-1",
		FiredAt:      now,
		Stop:         "end_turn",
	}); err != nil {
		t.Fatalf("RecordFire: %v", err)
	}
	if _, err := gsrv.GetFire(ctx, &mecatlv1.GetFireRequest{FireId: "sched--wire-keep-1"}); err != nil {
		t.Fatalf("gRPC GetFire: %v", err)
	}
	if _, err := gsrv.ListFires(ctx, &mecatlv1.ListFiresRequest{ScheduleName: "wire-keep"}); err != nil {
		t.Fatalf("gRPC ListFires: %v", err)
	}
	// Delete is exercised on a THROWAWAY schedule — wire-keep survives (the
	// REST half lists its fires below; ListFires is keyed on an existing
	// schedule name).
	if _, err := gsrv.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{Spec: &mecatlv1.ScheduleSpec{
		Name:      "grpc-del",
		Prompt:    "x",
		Workspace: "/repo",
		Mutating:  true,
		Trigger:   &mecatlv1.TriggerSpec{Cron: "0 1 * * *"},
	}}); err != nil {
		t.Fatalf("gRPC CreateSchedule (throwaway): %v", err)
	}
	if _, err := gsrv.DeleteSchedule(ctx, &mecatlv1.DeleteScheduleRequest{Name: "grpc-del"}); err != nil {
		t.Fatalf("gRPC DeleteSchedule: %v", err)
	}

	// --- REST /v1/schedules ---------------------------------------------------
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	do := func(method, path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		if body != "" {
			req.Header.Set("content-type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}

	// POST /v1/schedules (create).
	if resp := do("POST", "/v1/schedules", `{"name":"rest-keep","prompt":"hi","workspace":"/repo","mutating":true,"trigger":{"cron":"0 9 * * *"}}`); resp.StatusCode != http.StatusCreated {
		_ = resp.Body.Close()
		t.Fatalf("POST /v1/schedules status = %d, want 201", resp.StatusCode)
	} else {
		_ = resp.Body.Close()
	}

	// list/get/update/pause/resume/fires/delete on the REST surface (the
	// fires are listed on the schedule created via gRPC above — ListFires is
	// keyed on an existing schedule name).
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/v1/schedules", "", http.StatusOK},
		{"GET", "/v1/schedules/rest-keep", "", http.StatusOK},
		{"PUT", "/v1/schedules/rest-keep", `{"prompt":"hi2","workspace":"/repo","mutating":true,"trigger":{"cron":"0 10 * * *"}}`, http.StatusOK},
		{"POST", "/v1/schedules/rest-keep/pause", "", http.StatusNoContent},
		{"POST", "/v1/schedules/rest-keep/resume", "", http.StatusNoContent},
		{"GET", "/v1/schedules/wire-keep/fires", "", http.StatusOK},
		{"GET", "/v1/schedules/wire-keep/fires/sched--wire-keep-1", "", http.StatusOK},
		{"DELETE", "/v1/schedules/rest-keep", "", http.StatusNoContent},
	} {
		resp := do(tc.method, tc.path, tc.body)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("%s %s status = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}
}
