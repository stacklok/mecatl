package sessiondebug

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	enginetool "github.com/stacklok/mecatl/engine/tool"
)

type readAuditLog struct {
	base    port.CursorEventLog
	calls   []port.ReadOptions
	yields  []int
	after   []port.Cursor
	readErr error
}

func (l *readAuditLog) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	return l.base.Append(ctx, id, ev)
}
func (l *readAuditLog) Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	return l.base.Read(ctx, id)
}
func (l *readAuditLog) AppendEvent(ctx context.Context, id session.SessionID, ev session.Event) (port.Cursor, error) {
	return l.base.AppendEvent(ctx, id, ev)
}
func (l *readAuditLog) AppendGap(ctx context.Context, id session.SessionID, reason string) (port.Cursor, error) {
	return l.base.AppendGap(ctx, id, reason)
}
func (l *readAuditLog) ReadAfter(ctx context.Context, id session.SessionID, after port.Cursor, opts port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	call := len(l.calls)
	l.calls = append(l.calls, opts)
	l.after = append(l.after, after)
	l.yields = append(l.yields, 0)
	if l.readErr != nil {
		return func(yield func(port.LogRecord, error) bool) { yield(port.LogRecord{}, l.readErr) }
	}
	seq := l.base.ReadAfter(ctx, id, after, opts)
	return func(yield func(port.LogRecord, error) bool) {
		for rec, err := range seq {
			l.yields[call]++
			if !yield(rec, err) {
				return
			}
		}
	}
}

func TestDebuggerScanContinuation_QARecordReadAccountingAndWindowIdentity(t *testing.T) {
	for _, count := range []int{0, 10_000, 10_001} {
		t.Run(string(rune('a'+count%7)), func(t *testing.T) {
			store, target := seededTarget(t, nil)
			base := memstore.NewEventLog()
			appendEvents(t, base, target.ID, count, func(i int) session.Event {
				return session.Event{Type: session.EvTurnStart, Seq: int64(i)}
			})
			audit := &readAuditLog{base: base}
			out := continuationResult(t, execute(t, New(target.ID, store, audit), `{"view":"network"}`))
			wantYields := []int{count}
			if count > 10_000 {
				wantYields[0] = 10_001
			}
			if count > 0 {
				wantYields = append(wantYields, 1) // publication reread, not coverage
			}
			if !reflect.DeepEqual(audit.yields, wantYields) {
				t.Fatalf("ReadAfter yields=%v want %v; metadata=%#v", audit.yields, wantYields, out["event_window"])
			}
			if audit.calls[0].Limit != 10_001 || audit.calls[0].Follow {
				t.Fatalf("scan options=%+v", audit.calls[0])
			}
			window := out["event_window"].(map[string]any)
			if window["records_scanned"] != float64(min(count, 10_000)) {
				t.Fatalf("probe counted as coverage: %#v", window)
			}
		})
	}

	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnStart})
	network := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"network"}`))
	performance := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"performance"}`))
	if network["event_window"].(map[string]any)["id"] == performance["event_window"].(map[string]any)["id"] {
		t.Fatal("window identity is not bound to the selected view")
	}
}

func traverseRows(t *testing.T, inspect enginetool.Tool, view, rowsKey string, firstLimit int) ([]any, []float64, []string) {
	t.Helper()
	args := `{"view":"` + view + `","limit":` + strconv.Itoa(firstLimit) + `}`
	out := continuationResult(t, execute(t, inspect, args))
	var rows []any
	var offsets []float64
	var ids []string
	for page := 0; ; page++ {
		rows = append(rows, out[rowsKey].([]any)...)
		offsets = append(offsets, out["offset"].(float64))
		ids = append(ids, out["event_window"].(map[string]any)["id"].(string))
		cursor, _ := out["next_cursor"].(string)
		if cursor == "" {
			break
		}
		limit := 3
		if page%2 == 1 {
			limit = 1
		}
		out = continuationResult(t, execute(t, inspect, `{"view":"`+view+`","limit":`+strconv.Itoa(limit)+`,"cursor":"`+cursor+`"}`))
	}
	return rows, offsets, ids
}

