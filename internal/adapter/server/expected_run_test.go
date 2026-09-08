package server_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestSDKServerEnablers_Scenario5_MatchingExpectedRunIDSucceeds is AC5.1.
//
// A control carrying the CURRENT run's id behaves exactly as an unqualified one.
// The guard must be invisible when the caller is right.
func TestSDKServerEnablers_Scenario5_MatchingExpectedRunIDSucceeds(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	svc := newService(t, llm, allowRules())

	sess, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(t.Context(), sess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	current := run.RunID()
	if current == "" {
		t.Fatal("the started run has no id")
	}

	// Cancelling the run it names must succeed.
	if err := svc.Cancel(t.Context(), sess.ID, current); err != nil {
		t.Fatalf("Cancel with the matching run id = %v, want nil", err)
	}
	drainRun(t, run)
}

// TestADR_0249_StaleControlCannotTouchNewerRun is AC5.2, and the reason this
// whole scenario exists.
//
// Controls are addressed at a SESSION, so before run ids a control still in
// flight when a run ended would land on whatever started next — cancelling work
// the user never asked to stop. `seq` cannot tell the two runs apart (it
// restarts each run), so there was no way to express "this one".
func TestADR_0249_StaleControlCannotTouchNewerRun(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("first"), mockllm.TextTurn("second"))
	svc := newService(t, llm, allowRules())

	sess, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	first, err := svc.StartRun(t.Context(), sess.ID, "one")
	if err != nil {
		t.Fatalf("StartRun(first): %v", err)
	}
	staleID := first.RunID()
	drainRun(t, first)
	svc.FinishRun(sess.ID, first)

	second, err := svc.StartRun(t.Context(), sess.ID, "two")
	if err != nil {
		t.Fatalf("StartRun(second): %v", err)
	}
	if second.RunID() == staleID {
		t.Fatal("the second run reused the first run's id; the test cannot detect a stale control")
	}

	// The stale cancel must be refused, typed, and must NOT stop the newer run.
	err = svc.Cancel(t.Context(), sess.ID, staleID)
	if err == nil {
		t.Fatal("a cancel naming the FINISHED run succeeded; it would have stopped work the caller never meant to touch")
	}
	if !errors.Is(err, server.ErrStaleRunControl) {
		t.Errorf("stale cancel error = %v, want ErrStaleRunControl so a client can tell it from a bad request", err)
	}

	// The newer run is untouched — it still completes on its own terms.
	evs := collectRun(t, second)
	if !hasTerminalStop(evs, session.StopEndTurn) {
		t.Errorf("the newer run did not complete normally after the stale cancel; stops seen: %v", stopsOf(evs))
	}
	svc.FinishRun(sess.ID, second)
}

// TestSDKServerEnablers_Scenario5_OmittedExpectedRunIDUnchanged is AC5.3.
//
// The whole feature is opt-in. An omitted expected_run_id must behave exactly as
// before it existed, or every deployed mecatui breaks.
func TestSDKServerEnablers_Scenario5_OmittedExpectedRunIDUnchanged(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	svc := newService(t, llm, allowRules())

	sess, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(t.Context(), sess.ID, "hi")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := svc.Cancel(t.Context(), sess.ID, ""); err != nil {
		t.Fatalf("Cancel with NO expected run id = %v, want nil (the legacy path)", err)
	}
	drainRun(t, run)
	svc.FinishRun(sess.ID, run)
}

// TestADR_0249_StrictSteerNeverPromotes is AC5.4.
//
// An unqualified steer that loses the terminal race is PROMOTED into a fresh
// follow-up run — the operator meant "say this to the agent", and a new run says
// it. A steer naming a specific run did NOT ask to start a different one, so it
// is refused instead. This is what lets an SDK offer a steer that never
// surprises a caller with an extra run.
func TestADR_0249_StrictSteerNeverPromotes(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("first"), mockllm.TextTurn("promoted"))
	svc := newService(t, llm, allowRules())

	sess, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(t.Context(), sess.ID, "one")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	staleID := run.RunID()
	drainRun(t, run)
	svc.FinishRun(sess.ID, run)

	// Strict: names a run that has ended → refused, and NO follow-up run started.
	outcome, promoted, promotedRun, err := svc.Steer(t.Context(), sess.ID, "hello", nil, "m1", staleID)
	if err == nil {
		t.Fatal("a strict steer naming a finished run succeeded; it must be refused, not promoted")
	}
	if !errors.Is(err, server.ErrStaleRunControl) {
		t.Errorf("strict steer error = %v, want ErrStaleRunControl", err)
	}
	if promoted || promotedRun != nil {
		t.Errorf("a strict steer PROMOTED a follow-up run (promoted=%v run=%v); naming a run is not asking to start another",
			promoted, promotedRun != nil)
	}
	if outcome != agent.SteerTooLate {
		t.Errorf("outcome = %v, want %v", outcome, agent.SteerTooLate)
	}
}

