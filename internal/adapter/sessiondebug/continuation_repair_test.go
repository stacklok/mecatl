package sessiondebug

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	enginetool "github.com/stacklok/mecatl/engine/tool"
)

type countingCursorLog struct {
	base        *memstore.EventLog
	reads       int
	cursorReads int
	beforeRead  func(int, session.SessionID)
}

func (l *countingCursorLog) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	return l.base.Append(ctx, id, ev)
}

func (l *countingCursorLog) AppendEvent(ctx context.Context, id session.SessionID, ev session.Event) (port.Cursor, error) {
	return l.base.AppendEvent(ctx, id, ev)
}

func (l *countingCursorLog) AppendGap(ctx context.Context, id session.SessionID, reason string) (port.Cursor, error) {
	return l.base.AppendGap(ctx, id, reason)
}

func (l *countingCursorLog) Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	l.reads++
	return l.base.Read(ctx, id)
}

func (l *countingCursorLog) ReadAfter(ctx context.Context, id session.SessionID, after port.Cursor, opts port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	l.cursorReads++
	if l.beforeRead != nil {
		l.beforeRead(l.cursorReads, id)
	}
	return l.base.ReadAfter(ctx, id, after, opts)
}

func TestContinuationRepair_StatusUsesOneScanAndPublicationReread(t *testing.T) {
	store, target := seededTarget(t, nil)
	base := memstore.NewEventLog()
	_, _ = base.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnStart})
	log := &countingCursorLog{base: base}

	out := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"status"}`))
	if log.reads != 0 || log.cursorReads != 2 {
		t.Fatalf("event-log reads = legacy:%d cursor:%d, want legacy:0 cursor:2", log.reads, log.cursorReads)
	}
	if out["lifetime_event_log"].(map[string]any)["scanned_events"] != float64(1) {
		t.Fatalf("status lifetime evidence = %#v", out["lifetime_event_log"])
	}
}

func TestContinuationRepair_EndpointMutationBeforePublicationReturnsNoEvidence(t *testing.T) {
	store, target := seededTarget(t, nil)
	base := memstore.NewEventLog()
	_, _ = base.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnEnd, TurnEnd: &session.TurnEndPayload{}})
	log := &countingCursorLog{base: base}
	log.beforeRead = func(call int, id session.SessionID) {
		if call == 2 {
			base.Reset(id)
			_, _ = base.AppendEvent(context.Background(), id, session.Event{Type: session.EvTurnEnd, Turn: 99, TurnEnd: &session.TurnEndPayload{}})
		}
	}

	got := execute(t, New(target.ID, store, log), `{"view":"performance"}`)
	if !got.IsError || !strings.Contains(got.Content, continuationInvalid) || strings.Contains(got.Content, `"turns"`) || strings.Contains(got.Content, `"next_cursor"`) {
		t.Fatalf("mutated endpoint publication = error:%v content:%s", got.IsError, got.Content)
	}
}

func TestContinuationRepair_EndpointReadMutationPrecedesFinalBasisCheck(t *testing.T) {
	store, target := seededTarget(t, nil)
	base := memstore.NewEventLog()
	_, _ = base.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnStart})
	log := &countingCursorLog{base: base}
	log.beforeRead = func(call int, id session.SessionID) {
		if call != 2 {
			return
		}
		mutated, err := store.Load(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		mutated.Limits.MaxTurns++
		if err := store.Save(context.Background(), mutated); err != nil {
			t.Fatal(err)
		}
	}

	got := execute(t, New(target.ID, store, log), `{"view":"status"}`)
	if !got.IsError || !strings.Contains(got.Content, continuationInvalid) || strings.Contains(got.Content, `"lifetime_event_log"`) {
		t.Fatalf("endpoint-time basis mutation = error:%v content:%s", got.IsError, got.Content)
	}
}

func TestContinuationRepair_ResponseBoundOmitsAndAdvancesSingleRow(t *testing.T) {
	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvRequestManifest, RequestManifest: &session.RequestManifestPayload{Provider: strings.Repeat("x", maxEvidenceBytes)}})

	out := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"manifest","limit":1}`))
	page := out["row_page"].(map[string]any)
	if page["returned"] != float64(0) || page["projection_complete"] != false || page["has_more_rows"] != false || out["projection_complete"] != false {
		t.Fatalf("bounded page metadata = %#v", out)
	}
	omitted := out["omitted_rows"].([]any)[0].(map[string]any)
	if omitted["index"] != float64(0) || omitted["reason"] != "response_bound" || out["next_cursor"] != nil {
		t.Fatalf("bounded omission did not advance = %#v", out)
	}
}

