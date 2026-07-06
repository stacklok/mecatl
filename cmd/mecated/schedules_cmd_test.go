package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockScheduleServer is a minimal httptest.Server whose handler records the
// requests it received and replays canned responses. It exercises the CLI
// client's HTTP plumbing + output formatting, NOT the server adapter (which has
// its own conformance suite under internal/adapter/server).
type mockScheduleServer struct {
	t        *testing.T
	requests []recordedReq
	handler  func(req recordedReq) (int, []byte)
}

type recordedReq struct {
	method string
	path   string
	body   []byte
}

func newMockScheduleServer(t *testing.T, h func(req recordedReq) (int, []byte)) *mockScheduleServer {
	return &mockScheduleServer{t: t, handler: h}
}

func (m *mockScheduleServer) serve() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req := recordedReq{method: r.Method, path: r.URL.Path, body: body}
		m.requests = append(m.requests, req)
		code, resp := m.handler(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write(resp)
	}))
}

// runSchedulesAt invokes runSchedules against addr with the given argv (verb
// first). It returns the stdout, stderr, and error.
func runSchedulesAt(t *testing.T, addr string, argv ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut strings.Builder
	verb := argv[0]
	rest := append([]string{"--server-addr", addr}, argv[1:]...)
	err = runSchedules(append([]string{verb}, rest...), &out, &errOut)
	return out.String(), errOut.String(), err
}

// mustRunSchedules invokes runSchedules against addr with the given argv and
// fails the test if it returns a non-nil error.
func mustRunSchedules(t *testing.T, addr string, argv ...string) (stdout, stderr string) {
	t.Helper()
	out, errOut, err := runSchedulesAt(t, addr, argv...)
	if err != nil {
		t.Fatalf("runSchedules %v: %v\nstderr: %s", argv, err, errOut)
	}
	return out, errOut
}

// mustRunSchedulesErr invokes runSchedules and asserts it returns a non-nil
// error (a required-flag-missing req.path, etc.).
func mustRunSchedulesErr(t *testing.T, addr string, want string, argv ...string) {
	t.Helper()
	_, _, err := runSchedulesAt(t, addr, argv...)
	if err == nil {
		t.Fatalf("runSchedules %v: expected error containing %q, got nil", argv, want)
	}
	if want != "" && !strings.Contains(err.Error(), want) {
		t.Fatalf("runSchedules %v: error %q does not contain %q", argv, err.Error(), want)
	}
}

// --- usage / unknown verb ---------------------------------------------------

