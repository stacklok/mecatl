package sessiondebug

import (
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestInspectLegacyHistoryActivityAndPerformance(t *testing.T) {
	store, target := seededTarget(t, nil)
	log, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"Type":"compaction.archive","CompactionArchive":{"Replaced":[{"Role":"assistant","Reasoning":"PRIVATE-REPLAY","reasoning_item_id":"PRIVATE-ITEM","ToolCalls":[{"ID":"PRIVATE-PAIR","Name":"Read","Args":{}}]},{"Role":"tool","ToolResult":{"CallID":"PRIVATE-PAIR","Content":"retained archive result"}},{"Role":"tool","ToolResult":{"CallID":"PRIVATE-ORPHAN","Content":"archived orphan text","Parts":[{"Kind":"image","MIMEType":"image/png","Data":"AQI="}]}}]}}`,
		`{"Type":"tool.call","RunID":"old","Seq":1,"ToolCall":{"ID":"PRIVATE-PAIR","Name":"Read","Args":{}}}`,
		`{"Type":"tool.result","RunID":"old","Seq":2,"ToolResult":{"CallID":"PRIVATE-PAIR","Content":"paired failure","IsError":true}}`,
		`{"Type":"tool.result","RunID":"old","Seq":3,"ToolResult":{"CallID":"PRIVATE-ORPHAN","Content":"orphan text","IsError":true,"Parts":[{"Kind":"image","MIMEType":"image/png","Data":"AQI="}]}}`,
	} {
		ev, err := eventcodec.Unmarshal([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := log.Append(t.Context(), target.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	inspect := New(target.ID, store, log)
	check := func(args string) session.ToolResult {
		t.Helper()
		got := execute(t, inspect, args)
		if got.IsError {
			t.Fatal(got.Content)
		}
		for _, private := range []string{"PRIVATE-", "reasoning_item_id", `"Reasoning":`, `"Replay":`, "legacy_identity"} {
			if strings.Contains(got.Content, private) {
				t.Fatalf("inspection leaked %s: %s", private, got.Content)
			}
		}
		return got
	}
	catalog := decodeLineageEvidence[historyCatalog](t, check(`{"view":"history"}`))
	var archiveHandle, retainedHandle string
	for _, src := range catalog.Sources {
		switch src.Source {
		case "compaction_archive":
			archiveHandle = src.HistoryHandle
		case "retained_event_history":
			retainedHandle = src.HistoryHandle
		}
	}
	if archiveHandle == "" || retainedHandle == "" {
		t.Fatalf("legacy archive/history missing: %+v", catalog)
	}
	var retainedOrphanID string
	for _, handle := range []string{archiveHandle, retainedHandle} {
		result := check(`{"view":"history","history_handle":"` + handle + `"}`)
		if handle == archiveHandle && !strings.Contains(result.Content, "retained archive result") {
			t.Fatal("legacy archive result preview lost")
		}
		page := decodeLineageEvidence[struct {
			Transcript transcriptEvidence `json:"transcript"`
		}](t, result)
		found := false
		for _, row := range page.Transcript.Messages {
			if row.HistoricalKind == string(session.EvToolResult) && strings.Contains(row.Text, "orphan text") {
				found = row.HistoricalRecordID != "" && row.AttachmentUnavailable && row.ToolResult == nil && len(row.ToolCalls) == 0 && len(row.Parts) == 1 && row.Parts[0].BinaryOmitted && row.Parts[0].BinaryBytes == 2
				if handle == retainedHandle {
					retainedOrphanID = row.HistoricalRecordID
				}
			}
		}
		if !found {
			t.Fatalf("orphan content/media/source/status lost: %+v", page)
		}
	}
	activity := decodeLineageEvidence[activityEvidence](t, check(`{"view":"activity"}`))
	if len(activity.Rows) != 4 {
		t.Fatalf("activity order/count changed: %+v", activity)
	}
	orphan := activity.Rows[3]
	if orphan.HistoricalRecordID != retainedOrphanID || orphan.HistoricalKind != string(session.EvToolResult) || orphan.Text != "orphan text" || !orphan.AttachmentUnavailable || len(orphan.Parts) != 1 || orphan.ToolError == nil || !*orphan.ToolError {
		t.Fatalf("historical activity facts lost: %+v", orphan)
	}
	performance := decodeLineageEvidence[performanceEvidence](t, check(`{"view":"performance"}`))
	if performance.Totals.ToolFailures != 2 || !performance.ScanComplete {
		t.Fatalf("legacy tool failures lost: %+v", performance)
	}
	status := decodeLineageEvidence[statusEvidence](t, check(`{"view":"status"}`))
	if status.Lifetime.ToolCalls != 1 || status.Lifetime.ToolResults != 2 || status.Lifetime.ToolFailures != 2 {
		t.Fatalf("lifetime legacy facts lost: %+v", status.Lifetime)
	}
}

type boundedHistoryLog struct {
	port.EventLog
	event  session.Event
	reads  int
	cancel context.CancelFunc
}

func (l *boundedHistoryLog) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		for range maxPerformanceScan {
			l.reads++
			if l.cancel != nil {
				l.cancel()
			}
			if !yield(l.event, nil) {
				return
			}
		}
	}
}

func TestHistoryScanByteBudgetAndCancellation(t *testing.T) {
	store, target := seededTarget(t, nil)
	log := &boundedHistoryLog{event: session.Event{Type: session.EvUserPrompt, Text: strings.Repeat("x", 1<<20)}}
	catalog := decodeLineageEvidence[historyCatalog](t, execute(t, New(target.ID, store, log), `{"view":"history"}`))
	if catalog.ScanComplete || catalog.Error == "" || len(catalog.Sources) != 1 || log.reads > 17 {
		t.Fatalf("oversized source was not rejected within budget: reads=%d catalog=%+v", log.reads, catalog)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	log = &boundedHistoryLog{cancel: cancel}
	if _, err := readEvents(ctx, log, target.ID, maxPerformanceScan); err != context.Canceled || log.reads != 1 {
		t.Fatalf("scan did not stop on cancellation: reads=%d err=%v", log.reads, err)
	}
}

func TestIncompleteHistoryScanCannotPublishArchiveOrProvePairs(t *testing.T) {
	store, target := seededTarget(t, nil)
	inspect := New(target.ID, store, generatedLog{count: maxPerformanceScan + 1})
	catalog := decodeLineageEvidence[historyCatalog](t, execute(t, inspect, `{"view":"history"}`))
	if catalog.ScanComplete || catalog.Error == "" || len(catalog.Sources) != 1 {
		t.Fatalf("partial source treated as complete: %+v", catalog)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readEvents(ctx, generatedLog{count: maxPerformanceScan}, target.ID, maxPerformanceScan); err != context.Canceled {
		t.Fatalf("scan ignored cancellation: %v", err)
	}
}