func TestDebuggerScanContinuation_QAScanIncompleteRowViews(t *testing.T) {
	cases := []struct {
		name, view, key string
		event           func(int, session.SessionID) session.Event
		identity        func(any) any
	}{
		{"performance", "performance", "turns", func(i int, _ session.SessionID) session.Event {
			return session.Event{Type: session.EvTurnEnd, Turn: i, TurnEnd: &session.TurnEndPayload{DurationMs: int64(i + 1)}}
		}, func(v any) any { return v.(map[string]any)["turn"] }},
		{"network", "network", "attempts", func(i int, id session.SessionID) session.Event {
			return session.Event{Type: session.EvNetworkAttempt, NetworkAttempt: validAttempt(id, int64(i+1))}
		}, func(v any) any { return v.(map[string]any)["run_serial"] }},
		{"manifest", "manifest", "rows", func(i int, _ session.SessionID) session.Event {
			return session.Event{Type: session.EvRequestManifest, RequestManifest: &session.RequestManifestPayload{Provider: "provider-" + string(rune('a'+i))}}
		}, func(v any) any { return v.(map[string]any)["provider"] }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, target := seededTarget(t, nil)
			log := memstore.NewEventLog()
			for i := 0; i < 6; i++ {
				_, _ = log.AppendEvent(context.Background(), target.ID, tc.event(i, target.ID))
			}
			appendEvents(t, log, target.ID, 9_995, func(int) session.Event { return session.Event{Type: session.EvTurnStart} })
			rows, offsets, ids := traverseRows(t, New(target.ID, store, log), tc.view, tc.key, 2)
			if len(rows) != 6 || !reflect.DeepEqual(offsets[:3], []float64{0, 2, 5}) {
				t.Fatalf("rows=%d offsets=%v ids=%v", len(rows), offsets, ids)
			}
			seen := map[any]bool{}
			for _, row := range rows {
				id := tc.identity(row)
				if seen[id] {
					t.Fatalf("duplicate row identity %v", id)
				}
				seen[id] = true
			}
			if ids[0] == ids[len(ids)-1] {
				t.Fatal("row exhaustion did not advance into the later raw window")
			}
		})
	}
}