func TestSchedulesBareExitsUsage(t *testing.T) {
	var out, errOut strings.Builder
	err := runSchedules(nil, &out, &errOut)
	if !errors.Is(err, errSchedulesUsage) {
		t.Fatalf("bare schedules: want errSchedulesUsage, got %v", err)
	}
	if !strings.Contains(errOut.String(), "missing subcommand") {
		t.Fatalf("bare schedules stderr should mention missing subcommand: %s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "available subcommands") {
		t.Fatalf("bare schedules stderr should list subcommands: %s", errOut.String())
	}
}

func TestSchedulesUnknownVerbExitsUsage(t *testing.T) {
	var out, errOut strings.Builder
	err := runSchedules([]string{"bogus"}, &out, &errOut)
	if !errors.Is(err, errSchedulesUsage) {
		t.Fatalf("unknown verb: want errSchedulesUsage, got %v", err)
	}
	if !strings.Contains(errOut.String(), `unknown subcommand "bogus"`) {
		t.Fatalf("unknown verb stderr should name the bogus verb: %s", errOut.String())
	}
}

// --- create -----------------------------------------------------------------

func TestSchedulesCreateCron(t *testing.T) {
	var gotBody []byte
	m := newMockScheduleServer(t, func(req recordedReq) (int, []byte) {
		if req.method != http.MethodPost || req.path != "/v1/schedules" {
			t.Fatalf("unexpected request: %s %s", req.method, req.path)
		}
		gotBody = req.body
		resp, _ := json.Marshal(map[string]any{
			"schedule": map[string]any{
				"spec": map[string]any{
					"name":      "nightly",
					"prompt":    "say hi",
					"singleton": true,
					"timezone":  "UTC",
				},
			},
		})
		return http.StatusCreated, resp
	})
	ts := m.serve()
	defer ts.Close()

	stdout, _ := mustRunSchedules(t, ts.URL, "create", "--name", "nightly", "--cron", "0 2 * * *", "--prompt", "say hi")

	var spec map[string]any
	if err := json.Unmarshal(gotBody, &spec); err != nil {
		t.Fatalf("request req.body not valid JSON: %v\n%s", err, gotBody)
	}
	if spec["name"] != "nightly" {
		t.Errorf("name: want nightly, got %v", spec["name"])
	}
	if spec["prompt"] != "say hi" {
		t.Errorf("prompt: want 'say hi', got %v", spec["prompt"])
	}
	trig, _ := spec["trigger"].(map[string]any)
	if trig == nil || trig["cron"] != "0 2 * * *" {
		t.Errorf("trigger.cron missing/wrong: %v", spec["trigger"])
	}
	if spec["singleton"] != true {
		t.Errorf("singleton should default true, got %v", spec["singleton"])
	}
	if spec["timezone"] != "UTC" {
		t.Errorf("timezone: want UTC, got %v", spec["timezone"])
	}
	if !strings.Contains(stdout, "nightly") {
		t.Errorf("stdout should contain the schedule name: %s", stdout)
	}
}

// TestSchedulesCreateModeDefaulting pins the create-verb mode behavior: a
// non-mutating schedule (the default) sends the plan enum without the operator
// having to pass --mode (mirrors the declarative fold and the server's
// non-mutating-must-be-plan invariant); a mutating schedule sends no mode
// (server default); an explicit --mode is translated to the protojson enum
// name; and --workspace round-trips.
func TestSchedulesCreateModeDefaulting(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantMode any // nil = mode key must be absent
	}{
		{"non-mutating defaults to plan", []string{"create", "--name", "s", "--cron", "0 2 * * *", "--prompt", "p"}, "PERMISSION_MODE_PLAN"},
		{"mutating sends no mode", []string{"create", "--name", "s", "--cron", "0 2 * * *", "--prompt", "p", "--mutating"}, nil},
		{"explicit mode default", []string{"create", "--name", "s", "--cron", "0 2 * * *", "--prompt", "p", "--mutating", "--mode", "default"}, "PERMISSION_MODE_DEFAULT"},
		{"explicit mode acceptEdits", []string{"create", "--name", "s", "--cron", "0 2 * * *", "--prompt", "p", "--mutating", "--mode", "acceptEdits"}, "PERMISSION_MODE_ACCEPT_EDITS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody []byte
			m := newMockScheduleServer(t, func(req recordedReq) (int, []byte) {
				gotBody = req.body
				resp, _ := json.Marshal(map[string]any{"schedule": map[string]any{"spec": map[string]any{"name": "s"}}})
				return http.StatusCreated, resp
			})
			ts := m.serve()
			defer ts.Close()
			mustRunSchedules(t, ts.URL, tc.args...)
			var spec map[string]any
			if err := json.Unmarshal(gotBody, &spec); err != nil {
				t.Fatalf("body not JSON: %v\n%s", err, gotBody)
			}
			got, present := spec["mode"]
			if tc.wantMode == nil {
				if present {
					t.Errorf("mode should be absent for a mutating create, got %v", got)
				}
				return
			}
			if got != tc.wantMode {
				t.Errorf("mode: want %v, got %v", tc.wantMode, got)
			}
		})
	}

	t.Run("workspace round-trips", func(t *testing.T) {
		var gotBody []byte
		m := newMockScheduleServer(t, func(req recordedReq) (int, []byte) {
			gotBody = req.body
			resp, _ := json.Marshal(map[string]any{"schedule": map[string]any{"spec": map[string]any{"name": "s"}}})
			return http.StatusCreated, resp
		})
		ts := m.serve()
		defer ts.Close()
		mustRunSchedules(t, ts.URL, "create", "--name", "s", "--cron", "0 2 * * *", "--prompt", "p", "--workspace", "/repo")
		var spec map[string]any
		_ = json.Unmarshal(gotBody, &spec)
		if spec["workspace"] != "/repo" {
			t.Errorf("workspace: want /repo, got %v", spec["workspace"])
		}
	})

	t.Run("invalid mode rejected client-side", func(t *testing.T) {
		m := newMockScheduleServer(t, func(recordedReq) (int, []byte) {
			t.Fatal("server should not be dialed on an invalid --mode")
			return 0, nil
		})
		ts := m.serve()
		defer ts.Close()
		if err := runSchedules([]string{"create", "--server-addr", ts.URL, "--name", "s", "--cron", "0 2 * * *", "--prompt", "p", "--mode", "bogus"}, io.Discard, io.Discard); err == nil {
			t.Fatal("expected an error for --mode bogus")
		}
	})
}