func TestContinuationRepair_ResponseBoundOmissionContinuesToValidRowsAndLaterWindow(t *testing.T) {
	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvRequestManifest, RequestManifest: &session.RequestManifestPayload{Provider: strings.Repeat("x", maxEvidenceBytes)}})
	_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvRequestManifest, RequestManifest: &session.RequestManifestPayload{Provider: "first-window-valid"}})
	appendEvents(t, log, target.ID, maxPerformanceScan-2, func(int) session.Event { return session.Event{Type: session.EvTurnStart} })
	_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvRequestManifest, RequestManifest: &session.RequestManifestPayload{Provider: "later-window-valid"}})
	inspect := New(target.ID, store, log)
	firstResult := execute(t, inspect, `{"view":"manifest","limit":1}`)
	if firstResult.IsError || len(firstResult.Content) > maxEvidenceBytes {
		t.Fatalf("bounded first omission result = error:%v bytes:%d content:%s", firstResult.IsError, len(firstResult.Content), firstResult.Content)
	}
	first := continuationResult(t, firstResult)
	omitted := first["omitted_rows"].([]any)[0].(map[string]any)
	if omitted["index"] != float64(0) || nextCursor(t, first) == "" {
		t.Fatalf("first omission did not identify and advance row zero: %#v", first)
	}
	second := continuationResult(t, execute(t, inspect, `{"view":"manifest","limit":1,"cursor":"`+nextCursor(t, first)+`"}`))
	if second["rows"].([]any)[0].(map[string]any)["provider"] != "first-window-valid" || nextCursor(t, second) == "" {
		t.Fatalf("valid row after omission missing or later window hidden: %#v", second)
	}
	third := continuationResult(t, execute(t, inspect, `{"view":"manifest","limit":1,"cursor":"`+nextCursor(t, second)+`"}`))
	if third["rows"].([]any)[0].(map[string]any)["provider"] != "later-window-valid" || third["event_window"].(map[string]any)["id"] == first["event_window"].(map[string]any)["id"] {
		t.Fatalf("later raw window was not reached after omission: %#v", third)
	}
}

func TestContinuationRepair_InitialOffsetDoesNotSkipRawWindow(t *testing.T) {
	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	for _, provider := range []string{"first-zero", "first-one"} {
		_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvRequestManifest, RequestManifest: &session.RequestManifestPayload{Provider: provider}})
	}
	appendEvents(t, log, target.ID, maxPerformanceScan-2, func(int) session.Event { return session.Event{Type: session.EvTurnStart} })
	_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvRequestManifest, RequestManifest: &session.RequestManifestPayload{Provider: "later-zero"}})
	inspect := New(target.ID, store, log)
	first := continuationResult(t, execute(t, inspect, `{"view":"manifest","offset":1,"limit":1}`))
	if first["rows"].([]any)[0].(map[string]any)["provider"] != "first-one" || first["offset"] != float64(1) {
		t.Fatalf("initial offset was not restricted to first raw window: %#v", first)
	}
	later := continuationResult(t, execute(t, inspect, `{"view":"manifest","cursor":"`+nextCursor(t, first)+`"}`))
	if later["rows"].([]any)[0].(map[string]any)["provider"] != "later-zero" {
		t.Fatalf("continuation after initial offset skipped later raw window: %#v", later)
	}
}

