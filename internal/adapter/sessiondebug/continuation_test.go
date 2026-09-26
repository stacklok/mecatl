package sessiondebug

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func continuationResult(t *testing.T, result session.ToolResult) map[string]any {
	t.Helper()
	if result.IsError {
		t.Fatalf("InspectSession returned error: %s", result.Content)
	}
	start, end := strings.IndexByte(result.Content, '{'), strings.LastIndexByte(result.Content, '}')
	if start < 0 || end < start {
		t.Fatalf("result has no JSON object: %s", result.Content)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(result.Content[start:end+1]), &out); err != nil {
		t.Fatalf("decode result: %v\n%s", err, result.Content)
	}
	return out
}

func appendEvents(t *testing.T, log *memstore.EventLog, id session.SessionID, count int, event func(int) session.Event) {
	t.Helper()
	for i := 0; i < count; i++ {
		if _, err := log.AppendEvent(context.Background(), id, event(i)); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}
}

func nextCursor(t *testing.T, out map[string]any) string {
	t.Helper()
	cursor, _ := out["next_cursor"].(string)
	if cursor == "" {
		t.Fatalf("missing next_cursor: %#v", out)
	}
	return cursor
}

func TestDebuggerScanContinuation_Scenario1_RecordBoundaries(t *testing.T) {
	for _, count := range []int{0, 10_000, 10_001} {
		t.Run(string(rune(count)), func(t *testing.T) {
			store, target := seededTarget(t, nil)
			log := memstore.NewEventLog()
			appendEvents(t, log, target.ID, count, func(int) session.Event { return session.Event{Type: session.EvTurnStart} })
			out := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"performance"}`))
			window := out["event_window"].(map[string]any)
			if got := int(window["records_scanned"].(float64)); got != min(count, 10_000) {
				t.Fatalf("records_scanned=%d", got)
			}
			if count <= 10_000 && window["stop_reason"] != "end_of_log" || count > 10_000 && window["stop_reason"] != "scan_limit" {
				t.Fatalf("boundary metadata = %#v", window)
			}
			if (count > 10_000) != (out["next_cursor"] != nil) {
				t.Fatalf("continuation boundary = %#v", out)
			}
		})
	}

	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	appendEvents(t, log, target.ID, 10_000, func(int) session.Event { return session.Event{Type: session.EvTurnStart} })
	_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvNetworkAttempt, NetworkAttempt: validAttempt(target.ID, 1)})
	first := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"network"}`))
	second := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"network","cursor":"`+nextCursor(t, first)+`"}`))
	if second["matched_attempts"].(float64) != 1 {
		t.Fatalf("event after boundary was skipped: %#v", second)
	}
}

func TestDebuggerScanContinuation_Scenario1_RowPages(t *testing.T) {
	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	appendEvents(t, log, target.ID, 55, func(i int) session.Event {
		return session.Event{Type: session.EvTurnEnd, Turn: i, TurnEnd: &session.TurnEndPayload{DurationMs: 1}}
	})
	first := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"performance","limit":7}`))
	cursor := nextCursor(t, first)
	second := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"performance","limit":3,"cursor":"`+cursor+`"}`))
	if second["offset"].(float64) != 7 || second["row_page"].(map[string]any)["returned"].(float64) != 3 {
		t.Fatalf("row continuation did not advance: %#v", second)
	}
}

func TestDebuggerScanContinuation_Scenario1_Compatibility(t *testing.T) {
	store, target := seededTarget(t, []session.Message{session.NewUserMessage("one"), session.NewAssistantMessage("two", "", nil)})
	activity := execute(t, New(target.ID, store, generatedLog{count: 10_020}), `{"view":"activity","offset":10001,"limit":1}`)
	if activity.IsError {
		t.Fatalf("activity offset regressed: %s", activity.Content)
	}
	for _, args := range []string{
		`{"view":"network","cursor":"x","offset":1}`,
		`{"view":"history","cursor":"x","history_handle":"y"}`,
		`{"view":"transcript","cursor":"x"}`,
	} {
		if got := execute(t, New(target.ID, store, memstore.NewEventLog()), args); !got.IsError {
			t.Fatalf("invalid combination accepted: %s => %s", args, got.Content)
		}
	}
}

func TestDebuggerScanContinuation_Scenario2_WindowAggregates(t *testing.T) {
	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	appendEvents(t, log, target.ID, 10_001, func(int) session.Event {
		return session.Event{Type: session.EvTurnEnd, TurnEnd: &session.TurnEndPayload{Usage: session.Usage{InputTokens: 1}}}
	})
	first := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"performance","limit":1}`))
	if first["aggregate_scope"] != "event_window" || first["totals"].(map[string]any)["usage"].(map[string]any)["InputTokens"].(float64) != 10_000 {
		t.Fatalf("first aggregate = %#v", first)
	}
	cursor := nextCursor(t, first)
	initialID := first["event_window"].(map[string]any)["id"]
	for {
		page := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"performance","limit":50,"cursor":"`+cursor+`"}`))
		cursor, _ = page["next_cursor"].(string)
		pageID := page["event_window"].(map[string]any)["id"]
		if pageID != initialID {
			if page["totals"].(map[string]any)["usage"].(map[string]any)["InputTokens"].(float64) != 1 {
				t.Fatalf("adjacent-window aggregate = %#v", page)
			}
			break
		}
		if cursor == "" {
			t.Fatal("row pages hid the next raw window")
		}
	}
}

func TestDebuggerScanContinuation_Scenario2_RunScope(t *testing.T) {
	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	appendEvents(t, log, target.ID, 2, func(_ int) session.Event { return session.Event{Type: session.EvTurnStart, RunID: "same"} })
	out := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"status"}`))
	life := out["lifetime_event_log"].(map[string]any)
	if life["runs_scope"] != "distinct_observed_within_window" || life["runs_additive"] != false {
		t.Fatalf("run scope = %#v", life)
	}
}