func TestDebuggerScanContinuation_QAScanIncompleteDelegationAndHistoryRows(t *testing.T) {
	t.Run("multi-row-team-event", func(t *testing.T) {
		store, root := seededTarget(t, nil)
		call := session.NewToolCall("team-call", "Team", []byte(`{}`))
		if err := root.SeedHistory([]session.Message{
			session.NewAssistantMessage("", "", []session.ToolCall{call}),
			session.NewToolMessage(session.NewToolResult(call.ID, "done")),
		}); err != nil {
			t.Fatal(err)
		}
		members := make([]*session.Session, 2)
		roster := make([]session.TeamMemberSpec, 2)
		for i, name := range []string{"alpha", "beta"} {
			member, err := session.NewTeamMember(session.SessionID("member-"+name), session.ModeDefault, root.EnvironmentRef, root.Limits, root.CreatedAt, "team-qa", name, root.ID, root.Incarnation())
			if err != nil {
				t.Fatal(err)
			}
			member.Relationship.CallID = call.ID
			members[i] = member
			roster[i] = session.TeamMemberSpec{Name: name, MemberSessionID: member.ID, MemberIncarnation: member.Incarnation()}
		}
		if err := store.Save(context.Background(), root); err != nil {
			t.Fatal(err)
		}
		for _, member := range members {
			if err := store.Save(context.Background(), member); err != nil {
				t.Fatal(err)
			}
		}
		log := memstore.NewEventLog()
		_, _ = log.AppendEvent(context.Background(), root.ID, session.Event{Type: session.EvTeamStart, Team: &session.TeamPayload{ParentCallID: string(call.ID), TeamID: "team-qa", Roster: roster}})
		appendEvents(t, log, root.ID, 9_999, func(int) session.Event { return session.Event{Type: session.EvTurnStart} })
		_, _ = log.AppendEvent(context.Background(), root.ID, session.Event{Type: session.EvTurnStart})
		first := continuationResult(t, execute(t, New(root.ID, store, log), `{"view":"delegation","limit":1}`))
		mutated, err := store.Load(context.Background(), root.ID)
		if err != nil {
			t.Fatal(err)
		}
		mutated.Conversation.Messages = append(mutated.Conversation.Messages, session.NewUserMessage("changed delegation projection basis"))
		if err := store.Save(context.Background(), mutated); err != nil {
			t.Fatal(err)
		}
		stale := execute(t, New(root.ID, store, log), `{"view":"delegation","limit":1,"cursor":"`+nextCursor(t, first)+`"}`)
		if !stale.IsError || stale.Content != continuationInvalid || strings.Contains(stale.Content, `"rows"`) {
			t.Fatalf("delegation row token survived projection-basis change: %s", stale.Content)
		}
		if err := store.Save(context.Background(), root); err != nil {
			t.Fatal(err)
		}
		second := continuationResult(t, execute(t, New(root.ID, store, log), `{"view":"delegation","limit":1,"cursor":"`+nextCursor(t, first)+`"}`))
		firstMember := first["rows"].([]any)[0].(map[string]any)["member"]
		secondMember := second["rows"].([]any)[0].(map[string]any)["member"]
		if firstMember == secondMember || first["event_window"].(map[string]any)["id"] != second["event_window"].(map[string]any)["id"] || second["next_cursor"] == nil {
			t.Fatalf("multi-row event skipped/duplicated or hid next window: first=%#v second=%#v", first, second)
		}
	})

	t.Run("history-catalog", func(t *testing.T) {
		store, target := seededTarget(t, nil)
		log := memstore.NewEventLog()
		for _, marker := range []string{"archive-a", "archive-b"} {
			_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvCompactionArchive, CompactionArchive: &session.CompactionArchivePayload{Replaced: []session.Message{session.NewUserMessage(marker)}}})
		}
		appendEvents(t, log, target.ID, 9_998, func(int) session.Event { return session.Event{Type: session.EvTurnStart} })
		_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnStart})
		first := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"history","limit":1}`))
		mutated, err := store.Load(context.Background(), target.ID)
		if err != nil {
			t.Fatal(err)
		}
		mutated.Conversation.Messages = append(mutated.Conversation.Messages, session.NewUserMessage("changed history projection basis"))
		if err := store.Save(context.Background(), mutated); err != nil {
			t.Fatal(err)
		}
		stale := execute(t, New(target.ID, store, log), `{"view":"history","limit":1,"cursor":"`+nextCursor(t, first)+`"}`)
		if !stale.IsError || stale.Content != continuationInvalid || strings.Contains(stale.Content, `"sources"`) {
			t.Fatalf("history row token survived projection-basis change: %s", stale.Content)
		}
		if err := store.Save(context.Background(), target); err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		out := first
		for i := 0; i < 4; i++ {
			for _, raw := range out["sources"].([]any) {
				row := raw.(map[string]any)
				if handle, _ := row["history_handle"].(string); handle != "" {
					if seen[handle] {
						t.Fatalf("duplicate history row: %#v", row)
					}
					seen[handle] = true
				}
			}
			cursor, _ := out["next_cursor"].(string)
			if cursor == "" {
				break
			}
			out = continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"history","limit":2,"cursor":"`+cursor+`"}`))
		}
		if len(seen) < 3 || first["event_window"].(map[string]any)["stop_reason"] != "scan_limit" {
			t.Fatalf("history catalog did not traverse snapshot+archives under incomplete scan: seen=%d first=%#v", len(seen), first)
		}
	})
}