func TestContinuationRepair_HistoryFailureReasonsRemainDistinct(t *testing.T) {
	t.Run("gap", func(t *testing.T) {
		store, target := seededTarget(t, nil)
		log := memstore.NewEventLog()
		_, _ = log.AppendGap(context.Background(), target.ID, "private")
		out := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"history"}`))
		if findHistorySource(t, out, sourceRetainedReplay, false)["reason"] != "event_gap" {
			t.Fatalf("gap outcome = %#v", out)
		}
	})
	t.Run("malformed-fold", func(t *testing.T) {
		store, target := seededTarget(t, nil)
		log := memstore.NewEventLog()
		_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvToolResult, ToolResult: ptr(session.NewToolResult("orphan", "x"))})
		out := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"history"}`))
		if findHistorySource(t, out, sourceRetainedReplay, false)["reason"] != "replay_incomplete" {
			t.Fatalf("malformed fold outcome = %#v", out)
		}
	})
	t.Run("read-error", func(t *testing.T) {
		store, target := seededTarget(t, nil)
		out := continuationResult(t, execute(t, New(target.ID, store, failingLog{}), `{"view":"history"}`))
		window := out["event_window"].(map[string]any)
		if out["error"] != errLogReadFailed || window["stop_reason"] != stopReadError || window["gap_records"] != nil {
			t.Fatalf("read error outcome = %#v", out)
		}
		for _, raw := range out["sources"].([]any) {
			if raw.(map[string]any)["source"] == sourceRetainedReplay {
				t.Fatalf("read error fabricated replay source: %#v", out)
			}
		}
	})
}

type cancelAtEOFLog struct{ cancel context.CancelFunc }

func (cancelAtEOFLog) Append(context.Context, session.SessionID, session.Event) error { return nil }

func (l cancelAtEOFLog) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		_ = yield(session.Event{Type: session.EvTurnStart}, nil)
		l.cancel()
	}
}

