package sessiondebug

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
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
	args := `{"view":"` + view + `","limit":` + string(rune('0'+firstLimit)) + `}`
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
		out = continuationResult(t, execute(t, inspect, `{"view":"`+view+`","limit":`+string(rune('0'+limit))+`,"cursor":"`+cursor+`"}`))
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