func TestSchedulesCreateOneShot(t *testing.T) {
	var gotBody []byte
	m := newMockScheduleServer(t, func(req recordedReq) (int, []byte) {
		gotBody = req.body
		resp, _ := json.Marshal(map[string]any{"schedule": map[string]any{"spec": map[string]any{"name": "once"}}})
		return http.StatusCreated, resp
	})
	ts := m.serve()
	defer ts.Close()

	_, _ = mustRunSchedules(t, ts.URL, "create", "--name", "once", "--one-shot", "2030-01-01T00:00:00Z", "--prompt", "boom")

	var spec map[string]any
	if err := json.Unmarshal(gotBody, &spec); err != nil {
		t.Fatalf("request req.body not valid JSON: %v\n%s", err, gotBody)
	}
	trig, _ := spec["trigger"].(map[string]any)
	if trig == nil {
		t.Fatalf("trigger missing: %v", spec["trigger"])
	}
	if trig["cron"] != nil && trig["cron"] != "" {
		t.Errorf("one-shot must not set cron: %v", trig["cron"])
	}
	if trig["one_shot"] == nil {
		t.Errorf("one-shot must set one_shot: %v", trig)
	}
}

func TestSchedulesCreateMissingName(t *testing.T) {
	m := newMockScheduleServer(t, func(_ recordedReq) (int, []byte) {
		t.Fatalf("should not dial server on a missing required flag")
		return 500, nil
	})
	ts := m.serve()
	defer ts.Close()
	mustRunSchedulesErr(t, ts.URL, "--name", "create", "--cron", "0 2 * * *", "--prompt", "hi")
}

func TestSchedulesCreateMissingTrigger(t *testing.T) {
	m := newMockScheduleServer(t, func(_ recordedReq) (int, []byte) {
		t.Fatalf("should not dial server on a missing required flag")
		return 500, nil
	})
	ts := m.serve()
	defer ts.Close()
	mustRunSchedulesErr(t, ts.URL, "--cron or --one-shot", "create", "--name", "x", "--prompt", "hi")
}

func TestSchedulesCreateBothTriggers(t *testing.T) {
	m := newMockScheduleServer(t, func(_ recordedReq) (int, []byte) {
		t.Fatalf("should not dial server on mutually-exclusive flags")
		return 500, nil
	})
	ts := m.serve()
	defer ts.Close()
	mustRunSchedulesErr(t, ts.URL, "mutually exclusive", "create", "--name", "x", "--cron", "0 2 * * *", "--one-shot", "2030-01-01T00:00:00Z", "--prompt", "hi")
}