func TestContinuationRepair_LegacyCancellationAtEOFPublishesNoPartialEvidence(t *testing.T) {
	store, target := seededTarget(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	tool := New(target.ID, store, cancelAtEOFLog{cancel: cancel})
	call := session.ToolCall{ID: "test", Name: ToolName, Args: []byte(`{"view":"status"}`)}
	got, err := tool.Execute(ctx, call, enginetool.Environment{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsError || !strings.Contains(got.Content, errLogReadFailed) || strings.Contains(got.Content, `"lifetime_event_log"`) {
		t.Fatalf("cancelled legacy publication = error:%v content:%s", got.IsError, got.Content)
	}
}

type cancelAtEOFCursorLog struct{ cancel context.CancelFunc }

func (cancelAtEOFCursorLog) Append(context.Context, session.SessionID, session.Event) error {
	return nil
}
func (cancelAtEOFCursorLog) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(func(session.Event, error) bool) {}
}
func (cancelAtEOFCursorLog) AppendEvent(context.Context, session.SessionID, session.Event) (port.Cursor, error) {
	return "", nil
}
func (cancelAtEOFCursorLog) AppendGap(context.Context, session.SessionID, string) (port.Cursor, error) {
	return "", nil
}
func (l cancelAtEOFCursorLog) ReadAfter(context.Context, session.SessionID, port.Cursor, port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	return func(yield func(port.LogRecord, error) bool) {
		_ = yield(port.LogRecord{Kind: port.LogRecordEvent, Cursor: "position", Event: session.Event{Type: session.EvTurnStart}}, nil)
		l.cancel()
	}
}

func TestContinuationRepair_CursorCancellationAtEOFPublishesNoPartialEvidence(t *testing.T) {
	store, target := seededTarget(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	tool := New(target.ID, store, cancelAtEOFCursorLog{cancel: cancel})
	call := session.ToolCall{ID: "test", Name: ToolName, Args: []byte(`{"view":"status"}`)}
	got, err := tool.Execute(ctx, call, enginetool.Environment{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsError || got.Content != errLogReadFailed || strings.Contains(got.Content, `"lifetime_event_log"`) {
		t.Fatalf("cancelled cursor publication = error:%v content:%s", got.IsError, got.Content)
	}
}

type failingLog struct{}

func (failingLog) Append(context.Context, session.SessionID, session.Event) error { return nil }

func (failingLog) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) { _ = yield(session.Event{}, context.DeadlineExceeded) }
}

func TestContinuationRepair_ReadErrorLifetimeIsUnavailable(t *testing.T) {
	store, target := seededTarget(t, nil)
	out := continuationResult(t, execute(t, New(target.ID, store, failingLog{}), `{"view":"status"}`))
	life := out["lifetime_event_log"].(map[string]any)
	if life["available"] != false || life["authoritative"] != false || life["error"] != errLogReadFailed || life["event_window"].(map[string]any)["gap_records"] != nil {
		t.Fatalf("read-error lifetime evidence = %#v", life)
	}
}

type snapshotMutatingStore struct {
	base  *memstore.Store
	loads int
}

func (s *snapshotMutatingStore) Save(ctx context.Context, current *session.Session) error {
	return s.base.Save(ctx, current)
}

func (s *snapshotMutatingStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	s.loads++
	current, err := s.base.Load(ctx, id)
	if err != nil || current == nil || s.loads != 3 {
		return current, err
	}
	current.Conversation.Messages = append(current.Conversation.Messages, session.NewUserMessage("changed during projection"))
	if err := s.base.Save(ctx, current); err != nil {
		return nil, err
	}
	return s.base.Load(ctx, id)
}

func TestContinuationRepair_SnapshotBasisMutationBeforePublicationReturnsNoEvidence(t *testing.T) {
	base, target := seededTarget(t, nil)
	store := &snapshotMutatingStore{base: base}
	log := memstore.NewEventLog()
	_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnStart})

	got := execute(t, New(target.ID, store, log), `{"view":"history"}`)
	if !got.IsError || !strings.Contains(got.Content, continuationInvalid) || strings.Contains(got.Content, `"sources"`) || strings.Contains(got.Content, `"history_handle"`) {
		t.Fatalf("mutated snapshot publication = error:%v content:%s", got.IsError, got.Content)
	}
}

func TestContinuationRepair_DuplicateLineageKeysFailClosed(t *testing.T) {
	_, root := seededTarget(t, nil)
	child, err := session.NewSubagent("child", session.ModeDefault, root.EnvironmentRef, session.Limits{}, root.CreatedAt, root.ID, root.Incarnation(), "call")
	if err != nil {
		t.Fatal(err)
	}
	node := lineageNode{ID: child.ID, Kind: child.Kind, Relationship: child.Relationship, OwnerScope: session.PrincipalScopeHash(root.Owner), Incarnation: string(child.Incarnation()), State: string(port.SessionLineageRetained), Edge: "subagent", Handle: "first"}
	duplicate := node
	duplicate.Handle = "second"
	scan := eventScan{Events: []session.Event{{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{ParentCallID: "call", ChildID: string(child.ID), ChildIncarnation: child.Incarnation()}}}}

	projection := delegationFromScan(scan, root, lineageGraph{Nodes: []lineageNode{node, duplicate}}, 0, 10)
	if rows := projection.Value["rows"].([]any); len(rows) != 0 {
		t.Fatalf("ambiguous lineage projected rows: %#v", rows)
	}
}

func TestContinuationRepair_ArchiveSelectionHasPostProjectionEndpointCheck(t *testing.T) {
	store, target := seededTarget(t, nil)
	base := memstore.NewEventLog()
	archive := &session.CompactionArchivePayload{Replaced: []session.Message{session.NewUserMessage("archived")}}
	_, _ = base.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvCompactionArchive, CompactionArchive: archive})
	log := &countingCursorLog{base: base}
	inspect := New(target.ID, store, log)
	catalog := continuationResult(t, execute(t, inspect, `{"view":"history"}`))
	var handle string
	for _, raw := range catalog["sources"].([]any) {
		source := raw.(map[string]any)
		if source["source"] == sourceArchive {
			handle, _ = source["history_handle"].(string)
		}
	}
	if handle == "" {
		t.Fatalf("archive handle missing: %#v", catalog)
	}
	log.beforeRead = func(call int, id session.SessionID) {
		if call == 4 {
			base.Reset(id)
			_, _ = base.AppendEvent(context.Background(), id, session.Event{Type: session.EvCompactionArchive, CompactionArchive: archive})
		}
	}
	got := execute(t, inspect, `{"view":"history","history_handle":"`+handle+`"}`)
	if !got.IsError || !strings.Contains(got.Content, "invalid or stale history handle") || strings.Contains(got.Content, `"transcript"`) {
		t.Fatalf("mutated archive selection = error:%v content:%s", got.IsError, got.Content)
	}
}