func TestDebuggerScanContinuation_QAAggregateReferenceAndGapPrivacy(t *testing.T) {
	events := []session.Event{
		{Type: session.EvTurnEnd, RunID: "modern", TurnEnd: &session.TurnEndPayload{Usage: session.Usage{InputTokens: 2, OutputTokens: 3}, DurationMs: 7}},
		{Type: session.EvModelRetry, RunID: "modern"},
		{Type: session.EvToolCall, RunID: "modern"},
		{Type: session.EvToolResult, RunID: "modern", ToolResult: ptr(session.NewToolError("c", "x"))},
		{Type: session.EvResult, RunID: "modern", Result: &session.ResultPayload{Stop: session.StopEndTurn}},
		{Type: session.EvTurnStart}, {Type: session.EvResult},
	}
	scan := eventScan{Events: events, EventCount: len(events), Records: len(events), StopReason: "end_of_log", Complete: true}
	life := lifetimeFromScan(scan)
	perf := performanceFromScan(scan, 0, 1).Value
	if life["turns"] != float64(1) || life["tool_calls"] != float64(1) || life["tool_results"] != float64(1) || life["tool_failures"] != float64(1) || life["runs"] != float64(2) {
		t.Fatalf("lifetime reduction=%#v", life)
	}
	totals := perf["totals"].(map[string]any)
	if totals["duration_ms"] != float64(7) || totals["retries"] != float64(1) || totals["tool_failures"] != float64(1) || totals["stops"] != float64(2) {
		t.Fatalf("performance reduction=%#v", totals)
	}
	if life["runs_additive"] != false || life["runs_scope"] != "distinct_observed_within_window" {
		t.Fatalf("run count advertised as additive: %#v", life)
	}

	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	for i := 0; i < 9_999; i++ {
		_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnStart})
	}
	_, _ = log.AppendGap(context.Background(), target.ID, "FOREIGN-SECRET-REASON")
	_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvNetworkAttempt, NetworkAttempt: validAttempt(target.ID, 42)})
	first := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"network"}`))
	if strings.Contains(strings.ToLower(mustString(first)), "foreign-secret") || first["complete"] != false {
		t.Fatalf("gap leaked or claimed completeness: %#v", first)
	}
	second := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"network","cursor":"`+nextCursor(t, first)+`"}`))
	if second["matched_attempts"] != float64(1) {
		t.Fatalf("gap boundary skipped next record: %#v", second)
	}
}

