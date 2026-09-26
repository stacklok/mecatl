package sessiondebug

import (
	"context"
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

type failingLog struct{}

func (failingLog) Append(context.Context, session.SessionID, session.Event) error { return nil }

func (failingLog) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) { _ = yield(session.Event{}, context.DeadlineExceeded) }
}

func TestContinuationRepair_ReadErrorLifetimeIsUnavailable(t *testing.T) {
	store, target := seededTarget(t, nil)
	out := continuationResult(t, execute(t, New(target.ID, store, failingLog{}), `{"view":"status"}`))
	life := out["lifetime_event_log"].(map[string]any)
	if life["available"] != false || life["authoritative"] != false || life["error"] != errLogReadFailed {
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