func TestContinuationRepair_HistorySelectionRevalidatesAuthorizationAfterEndpoint(t *testing.T) {
	for _, selectedSource := range []string{sourceArchive, sourceRetainedReplay} {
		t.Run(selectedSource, func(t *testing.T) {
			store, target := seededTarget(t, nil)
			base := memstore.NewEventLog()
			_, _ = base.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvSessionInit})
			_, _ = base.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvCompactionArchive, CompactionArchive: &session.CompactionArchivePayload{Replaced: []session.Message{session.NewUserMessage("archived")}}})
			log := &countingCursorLog{base: base}
			inspect := New(target.ID, store, log)
			catalog := continuationResult(t, execute(t, inspect, `{"view":"history"}`))
			handle := findHistorySource(t, catalog, selectedSource, true)["history_handle"].(string)
			log.beforeRead = func(call int, id session.SessionID) {
				if call != 4 {
					return
				}
				mutated, err := store.Load(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				mutated.Owner = &session.Principal{Issuer: "qa", Subject: "new-owner", GrantType: session.GrantTypeUser}
				if err := store.Save(context.Background(), mutated); err != nil {
					t.Fatal(err)
				}
			}
			got := execute(t, inspect, `{"view":"history","history_handle":"`+handle+`"}`)
			if !got.IsError || strings.Contains(got.Content, `"transcript"`) || strings.Contains(got.Content, "archived") {
				t.Fatalf("%s endpoint-time authorization mutation published evidence: %s", selectedSource, got.Content)
			}
		})
	}
}

type legacyEventsLog struct{ events []session.Event }

func (legacyEventsLog) Append(context.Context, session.SessionID, session.Event) error { return nil }
func (l legacyEventsLog) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		for _, ev := range l.events {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func TestContinuationRepair_LegacyHistoryHandlesRejectMissingLog(t *testing.T) {
	store, target := seededTarget(t, nil)
	legacy := legacyEventsLog{events: []session.Event{
		{Type: session.EvSessionInit},
		{Type: session.EvCompactionArchive, CompactionArchive: &session.CompactionArchivePayload{Replaced: []session.Message{session.NewUserMessage("archived")}}},
	}}
	catalog := continuationResult(t, execute(t, New(target.ID, store, legacy), `{"view":"history"}`))
	handles := map[string]string{}
	for _, raw := range catalog["sources"].([]any) {
		source := raw.(map[string]any)
		if handle, _ := source["history_handle"].(string); handle != "" && source["source"] != "current_snapshot" {
			handles[source["source"].(string)] = handle
		}
	}
	for _, source := range []string{sourceArchive, sourceRetainedReplay} {
		handle := handles[source]
		if handle == "" {
			t.Fatalf("valid legacy %s handle missing: %#v", source, catalog)
		}
		got := execute(t, New(target.ID, store, nil), `{"view":"history","history_handle":"`+handle+`"}`)
		if !got.IsError || !strings.Contains(got.Content, "invalid or stale history handle") {
			t.Fatalf("missing-log %s selection = error:%v content:%s", source, got.IsError, got.Content)
		}
	}
}

func TestContinuationRepair_HistoryHandleCannotBeUsedAsCursor(t *testing.T) {
	store, target := seededTarget(t, nil)
	log := memstore.NewEventLog()
	_, _ = log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvCompactionArchive, CompactionArchive: &session.CompactionArchivePayload{Replaced: []session.Message{session.NewUserMessage("archive")}}})
	catalog := continuationResult(t, execute(t, New(target.ID, store, log), `{"view":"history"}`))
	handle := findHistorySource(t, catalog, sourceArchive, true)["history_handle"].(string)
	got := execute(t, New(target.ID, store, log), `{"view":"history","cursor":"`+handle+`"}`)
	if !got.IsError || got.Content != continuationInvalid || strings.Contains(got.Content, `"sources"`) {
		t.Fatalf("history-purpose token used as cursor = error:%v content:%s", got.IsError, got.Content)
	}
}