func TestSchedulesCreateMissingPrompt(t *testing.T) {
	m := newMockScheduleServer(t, func(_ recordedReq) (int, []byte) {
		t.Fatalf("should not dial server on a missing required flag")
		return 500, nil
	})
	ts := m.serve()
	defer ts.Close()
	mustRunSchedulesErr(t, ts.URL, "--prompt", "create", "--name", "x", "--cron", "0 2 * * *")
}

func TestSchedulesCreateOneShotInvalidRFC3339(t *testing.T) {
	m := newMockScheduleServer(t, func(_ recordedReq) (int, []byte) {
		t.Fatalf("should not dial server on an invalid --one-shot")
		return 500, nil
	})
	ts := m.serve()
	defer ts.Close()
	mustRunSchedulesErr(t, ts.URL, "RFC3339", "create", "--name", "x", "--one-shot", "not-a-date", "--prompt", "hi")
}

func TestSchedulesCreateProviderModelMaxTurnsTransmitted(t *testing.T) {
	var gotBody []byte
	m := newMockScheduleServer(t, func(req recordedReq) (int, []byte) {
		gotBody = req.body
		resp, _ := json.Marshal(map[string]any{"schedule": map[string]any{"spec": map[string]any{"name": "x"}}})
		return http.StatusCreated, resp
	})
	ts := m.serve()
	defer ts.Close()

	_, _ = mustRunSchedules(t, ts.URL, "create", "--name", "x", "--cron", "0 2 * * *", "--prompt", "hi",
		"--provider", "openai", "--model", "gpt-4o", "--max-turns", "7")

	var spec map[string]any
	if err := json.Unmarshal(gotBody, &spec); err != nil {
		t.Fatalf("request body not valid JSON: %v\n%s", err, gotBody)
	}
	sel, ok := spec["selector"].(map[string]any)
	if !ok {
		t.Fatalf("selector missing: %v", spec["selector"])
	}
	if sel["provider_id"] != "openai" {
		t.Errorf("selector.provider_id: want openai, got %v", sel["provider_id"])
	}
	if sel["model_id"] != "gpt-4o" {
		t.Errorf("selector.model_id: want gpt-4o, got %v", sel["model_id"])
	}
	lim, ok := spec["limits"].(map[string]any)
	if !ok {
		t.Fatalf("limits missing: %v", spec["limits"])
	}
	if lim["max_turns"] != float64(7) {
		t.Errorf("limits.max_turns: want 7, got %v", lim["max_turns"])
	}
}

func TestSchedulesCreateMaxTokensNotTransmitted(t *testing.T) {
	var gotBody []byte
	m := newMockScheduleServer(t, func(req recordedReq) (int, []byte) {
		gotBody = req.body
		resp, _ := json.Marshal(map[string]any{"schedule": map[string]any{"spec": map[string]any{"name": "x"}}})
		return http.StatusCreated, resp
	})
	ts := m.serve()
	defer ts.Close()

	_, _ = mustRunSchedules(t, ts.URL, "create", "--name", "x", "--cron", "0 2 * * *", "--prompt", "hi",
		"--max-tokens", "4096")

	var spec map[string]any
	if err := json.Unmarshal(gotBody, &spec); err != nil {
		t.Fatalf("request body not valid JSON: %v\n%s", err, gotBody)
	}
	// max_tokens is deliberately NOT in the v1 Limits proto, so it must not
	// appear anywhere in the serialized request body.
	if _, present := spec["max_tokens"]; present {
		t.Errorf("max_tokens must not be transmitted: %v", spec["max_tokens"])
	}
	if lim, ok := spec["limits"].(map[string]any); ok {
		if _, present := lim["max_tokens"]; present {
			t.Errorf("limits.max_tokens must not be transmitted: %v", lim["max_tokens"])
		}
	}
}