func TestDebuggerScanContinuation_Scenario2_Completeness(t *testing.T) {
	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	_, _ = log.AppendGap(context.Background(), target.ID, "secret gap reason")
	_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnStart})
	out := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"network"}`))
	window := out["event_window"].(map[string]any)
	if window["gap_records"].(float64) != 1 || out["complete"] != false || strings.Contains(out["error"].(string), "secret") {
		t.Fatalf("gap completeness = %#v", out)
	}
}

func TestDebuggerScanContinuation_Scenario3_ArchiveSelection(t *testing.T) {
	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	archive := &session.CompactionArchivePayload{Replaced: []session.Message{session.NewUserMessage("archived")}}
	_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvCompactionArchive, CompactionArchive: archive})
	catalog := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"history"}`))
	sources := catalog["sources"].([]any)
	var handle string
	for _, item := range sources {
		src := item.(map[string]any)
		if src["source"] == "compaction_archive" {
			handle, _ = src["history_handle"].(string)
		}
	}
	selected := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"history","history_handle":"`+handle+`"}`))
	if selected["source"] != "compaction_archive" || !strings.Contains(selected["transcript"].(map[string]any)["messages"].([]any)[0].(map[string]any)["text"].(string), "archived") {
		t.Fatalf("archive selection = %#v", selected)
	}
}

func TestDebuggerScanContinuation_Scenario3_ReplayCoverage(t *testing.T) {
	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	appendEvents(t, log, target.ID, 10_001, func(int) session.Event { return session.Event{Type: session.EvTurnStart} })
	out := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"history"}`))
	found := false
	for _, item := range out["sources"].([]any) {
		src := item.(map[string]any)
		if src["source"] == "retained_event_history" && src["available"] == false && src["reason"] == "full_replay_window_limit" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing replay limit source: %#v", out)
	}
}

func TestDebuggerScanContinuation_Scenario4_TokenBinding(t *testing.T) {
	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	appendEvents(t, log, target.ID, 10_001, func(int) session.Event { return session.Event{Type: session.EvTurnStart} })
	out := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"network"}`))
	cursor := nextCursor(t, out)
	index := len(cursor) / 2
	replacement := byte('A')
	if cursor[index] == replacement {
		replacement = 'B'
	}
	cursor = cursor[:index] + string(replacement) + cursor[index+1:]
	got := execute(t, New(target.ID, store, log), `{"view":"network","cursor":"`+cursor+`"}`)
	if !got.IsError || !strings.Contains(got.Content, "continuation is invalid or stale") || strings.Contains(got.Content, cursor) {
		t.Fatalf("tampered token result = %s", got.Content)
	}
}

func TestDebuggerScanContinuation_Scenario4_ConcurrentReads(t *testing.T) {
	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	appendEvents(t, log, target.ID, 3, func(i int) session.Event {
		return session.Event{Type: session.EvTurnEnd, Turn: i, TurnEnd: &session.TurnEndPayload{}}
	})
	first := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"performance","limit":1}`))
	_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnEnd, Turn: 99, TurnEnd: &session.TurnEndPayload{}})
	second := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"performance","limit":50,"cursor":"`+nextCursor(t, first)+`"}`))
	if second["event_window"].(map[string]any)["id"] != first["event_window"].(map[string]any)["id"] || second["row_page"].(map[string]any)["returned"].(float64) != 2 {
		t.Fatalf("append expanded sealed row interval: first=%#v second=%#v", first, second)
	}
}

func TestDebuggerScanContinuation_Scenario4_BackendCapability(t *testing.T) {
	if !errors.Is(port.ErrCursorUnsupported, port.ErrCursorUnsupported) {
		t.Fatal("cursor unsupported sentinel lost identity")
	}
	store, target := seededTarget(t, nil)
	legacy := generatedLog{count: 1}
	out := continuationResult(t, execute(t, New(target.ID, store, legacy), `{"view":"network"}`))
	window := out["event_window"].(map[string]any)
	if window["continuation_supported"] != false || out["continuation_error"] != "configured event log does not support resumable reads" || out["next_cursor"] != nil {
		t.Fatalf("legacy capability = %#v", out)
	}
}

func validAttempt(id session.SessionID, serial int64) *session.NetworkAttemptPayload {
	return &session.NetworkAttemptPayload{SessionID: id, RunSerial: serial, Attempt: 1, MaxAttempts: 3, RetryDisposition: "retryable", StreamProgress: "precommit", Decision: "retry", FailureClass: "connect"}
}