type scriptedCursorLog struct {
	*countingCursorLog
	emptyContinuation bool
	resetBeforeCheck  bool
}

func (l *scriptedCursorLog) ReadAfter(ctx context.Context, id session.SessionID, after port.Cursor, opts port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	l.cursorReads++
	if l.emptyContinuation && after != "" && opts.Limit == maxPerformanceScan+1 {
		return func(func(port.LogRecord, error) bool) {}
	}
	if l.resetBeforeCheck && l.cursorReads == 4 {
		l.base.Reset(id)
		_, _ = l.base.AppendEvent(context.Background(), id, session.Event{Type: session.EvTurnStart})
	}
	return l.base.ReadAfter(ctx, id, after, opts)
}

func TestContinuationRepair_EmptyNextWindowStillChecksPriorEndpoint(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(map[bool]string{false: "stable", true: "reset"}[reset], func(t *testing.T) {
			store, target := seededTarget(t, nil)
			base := memstore.NewEventLog()
			appendEvents(t, base, target.ID, maxPerformanceScan+1, func(int) session.Event { return session.Event{Type: session.EvTurnStart} })
			log := &scriptedCursorLog{countingCursorLog: &countingCursorLog{base: base}, emptyContinuation: true}
			inspect := New(target.ID, store, log)
			first := continuationResult(t, execute(t, inspect, `{"view":"network"}`))
			log.resetBeforeCheck = reset
			got := execute(t, inspect, `{"view":"network","cursor":"`+nextCursor(t, first)+`"}`)
			if log.cursorReads != 4 {
				t.Fatalf("cursor reads=%d, want scan+checkpoint for both calls", log.cursorReads)
			}
			if reset {
				if !got.IsError || !strings.Contains(got.Content, continuationInvalid) {
					t.Fatalf("reset endpoint result = error:%v content:%s", got.IsError, got.Content)
				}
			} else if got.IsError {
				t.Fatalf("stable empty continuation failed: %s", got.Content)
			}
		})
	}
}

type errorCursorLog struct{ err error }

func (errorCursorLog) Append(context.Context, session.SessionID, session.Event) error { return nil }
func (errorCursorLog) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(func(session.Event, error) bool) {}
}
func (errorCursorLog) AppendEvent(context.Context, session.SessionID, session.Event) (port.Cursor, error) {
	return "", nil
}
func (errorCursorLog) AppendGap(context.Context, session.SessionID, string) (port.Cursor, error) {
	return "", nil
}
func (l errorCursorLog) ReadAfter(context.Context, session.SessionID, port.Cursor, port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	return func(yield func(port.LogRecord, error) bool) { _ = yield(port.LogRecord{}, l.err) }
}