func TestDebuggerScanContinuation_QAAdjacentWindowsSumEveryAdditiveCounter(t *testing.T) {
	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	windowEvents := func(multiplier int) []session.Event {
		usage := session.Usage{InputTokens: multiplier, OutputTokens: 2 * multiplier, CacheReadTokens: 3 * multiplier, CacheWriteTokens: 4 * multiplier, ReasoningTokens: 5 * multiplier}
		return []session.Event{
			{Type: session.EvTurnEnd, RunID: "spans-windows", Turn: multiplier, TurnEnd: &session.TurnEndPayload{Usage: usage, DurationMs: int64(6 * multiplier)}},
			{Type: session.EvModelRetry, RunID: "modern-" + strconv.Itoa(multiplier)},
			{Type: session.EvToolCall, RunID: "spans-windows"},
			{Type: session.EvToolResult, RunID: "spans-windows", ToolResult: ptr(session.NewToolError("failed", "x"))},
			{Type: session.EvResult, RunID: "spans-windows", Result: &session.ResultPayload{Stop: session.StopEndTurn}},
			{Type: session.EvTurnStart},
			{Type: session.EvResult},
			{Type: session.EvNetworkAttempt, NetworkAttempt: validAttempt(target.ID, int64(multiplier))},
		}
	}
	firstEvents, secondEvents := windowEvents(1), windowEvents(2)
	for _, ev := range firstEvents {
		_, _ = log.AppendEvent(context.Background(), target.ID, ev)
	}
	appendEvents(t, log, target.ID, maxPerformanceScan-len(firstEvents), func(i int) session.Event {
		return session.Event{Type: session.EvTurnStart, RunID: "interleaved-" + strconv.Itoa(i%3)}
	})
	for _, ev := range secondEvents {
		_, _ = log.AppendEvent(context.Background(), target.ID, ev)
	}

	inspect := New(target.ID, store, log)
	status1 := continuationResult(t, execute(t, inspect, `{"view":"status"}`))
	status2 := continuationResult(t, execute(t, inspect, `{"view":"status","cursor":"`+nextCursor(t, status1)+`"}`))
	life1 := status1["lifetime_event_log"].(map[string]any)
	life2 := status2["lifetime_event_log"].(map[string]any)
	for _, key := range []string{"turns", "tool_calls", "tool_results", "tool_failures"} {
		if life1[key].(float64)+life2[key].(float64) != 2 {
			t.Fatalf("adjacent lifetime %s did not sum to complete reference: first=%#v second=%#v", key, life1, life2)
		}
	}
	for key, want := range map[string]float64{"InputTokens": 3, "OutputTokens": 6, "CacheReadTokens": 9, "CacheWriteTokens": 12, "ReasoningTokens": 15} {
		got := life1["usage"].(map[string]any)[key].(float64) + life2["usage"].(map[string]any)[key].(float64)
		if got != want {
			t.Fatalf("adjacent lifetime usage %s=%v want %v", key, got, want)
		}
	}
	if !reflect.DeepEqual(status1["latest_run_counters"], status2["latest_run_counters"]) || !reflect.DeepEqual(status1["snapshot_cumulative_usage"], status2["snapshot_cumulative_usage"]) {
		t.Fatalf("snapshot counters changed across event windows: first=%#v second=%#v", status1, status2)
	}
	if life1["runs"] != float64(7) || life2["runs"] != float64(4) || life1["runs_additive"] != false || life2["runs_additive"] != false {
		t.Fatalf("modern/legacy/interleaved run IDs were treated as additive: first=%#v second=%#v", life1, life2)
	}

	performance1 := continuationResult(t, execute(t, inspect, `{"view":"performance","limit":50}`))
	repeated := continuationResult(t, execute(t, inspect, `{"view":"performance","limit":50,"cursor":"`+nextCursor(t, performance1)+`"}`))
	if performance1["event_window"].(map[string]any)["id"] == repeated["event_window"].(map[string]any)["id"] {
		t.Fatal("performance continuation did not advance to adjacent raw window")
	}
	totals1, totals2 := performance1["totals"].(map[string]any), repeated["totals"].(map[string]any)
	for key, want := range map[string]float64{"duration_ms": 18, "retries": 2, "tool_failures": 2, "stops": 4} {
		if totals1[key].(float64)+totals2[key].(float64) != want {
			t.Fatalf("adjacent performance %s did not sum to reference: first=%#v second=%#v", key, totals1, totals2)
		}
	}
	for key, want := range map[string]float64{"InputTokens": 3, "OutputTokens": 6, "CacheReadTokens": 9, "CacheWriteTokens": 12, "ReasoningTokens": 15} {
		got := totals1["usage"].(map[string]any)[key].(float64) + totals2["usage"].(map[string]any)[key].(float64)
		if got != want {
			t.Fatalf("adjacent performance usage %s=%v want %v", key, got, want)
		}
	}
	network1 := continuationResult(t, execute(t, inspect, `{"view":"network","limit":50}`))
	network2 := continuationResult(t, execute(t, inspect, `{"view":"network","limit":50,"cursor":"`+nextCursor(t, network1)+`"}`))
	if network1["matched_attempts"].(float64)+network2["matched_attempts"].(float64) != 2 || network1["counts_scope"] != "event_window" || network2["counts_scope"] != "event_window" {
		t.Fatalf("adjacent validated network counts did not sum to reference: first=%#v second=%#v", network1, network2)
	}

	rowLog := memstore.NewEventLog()
	for i := 0; i < 3; i++ {
		_, _ = rowLog.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnEnd, Turn: i, TurnEnd: &session.TurnEndPayload{Usage: session.Usage{InputTokens: i + 1}, DurationMs: int64(i + 1)}})
	}
	rowInspect := New(target.ID, store, rowLog)
	row1 := continuationResult(t, execute(t, rowInspect, `{"view":"performance","limit":1}`))
	row2 := continuationResult(t, execute(t, rowInspect, `{"view":"performance","limit":1,"cursor":"`+nextCursor(t, row1)+`"}`))
	if !reflect.DeepEqual(row1["totals"], row2["totals"]) || !reflect.DeepEqual(row1["event_window"], row2["event_window"]) {
		t.Fatalf("row retry changed aggregates or scan metadata: first=%#v second=%#v", row1, row2)
	}
}

func mustString(v any) string { b, _ := json.Marshal(v); return string(b) }

type arbitraryFailCursorLog struct{ port.EventLog }

func (arbitraryFailCursorLog) AppendEvent(context.Context, session.SessionID, session.Event) (port.Cursor, error) {
	return "", nil
}
func (arbitraryFailCursorLog) AppendGap(context.Context, session.SessionID, string) (port.Cursor, error) {
	return "", nil
}
func (arbitraryFailCursorLog) ReadAfter(context.Context, session.SessionID, port.Cursor, port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	return func(yield func(port.LogRecord, error) bool) {
		yield(port.LogRecord{}, errors.New("arbitrary backend failure"))
	}
}