func TestValidateScheduleName(t *testing.T) {
	for _, name := range []string{"nightly-review", "my.schedule"} {
		if err := validateScheduleName(name); err != nil {
			t.Errorf("validateScheduleName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range []string{".", ".."} {
		if err := validateScheduleName(name); err == nil {
			t.Errorf("validateScheduleName(%q) = nil, want error", name)
		}
	}
}

func TestSchedulesCreateInvalidNameRejected(t *testing.T) {
	for _, bad := range []string{"../admin", "a/b", "a?b", "a#b", ".", ".."} {
		m := newMockScheduleServer(t, func(_ recordedReq) (int, []byte) {
			t.Fatalf("should not dial server for an invalid name")
			return 500, nil
		})
		ts := m.serve()
		mustRunSchedulesErr(t, ts.URL, "invalid schedule name", "create", "--name", bad, "--cron", "0 2 * * *", "--prompt", "hi")
		ts.Close()
	}
}

func TestSchedulesInvalidNameRejectedAcrossVerbs(t *testing.T) {
	for _, verb := range []string{"inspect", "pause", "resume", "delete", "fire"} {
		for _, bad := range []string{"a/b", ".", ".."} {
			m := newMockScheduleServer(t, func(_ recordedReq) (int, []byte) {
				t.Fatalf("should not dial server for an invalid name")
				return 500, nil
			})
			ts := m.serve()
			mustRunSchedulesErr(t, ts.URL, "invalid schedule name", verb, "--name", bad)
			ts.Close()
		}
	}
}

// --- list -------------------------------------------------------------------

func TestSchedulesList(t *testing.T) {
	m := newMockScheduleServer(t, func(req recordedReq) (int, []byte) {
		if req.method != http.MethodGet || req.path != "/v1/schedules" {
			t.Fatalf("unexpected request: %s %s", req.method, req.path)
		}
		resp, _ := json.Marshal(map[string]any{
			"schedules": []map[string]any{
				{"spec": map[string]any{"name": "a"}},
				{"spec": map[string]any{"name": "b"}},
			},
		})
		return http.StatusOK, resp
	})
	ts := m.serve()
	defer ts.Close()

	stdout, _ := mustRunSchedules(t, ts.URL, "list")
	if !strings.Contains(stdout, "a") || !strings.Contains(stdout, "b") {
		t.Errorf("list stdout should contain both schedule names: %s", stdout)
	}
}

// --- inspect ----------------------------------------------------------------

func TestSchedulesInspect(t *testing.T) {
	var paths []string
	m := newMockScheduleServer(t, func(req recordedReq) (int, []byte) {
		paths = append(paths, req.method+" "+req.path)
		resp, _ := json.Marshal(map[string]any{"schedule": map[string]any{"spec": map[string]any{"name": "x"}}})
		return http.StatusOK, resp
	})
	ts := m.serve()
	defer ts.Close()

	stdout, _ := mustRunSchedules(t, ts.URL, "inspect", "--name", "x")
	if !strings.Contains(stdout, "x") {
		t.Errorf("inspect stdout should contain the schedule name: %s", stdout)
	}
	wantGet := "GET /v1/schedules/x"
	if len(paths) != 1 || paths[0] != wantGet {
		t.Errorf("inspect without --fires should issue %q, got %v", wantGet, paths)
	}
}

func TestSchedulesInspectWithFires(t *testing.T) {
	var paths []string
	m := newMockScheduleServer(t, func(req recordedReq) (int, []byte) {
		paths = append(paths, req.method+" "+req.path)
		if strings.HasSuffix(req.path, "/fires") {
			resp, _ := json.Marshal(map[string]any{"fires": []any{}})
			return http.StatusOK, resp
		}
		resp, _ := json.Marshal(map[string]any{"schedule": map[string]any{"spec": map[string]any{"name": "x"}}})
		return http.StatusOK, resp
	})
	ts := m.serve()
	defer ts.Close()

	_, _ = mustRunSchedules(t, ts.URL, "inspect", "--name", "x", "--fires")
	want := []string{"GET /v1/schedules/x", "GET /v1/schedules/x/fires"}
	if len(paths) != 2 {
		t.Fatalf("inspect --fires should issue 2 requests, got %d: %v", len(paths), paths)
	}
	for i, w := range want {
		if paths[i] != w {
			t.Errorf("request %d: want %q, got %q", i, w, paths[i])
		}
	}
}

// --- pause / resume / delete ------------------------------------------------

func TestSchedulesPause(t *testing.T) {
	var hit string
	m := newMockScheduleServer(t, func(req recordedReq) (int, []byte) {
		hit = req.method + " " + req.path
		return http.StatusNoContent, nil
	})
	ts := m.serve()
	defer ts.Close()

	stdout, _ := mustRunSchedules(t, ts.URL, "pause", "--name", "x")
	if hit != "POST /v1/schedules/x/pause" {
		t.Errorf("pause should POST .../pause, got %q", hit)
	}
	if !strings.Contains(stdout, "paused") {
		t.Errorf("pause stdout should mention paused: %s", stdout)
	}
}

func TestSchedulesResume(t *testing.T) {
	var hit string
	m := newMockScheduleServer(t, func(req recordedReq) (int, []byte) {
		hit = req.method + " " + req.path
		return http.StatusNoContent, nil
	})
	ts := m.serve()
	defer ts.Close()

	stdout, _ := mustRunSchedules(t, ts.URL, "resume", "--name", "x")
	if hit != "POST /v1/schedules/x/resume" {
		t.Errorf("resume should POST .../resume, got %q", hit)
	}
	if !strings.Contains(stdout, "resumed") {
		t.Errorf("resume stdout should mention resumed: %s", stdout)
	}
}

func TestSchedulesDelete(t *testing.T) {
	var hit string
	m := newMockScheduleServer(t, func(req recordedReq) (int, []byte) {
		hit = req.method + " " + req.path
		return http.StatusNoContent, nil
	})
	ts := m.serve()
	defer ts.Close()

	stdout, _ := mustRunSchedules(t, ts.URL, "delete", "--name", "x")
	if hit != "DELETE /v1/schedules/x" {
		t.Errorf("delete should DELETE .../x, got %q", hit)
	}
	if !strings.Contains(stdout, "deleted") {
		t.Errorf("delete stdout should mention deleted: %s", stdout)
	}
}

// --- fire -------------------------------------------------------------------

func TestSchedulesFire(t *testing.T) {
	var hit string
	m := newMockScheduleServer(t, func(req recordedReq) (int, []byte) {
		hit = req.method + " " + req.path
		resp, _ := json.Marshal(map[string]any{
			"fire_id":    "fire-123",
			"session_id": "sess-456",
		})
		return http.StatusCreated, resp
	})
	ts := m.serve()
	defer ts.Close()

	stdout, _ := mustRunSchedules(t, ts.URL, "fire", "--name", "x")
	if hit != "POST /v1/schedules/x/fire" {
		t.Errorf("fire should POST .../fire, got %q", hit)
	}
	if !strings.Contains(stdout, "fire-123") {
		t.Errorf("fire stdout should contain fire_id: %s", stdout)
	}
	if !strings.Contains(stdout, "sess-456") {
		t.Errorf("fire stdout should contain session_id: %s", stdout)
	}
}

// --- output json ------------------------------------------------------------

func TestSchedulesListOutputJSON(t *testing.T) {
	m := newMockScheduleServer(t, func(_ recordedReq) (int, []byte) {
		resp, _ := json.Marshal(map[string]any{"schedules": []any{}})
		return http.StatusOK, resp
	})
	ts := m.serve()
	defer ts.Close()

	stdout, _ := mustRunSchedules(t, ts.URL, "list", "--output", "json")
	// The raw JSON output must be valid JSON.
	var v map[string]any
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("list --output json did not produce valid JSON: %v\n%s", err, stdout)
	}
	if _, ok := v["schedules"]; !ok {
		t.Errorf("list --output json should carry schedules key: %s", stdout)
	}
}

func TestSchedulesInspectOutputJSON(t *testing.T) {
	m := newMockScheduleServer(t, func(_ recordedReq) (int, []byte) {
		resp, _ := json.Marshal(map[string]any{"schedule": map[string]any{"spec": map[string]any{"name": "x"}}})
		return http.StatusOK, resp
	})
	ts := m.serve()
	defer ts.Close()

	stdout, _ := mustRunSchedules(t, ts.URL, "inspect", "--name", "x", "--output", "json")
	var v map[string]any
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("inspect --output json did not produce valid JSON: %v\n%s", err, stdout)
	}
}