type failNthCursorRead struct {
	base   port.CursorEventLog
	calls  int
	failAt int
	err    error
}

func (l *failNthCursorRead) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	return l.base.Append(ctx, id, ev)
}
func (l *failNthCursorRead) Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	return l.base.Read(ctx, id)
}
func (l *failNthCursorRead) AppendEvent(ctx context.Context, id session.SessionID, ev session.Event) (port.Cursor, error) {
	return l.base.AppendEvent(ctx, id, ev)
}
func (l *failNthCursorRead) AppendGap(ctx context.Context, id session.SessionID, reason string) (port.Cursor, error) {
	return l.base.AppendGap(ctx, id, reason)
}
func (l *failNthCursorRead) ReadAfter(ctx context.Context, id session.SessionID, after port.Cursor, opts port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	l.calls++
	if l.calls == l.failAt {
		return func(yield func(port.LogRecord, error) bool) { _ = yield(port.LogRecord{}, l.err) }
	}
	return l.base.ReadAfter(ctx, id, after, opts)
}

func TestContinuationRepair_RowAndEndpointIOErrorsKeepCategories(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{{"expired", port.ErrCursorExpired, continuationInvalid}, {"transient", errors.New("PRIVATE ENDPOINT DETAIL"), errLogReadFailed}} {
		t.Run("row-"+tc.name, func(t *testing.T) {
			store, target := seededTarget(t, nil)
			base := memstore.NewEventLog()
			for i := 0; i < 2; i++ {
				_, _ = base.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnEnd, Turn: i, TurnEnd: &session.TurnEndPayload{}})
			}
			first := continuationResult(t, execute(t, New(target.ID, store, base), `{"view":"performance","limit":1}`))
			failing := &failNthCursorRead{base: base, failAt: 1, err: tc.err}
			got := execute(t, New(target.ID, store, failing), `{"view":"performance","limit":1,"cursor":"`+nextCursor(t, first)+`"}`)
			if !got.IsError || got.Content != tc.want || strings.Contains(got.Content, "PRIVATE") {
				t.Fatalf("row error = error:%v content:%q want %q", got.IsError, got.Content, tc.want)
			}
		})
		t.Run("endpoint-"+tc.name, func(t *testing.T) {
			store, target := seededTarget(t, nil)
			base := memstore.NewEventLog()
			_, _ = base.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnStart})
			failing := &failNthCursorRead{base: base, failAt: 2, err: tc.err}
			got := execute(t, New(target.ID, store, failing), `{"view":"network"}`)
			if !got.IsError || got.Content != tc.want || strings.Contains(got.Content, "PRIVATE") {
				t.Fatalf("endpoint error = error:%v content:%q want %q", got.IsError, got.Content, tc.want)
			}
		})
	}
}

func TestContinuationRepair_CursorErrorCategoriesAreSanitized(t *testing.T) {
	store, target := seededTarget(t, nil)
	base := memstore.NewEventLog()
	appendEvents(t, base, target.ID, maxPerformanceScan+1, func(int) session.Event { return session.Event{Type: session.EvTurnStart} })
	first := continuationResult(t, execute(t, New(target.ID, store, base), `{"view":"network"}`))
	token := nextCursor(t, first)
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{{"expired", port.ErrCursorExpired, continuationInvalid}, {"malformed", port.ErrCursorMalformed, continuationInvalid}, {"unsupported", port.ErrCursorUnsupported, continuationInvalid}, {"transient", errors.New("PRIVATE BACKEND DETAIL"), errLogReadFailed}} {
		t.Run(tc.name, func(t *testing.T) {
			got := execute(t, New(target.ID, store, errorCursorLog{err: tc.err}), `{"view":"network","cursor":"`+token+`"}`)
			if !got.IsError || got.Content != tc.want || strings.Contains(got.Content, "PRIVATE") {
				t.Fatalf("categorized error = error:%v content:%q, want %q", got.IsError, got.Content, tc.want)
			}
		})
	}
}