// TestSDKServerEnablers_Scenario5_PromotionRetainedOnRawAPI is AC5.5.
//
// The strictness is opt-in and must not change the unqualified path: an ordinary
// steer that loses the terminal race still promotes, exactly as ADR 0232 shipped
// it. Losing that would silently drop operator input.
func TestSDKServerEnablers_Scenario5_PromotionRetainedOnRawAPI(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("first"), mockllm.TextTurn("promoted"))
	svc := newService(t, llm, allowRules())

	sess, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(t.Context(), sess.ID, "one")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	drainRun(t, run)
	svc.FinishRun(sess.ID, run)

	outcome, promoted, promotedRun, err := svc.Steer(t.Context(), sess.ID, "hello", nil, "m1", "")
	if err != nil {
		t.Fatalf("unqualified steer after the run ended = %v, want a promotion", err)
	}
	if !promoted || promotedRun == nil {
		t.Fatalf("unqualified steer did not promote (promoted=%v run=%v); operator input would be dropped",
			promoted, promotedRun != nil)
	}
	if outcome != agent.SteerTooLate {
		t.Errorf("outcome = %v, want %v", outcome, agent.SteerTooLate)
	}
	if promotedRun.RunID() == "" {
		t.Error("the promoted follow-up run carries no run id")
	}
	drainRun(t, promotedRun)
	svc.FinishRun(sess.ID, promotedRun)
}

// TestSDKServerEnablers_Scenario5_StaleControlOverHTTPIsTyped checks the wire
// contract: the HTTP control endpoints are where a typed stale-control error can
// actually reach a client, so it must arrive as a 409 problem with the stable
// code rather than an opaque failure.
func TestSDKServerEnablers_Scenario5_StaleControlOverHTTPIsTyped(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("first"), mockllm.TextTurn("second"))
	svc := newService(t, llm, allowRules())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	sess, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	first, err := svc.StartRun(t.Context(), sess.ID, "one")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	staleID := first.RunID()
	drainRun(t, first)
	svc.FinishRun(sess.ID, first)

	second, err := svc.StartRun(t.Context(), sess.ID, "two")
	if err != nil {
		t.Fatalf("StartRun(second): %v", err)
	}
	defer func() { drainRun(t, second); svc.FinishRun(sess.ID, second) }()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		srv.URL+"/v1/sessions/"+string(sess.ID)+"/cancel",
		strings.NewReader(`{"expected_run_id":"`+staleID+`"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST cancel: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409 Conflict (a lost race, not a bad request)", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	body := decodeProblem(t, resp)
	if body.Code != "stale_run_control" {
		t.Errorf("problem code = %q, want %q", body.Code, "stale_run_control")
	}
}

// collectRun drains a run's events and RETURNS them. It is the collecting
// sibling of drainRun (multimodal_test.go), which discards.
func collectRun(t *testing.T, run *agent.Run) []session.Event {
	t.Helper()
	var out []session.Event
	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev, ok := <-run.Events():
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatal("timed out draining run events")
			return out
		}
	}
}

func hasTerminalStop(evs []session.Event, want session.StopReason) bool {
	for _, ev := range evs {
		if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == want {
			return true
		}
	}
	return false
}

func stopsOf(evs []session.Event) []session.StopReason {
	var out []session.StopReason
	for _, ev := range evs {
		if ev.Type == session.EvResult && ev.Result != nil {
			out = append(out, ev.Result.Stop)
		}
	}
	return out
}

// decodeProblem reads an RFC 9457 body (the shape PR 2 established).
func decodeProblem(t *testing.T, resp *http.Response) problemBody {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var p problemBody
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode problem body (%s): %v", raw, err)
	}
	return p
}