// --- missing required flags -------------------------------------------------

func TestSchedulesInspectMissingName(t *testing.T) {
	m := newMockScheduleServer(t, func(_ recordedReq) (int, []byte) {
		t.Fatalf("should not dial server on a missing required flag")
		return 500, nil
	})
	ts := m.serve()
	defer ts.Close()
	mustRunSchedulesErr(t, ts.URL, "--name", "inspect")
}

func TestSchedulesFireMissingName(t *testing.T) {
	m := newMockScheduleServer(t, func(_ recordedReq) (int, []byte) {
		t.Fatalf("should not dial server on a missing required flag")
		return 500, nil
	})
	ts := m.serve()
	defer ts.Close()
	mustRunSchedulesErr(t, ts.URL, "--name", "fire")
}

func TestSchedulesPauseMissingName(t *testing.T) {
	m := newMockScheduleServer(t, func(_ recordedReq) (int, []byte) {
		t.Fatalf("should not dial server on a missing required flag")
		return 500, nil
	})
	ts := m.serve()
	defer ts.Close()
	mustRunSchedulesErr(t, ts.URL, "--name", "pause")
}

func TestSchedulesResumeMissingName(t *testing.T) {
	m := newMockScheduleServer(t, func(_ recordedReq) (int, []byte) {
		t.Fatalf("should not dial server on a missing required flag")
		return 500, nil
	})
	ts := m.serve()
	defer ts.Close()
	mustRunSchedulesErr(t, ts.URL, "--name", "resume")
}