func TestDebuggerScanContinuation_QAArbitraryCursorFailureNeverFallsBack(t *testing.T) {
	store, target := seededTarget(t, nil)
	legacy := generatedLog{count: 2}
	got := execute(t, New(target.ID, store, arbitraryFailCursorLog{EventLog: legacy}), `{"view":"performance"}`)
	if !got.IsError || !strings.Contains(got.Content, errLogReadFailed) || strings.Contains(got.Content, `"turns"`) {
		t.Fatalf("arbitrary cursor error fell back to prefix evidence: %s", got.Content)
	}
}

func findHistorySource(t *testing.T, out map[string]any, source string, available bool) map[string]any {
	t.Helper()
	for _, raw := range out["sources"].([]any) {
		row := raw.(map[string]any)
		if row["source"] == source && row["available"] == available {
			return row
		}
	}
	t.Fatalf("source %q available=%v missing: %#v", source, available, out)
	return nil
}

func TestDebuggerScanContinuation_QAArchivesAndExactBoundaryReplay(t *testing.T) {
	store, target := seededTarget(t, nil)
	base := memstore.NewEventLog()
	before := &session.CompactionArchivePayload{Replaced: []session.Message{session.NewUserMessage("BEFORE-ARCHIVE")}}
	after := &session.CompactionArchivePayload{Replaced: []session.Message{session.NewUserMessage("AFTER-ARCHIVE")}}
	_, _ = base.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvCompactionArchive, CompactionArchive: before})
	appendEvents(t, base, target.ID, 9_999, func(int) session.Event { return session.Event{Type: session.EvTurnStart} })
	_, _ = base.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvCompactionArchive, CompactionArchive: after})
	audit := &readAuditLog{base: base}
	inspect := New(target.ID, store, audit)
	first := continuationResult(t, execute(t, inspect, `{"view":"history"}`))
	beforeHandle := findHistorySource(t, first, sourceArchive, true)["history_handle"].(string)
	later := continuationResult(t, execute(t, inspect, `{"view":"history","cursor":"`+nextCursor(t, first)+`"}`))
	afterHandle := findHistorySource(t, later, sourceArchive, true)["history_handle"].(string)
	if beforeHandle == afterHandle {
		t.Fatal("archives at positions before/after 10000 shared a selector")
	}
	selectedBefore := continuationResult(t, execute(t, New(target.ID, store, audit), `{"view":"history","history_handle":"`+beforeHandle+`"}`))
	if !strings.Contains(mustString(selectedBefore), "BEFORE-ARCHIVE") || strings.Contains(mustString(selectedBefore), "AFTER-ARCHIVE") {
		t.Fatalf("first archive logical identity selection=%#v", selectedBefore)
	}
	readsBefore := len(audit.calls)
	selected := continuationResult(t, execute(t, New(target.ID, store, audit), `{"view":"history","history_handle":"`+afterHandle+`"}`))
	if !strings.Contains(mustString(selected), "AFTER-ARCHIVE") || strings.Contains(mustString(selected), "BEFORE-ARCHIVE") {
		t.Fatalf("later exact-point archive selection=%#v", selected)
	}
	if got := audit.calls[readsBefore:]; len(got) != 2 || got[0].Limit != 1 || got[1].Limit != 1 {
		t.Fatalf("later archive selection rescanned a prefix: calls=%+v after=%v yields=%v", got, audit.after[readsBefore:], audit.yields[readsBefore:])
	}

	boundaryStore, boundaryTarget := seededTarget(t, nil)
	boundaryLog := memstore.NewEventLog()
	_, _ = boundaryLog.AppendEvent(context.Background(), boundaryTarget.ID, session.Event{Type: session.EvSessionInit})
	appendEvents(t, boundaryLog, boundaryTarget.ID, 9_999, func(int) session.Event {
		return session.Event{Type: session.EvRequestManifest, RequestManifest: &session.RequestManifestPayload{Provider: "qa"}}
	})
	catalog := continuationResult(t, execute(t, New(boundaryTarget.ID, boundaryStore, boundaryLog), `{"view":"history"}`))
	replay := findHistorySource(t, catalog, sourceRetainedReplay, true)
	if catalog["event_window"].(map[string]any)["stop_reason"] != "end_of_log" || replay["history_handle"] == "" {
		t.Fatalf("exactly-10000 fold was not selectable: %#v", catalog)
	}
	selectedReplay := continuationResult(t, execute(t, New(boundaryTarget.ID, boundaryStore, boundaryLog), `{"view":"history","history_handle":"`+replay["history_handle"].(string)+`"}`))
	if selectedReplay["source"] != sourceRetainedReplay {
		t.Fatalf("exactly-10000 retained fold selection=%#v", selectedReplay)
	}
}

