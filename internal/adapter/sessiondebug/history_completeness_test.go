package sessiondebug

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestHistoryRequiresCompleteCursorEvidence(t *testing.T) {
	for _, kind := range []string{"current", "legacy-record", "gap", "excluded", "no-cursor"} {
		t.Run(kind, func(t *testing.T) {
			_, source := seededTarget(t, []session.Message{session.NewUserMessage("authoritative snapshot")})
			if kind == "excluded" {
				source.ExcludeFromLearning()
			}
			dir := t.TempDir()
			store, err := jsonlstore.New(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			if err := store.Append(t.Context(), source.ID, session.Event{Type: session.EvUserPrompt, UserPrompt: &session.UserPromptPayload{Text: "retained current prompt"}}); err != nil {
				t.Fatal(err)
			}
			if kind == "legacy-record" {
				sum := sha256.Sum256([]byte(source.ID))
				path := filepath.Join(dir, "sid-v1", fmt.Sprintf("sid-v1-%s-%x.events.jsonl", source.ID, sum[:16]))
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, writeErr := file.WriteString(`{"v":"eventlog-json/1","ev":{"Type":"tool.call","ToolCall":{"ID":"provider-private-sentinel","Name":"Read"}}}` + "\n")
				closeErr := file.Close()
				if writeErr != nil || closeErr != nil {
					t.Fatalf("append legacy fixture: %v %v", writeErr, closeErr)
				}
			}
			if kind == "gap" {
				if _, err := store.AppendGap(t.Context(), source.ID, "missing record"); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Append(t.Context(), source.ID, session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}}); err != nil {
				t.Fatal(err)
			}
			if kind == "legacy-record" {
				count := 0
				for event, err := range store.Read(t.Context(), source.ID) {
					if err != nil || event.Type == session.EvToolCall {
						t.Fatalf("legacy filtered stream: %+v %v", event, err)
					}
					count++
				}
				if count != 2 {
					t.Fatalf("mixed stream lost current records: %d", count)
				}
			}
			loaded, err := store.Load(t.Context(), source.ID)
			if err != nil {
				t.Fatal(err)
			}
			var log port.EventLog = store
			if kind == "no-cursor" {
				log = struct{ port.EventLog }{store}
			}
			inspect := New(source.ID, store, log).(*inspectTool)
			value, err := inspect.historyView(t.Context(), loaded, "root", "", 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			catalog := value.(historyCatalog)
			if catalog.ScanComplete != (kind == "current") {
				t.Fatalf("scan completeness: %+v", catalog)
			}
			for _, src := range catalog.Sources {
				if src.Source == "current_snapshot" {
					if !src.Authoritative || !src.ProjectionComplete || src.Messages != 1 {
						t.Fatalf("snapshot lost authority: %+v", src)
					}
					page, err := inspect.historyView(t.Context(), loaded, "root", src.HistoryHandle, 0, 10)
					if err != nil || !strings.Contains(fmt.Sprintf("%+v", page), "authoritative snapshot") {
						t.Fatalf("snapshot unavailable: %+v %v", page, err)
					}
				}
				if src.Source == "retained_event_history" && (src.Authoritative || src.ProjectionComplete) != (kind == "current") {
					t.Fatalf("incomplete history claimed authority: %+v", src)
				}
			}
			if kind == "current" && len(catalog.Sources) != 2 {
				t.Fatalf("current retained history unavailable: %+v", catalog)
			}
		})
	}
}

type failingHistoryCursor struct{ port.CursorEventLog }

func (f failingHistoryCursor) ReadAfter(ctx context.Context, id session.SessionID, after port.Cursor, opts port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	return func(yield func(port.LogRecord, error) bool) {
		for record, err := range f.CursorEventLog.ReadAfter(ctx, id, after, opts) {
			if !yield(record, err) {
				return
			}
		}
		yield(port.LogRecord{}, errors.New("incomplete scan"))
	}
}

type cancelingHistoryCursor struct {
	port.CursorEventLog
	cancel context.CancelFunc
}

func (c cancelingHistoryCursor) ReadAfter(ctx context.Context, id session.SessionID, after port.Cursor, opts port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	return func(yield func(port.LogRecord, error) bool) {
		for record, err := range c.CursorEventLog.ReadAfter(ctx, id, after, opts) {
			if !yield(record, err) {
				return
			}
		}
		c.cancel()
	}
}

func TestReadHistoryEventsRejectsIncompleteScans(t *testing.T) {
	for _, kind := range []string{"canceled", "canceled-at-tail", "bytes", "records", "read-error"} {
		t.Run(kind, func(t *testing.T) {
			log := memstore.NewEventLog()
			event := session.Event{Type: session.EvMessageDelta, Text: "small"}
			if kind == "bytes" {
				event.Text = strings.Repeat("x", 16<<20)
			}
			if err := log.Append(t.Context(), "source", event); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			limit := 10
			var reader port.EventLog = log
			switch kind {
			case "canceled":
				cancel()
			case "canceled-at-tail":
				reader = cancelingHistoryCursor{CursorEventLog: log, cancel: cancel}
			case "records":
				limit = 0
			case "read-error":
				reader = failingHistoryCursor{CursorEventLog: log}
			}
			if events, err := readEvents(ctx, reader, "source", limit); err == nil {
				t.Fatalf("incomplete scan accepted: %d events", len(events))
			}
		})
	}
}