func TestSchedulesDeleteMissingName(t *testing.T) {
	m := newMockScheduleServer(t, func(_ recordedReq) (int, []byte) {
		t.Fatalf("should not dial server on a missing required flag")
		return 500, nil
	})
	ts := m.serve()
	defer ts.Close()
	mustRunSchedulesErr(t, ts.URL, "--name", "delete")
}

func TestSchedulesFireOutputJSON(t *testing.T) {
	m := newMockScheduleServer(t, func(_ recordedReq) (int, []byte) {
		resp, _ := json.Marshal(map[string]any{
			"fire_id":    "fire-123",
			"session_id": "sess-456",
		})
		return http.StatusCreated, resp
	})
	ts := m.serve()
	defer ts.Close()

	stdout, _ := mustRunSchedules(t, ts.URL, "fire", "--name", "x", "--output", "json")
	var v map[string]any
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("fire --output json did not produce valid JSON: %v\n%s", err, stdout)
	}
	if v["fire_id"] != "fire-123" {
		t.Errorf("fire_id: want fire-123, got %v", v["fire_id"])
	}
	if v["session_id"] != "sess-456" {
		t.Errorf("session_id: want sess-456, got %v", v["session_id"])
	}
}

// --- server error propagation -----------------------------------------------

func TestSchedulesServerError(t *testing.T) {
	m := newMockScheduleServer(t, func(_ recordedReq) (int, []byte) {
		resp, _ := json.Marshal(map[string]string{"error": "schedule not found"})
		return http.StatusNotFound, resp
	})
	ts := m.serve()
	defer ts.Close()

	_, _, err := runSchedulesAt(t, ts.URL, "inspect", "--name", "missing")
	if err == nil {
		t.Fatal("expected an error for a 404 response, got nil")
	}
	if !strings.Contains(err.Error(), "schedule not found") {
		t.Errorf("error should carry the server's message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention the status code, got: %v", err)
	}
}