func TestDebuggerScanContinuation_QATokenBindingAndGenerationReset(t *testing.T) {
	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	for i := 0; i < 2; i++ {
		_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnEnd, Turn: i, TurnEnd: &session.TurnEndPayload{}})
	}
	first := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"performance","limit":1}`))
	cursor := nextCursor(t, first)

	// Stateless reconstruction with the same binding resumes the sealed interval.
	resumed := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"performance","limit":1,"cursor":"`+cursor+`"}`))
	if resumed["turns"].([]any)[0].(map[string]any)["turn"] != float64(1) {
		t.Fatalf("reconstructed debugger did not resume: %#v", resumed)
	}
	for name, args := range map[string]string{
		"cross-view": `{"view":"network","cursor":"` + cursor + `"}`,
		"oversized":  `{"view":"performance","cursor":"` + strings.Repeat("x", maxContinuationLen+1) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			got := execute(t, New(target.ID, store, log), args)
			if !got.IsError || !strings.Contains(got.Content, continuationInvalid) || strings.Contains(got.Content, `"turns"`) || strings.Contains(got.Content, cursor) {
				t.Fatalf("binding rejection leaked evidence/token: %s", got.Content)
			}
		})
	}
	log.Reset(target.ID)
	_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnEnd, Turn: 99, TurnEnd: &session.TurnEndPayload{}})
	stale := execute(t, New(target.ID, store, log), `{"view":"performance","cursor":"`+cursor+`"}`)
	if !stale.IsError || !strings.Contains(stale.Content, continuationInvalid) || strings.Contains(stale.Content, `"turn":99`) {
		t.Fatalf("generation reset returned partial foreign evidence: %s", stale.Content)
	}
}

func TestDebuggerScanContinuation_QACrossDescendantCursorRejectedBeforeLogRead(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	owner := &session.Principal{Issuer: "qa", Subject: "owner", GrantType: session.GrantTypeUser}
	root := session.New("scope-root", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/scope", Revision: "qa"}, session.Limits{}, time.Unix(1, 0))
	root.Owner = owner.Clone()
	children := make([]*session.Session, 2)
	for i := range children {
		child, err := session.NewSubagent(session.SessionID("scope-child-"+strconv.Itoa(i)), session.ModeDefault, root.EnvironmentRef, root.Limits, time.Unix(int64(i+2), 0), root.ID, root.Incarnation(), session.ToolCallID("call-"+strconv.Itoa(i)))
		if err != nil {
			t.Fatal(err)
		}
		child.Owner = owner.Clone()
		children[i] = child
	}
	if err := store.Save(ctx, root); err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		if err := store.Save(ctx, child); err != nil {
			t.Fatal(err)
		}
	}
	base := memstore.NewEventLog()
	for _, child := range children {
		for turn := 0; turn < 2; turn++ {
			_, _ = base.AppendEvent(ctx, child.ID, session.Event{Type: session.EvTurnEnd, Turn: turn, TurnEnd: &session.TurnEndPayload{}})
		}
	}
	audit := &readAuditLog{base: base}
	bound := NewBound(root.ID, session.DebugTargetFingerprint(root), owner, true, store, audit).(*inspectTool)
	graph := bound.scanLineage(ctx, root)
	if len(graph.Nodes) != 2 {
		t.Fatalf("descendant graph=%+v", graph)
	}
	ownerCtx := session.WithPrincipal(ctx, owner)
	first := continuationResult(t, executeAs(ownerCtx, t, bound, `{"view":"performance","limit":1,"scope_handle":"`+graph.Nodes[0].Handle+`"}`))
	reads := len(audit.calls)
	cross := executeAs(ownerCtx, t, bound, `{"view":"performance","limit":1,"scope_handle":"`+graph.Nodes[1].Handle+`","cursor":"`+nextCursor(t, first)+`"}`)
	if !cross.IsError || cross.Content != continuationInvalid || len(audit.calls) != reads || strings.Contains(cross.Content, `"turns"`) {
		t.Fatalf("cross-descendant cursor reached log/evidence: reads=%d->%d result=%s", reads, len(audit.calls), cross.Content)
	}
	children[0].Relationship.CallID = "changed-lineage-call"
	if err := store.Save(ctx, children[0]); err != nil {
		t.Fatal(err)
	}
	reads = len(audit.calls)
	lineageChanged := executeAs(ownerCtx, t, bound, `{"view":"performance","limit":1,"scope_handle":"`+graph.Nodes[0].Handle+`","cursor":"`+nextCursor(t, first)+`"}`)
	if !lineageChanged.IsError || len(audit.calls) != reads || strings.Contains(lineageChanged.Content, `"turns"`) {
		t.Fatalf("issued token survived descendant lineage change: reads=%d->%d result=%s", reads, len(audit.calls), lineageChanged.Content)
	}
}

func TestDebuggerScanContinuation_QAOwnerAndIncarnationRejectBeforeStorage(t *testing.T) {
	store, target := seededTarget(t, nil)
	owner := &session.Principal{Issuer: "qa", Subject: "owner", GrantType: session.GrantTypeUser}
	foreign := &session.Principal{Issuer: "qa", Subject: "foreign", GrantType: session.GrantTypeUser}
	target.Owner = owner.Clone()
	if err := store.Save(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	base := memstore.NewEventLog()
	for i := 0; i < 2; i++ {
		_, _ = base.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnEnd, Turn: i, TurnEnd: &session.TurnEndPayload{}})
	}
	audit := &readAuditLog{base: base}
	bound := NewBound(target.ID, session.DebugTargetFingerprint(target), owner, true, store, audit)
	ownerCtx := session.WithPrincipal(context.Background(), owner)
	first := continuationResult(t, executeAs(ownerCtx, t, bound, `{"view":"performance","limit":1}`))
	cursor := nextCursor(t, first)

	reads := len(audit.calls)
	wrongOwner := executeAs(session.WithPrincipal(context.Background(), foreign), t, NewBound(target.ID, session.DebugTargetFingerprint(target), foreign, true, store, audit), `{"view":"performance","cursor":"`+cursor+`"}`)
	if !wrongOwner.IsError || len(audit.calls) != reads || strings.Contains(wrongOwner.Content, `"turns"`) {
		t.Fatalf("foreign owner reached storage/partial evidence: reads=%d->%d result=%s", reads, len(audit.calls), wrongOwner.Content)
	}

	changedOwner, err := store.Load(context.Background(), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	changedOwner.Owner = foreign.Clone()
	if err := store.Save(context.Background(), changedOwner); err != nil {
		t.Fatal(err)
	}
	reads = len(audit.calls)
	ownerChanged := executeAs(ownerCtx, t, bound, `{"view":"performance","cursor":"`+cursor+`"}`)
	if !ownerChanged.IsError || len(audit.calls) != reads || strings.Contains(ownerChanged.Content, `"turns"`) {
		t.Fatalf("issued token survived stored-owner change: reads=%d->%d result=%s", reads, len(audit.calls), ownerChanged.Content)
	}
	if err := store.Save(context.Background(), target); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(context.Background(), target.ID); err != nil {
		t.Fatal(err)
	}
	replacement := session.New(target.ID, session.ModeDefault, target.EnvironmentRef, target.Limits, time.Unix(99, 0))
	replacement.Owner = owner.Clone()
	if replacement.Incarnation() == target.Incarnation() {
		t.Fatal("replacement reused target incarnation")
	}
	if err := store.Save(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	reads = len(audit.calls)
	recreated := executeAs(ownerCtx, t, NewBound(replacement.ID, session.DebugTargetFingerprint(replacement), owner, true, store, audit), `{"view":"performance","cursor":"`+cursor+`"}`)
	if !recreated.IsError || len(audit.calls) != reads || !strings.Contains(recreated.Content, continuationInvalid) || strings.Contains(recreated.Content, `"turns"`) {
		t.Fatalf("recreated incarnation reached old storage evidence: reads=%d->%d result=%s", reads, len(audit.calls), recreated.Content)
	}
}
