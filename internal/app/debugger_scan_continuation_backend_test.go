package app

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/grpcdriver"
	"github.com/stacklok/mecatl/internal/adapter/sessiondebug"
)

func executeDebuggerBackend(t *testing.T, inspect tool.Tool, args string) session.ToolResult {
	t.Helper()
	got, err := inspect.Execute(context.Background(), session.NewToolCall("qa", sessiondebug.ToolName, []byte(args)), tool.Environment{})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func backendResultCursor(t *testing.T, result session.ToolResult) string {
	t.Helper()
	if result.IsError {
		t.Fatalf("debugger result: %s", result.Content)
	}
	start, end := strings.IndexByte(result.Content, '{'), strings.LastIndexByte(result.Content, '}')
	if start < 0 || end < start {
		t.Fatalf("missing JSON result: %s", result.Content)
	}
	var out struct {
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(result.Content[start:end+1]), &out); err != nil {
		t.Fatal(err)
	}
	return out.NextCursor
}

func TestDebuggerScanContinuation_BackendMatrixUsesRealContinuation(t *testing.T) {
	for _, backend := range cursorBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			log := backend.new(t)
			store := memstore.New()
			target := session.New(session.SessionID("backend-"+backend.name), session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/target", Revision: "qa"}, session.Limits{}, time.Unix(1, 0))
			if err := store.Save(context.Background(), target); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if _, err := log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnEnd, Turn: i, TurnEnd: &session.TurnEndPayload{DurationMs: int64(i + 1)}}); err != nil {
					t.Fatal(err)
				}
			}
			inspect := sessiondebug.NewBound(target.ID, session.DebugTargetFingerprint(target), target.Owner, false, store, log)
			first := executeDebuggerBackend(t, inspect, `{"view":"performance","limit":1}`)
			cursor := backendResultCursor(t, first)
			if cursor == "" {
				t.Fatalf("%s omitted a row continuation: %s", backend.name, first.Content)
			}
			second := executeDebuggerBackend(t, inspect, `{"view":"performance","cursor":"`+cursor+`","limit":1}`)
			if second.IsError || !strings.Contains(second.Content, `"turn":1`) || strings.Contains(second.Content, `"turn":0`) {
				t.Fatalf("%s continuation replayed/skipped rows: %s", backend.name, second.Content)
			}
		})
	}
}

type oldDriverLog struct{ reads *int }

func (oldDriverLog) Append(context.Context, session.SessionID, session.Event) error { return nil }
func (l oldDriverLog) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	if l.reads != nil {
		*l.reads++
	}
	return func(yield func(session.Event, error) bool) {
		yield(session.Event{Type: session.EvTurnEnd, Turn: 7, TurnEnd: &session.TurnEndPayload{DurationMs: 1}}, nil)
	}
}

func TestDebuggerScanContinuation_OldGRPCServerRetainsAliasAndRefusesResume(t *testing.T) {
	legacyReads := 0
	remote := newDriverClient(t, oldDriverLog{reads: &legacyReads})
	var got error
	for _, err := range remote.ReadAfter(context.Background(), "old", "", port.ReadOptions{Limit: 1}) {
		got = err
	}
	//nolint:staticcheck // AC4.3 explicitly verifies the deprecated alias remains errors.Is-compatible.
	if got == nil || !errors.Is(got, port.ErrCursorUnsupported) || !errors.Is(got, grpcdriver.ErrDriverCursorUnsupported) {
		t.Fatalf("old server error=%v did not retain both sentinel identities", got)
	}

	store := memstore.New()
	target := session.New("old-driver-target", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/target", Revision: "qa"}, session.Limits{}, time.Unix(1, 0))
	if err := store.Save(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	inspect := sessiondebug.NewBound(target.ID, session.DebugTargetFingerprint(target), target.Owner, false, store, remote)
	first := executeDebuggerBackend(t, inspect, `{"view":"performance"}`)
	if first.IsError || !strings.Contains(first.Content, `"continuation_supported":false`) || !strings.Contains(first.Content, `"turn":7`) || backendResultCursor(t, first) != "" {
		t.Fatalf("old server initial fallback=%s", first.Content)
	}
	continued := executeDebuggerBackend(t, inspect, `{"view":"performance","cursor":"dbgcur.v1.invalid"}`)
	if !continued.IsError || !strings.Contains(continued.Content, "continuation is invalid or stale") || strings.Contains(continued.Content, `"turn":7`) {
		t.Fatalf("unsupported continuation replayed prefix evidence: %s", continued.Content)
	}

	modernBase := memstore.NewEventLog()
	for i := 0; i < 2; i++ {
		if _, err := modernBase.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnEnd, Turn: i, TurnEnd: &session.TurnEndPayload{}}); err != nil {
			t.Fatal(err)
		}
	}
	modern := newDriverClient(t, modernBase)
	modernInspect := sessiondebug.NewBound(target.ID, session.DebugTargetFingerprint(target), target.Owner, false, store, modern)
	validCursor := backendResultCursor(t, executeDebuggerBackend(t, modernInspect, `{"view":"performance","limit":1}`))
	readsBefore := legacyReads
	lostCapability := executeDebuggerBackend(t, inspect, `{"view":"performance","limit":1,"cursor":"`+validCursor+`"}`)
	if !lostCapability.IsError || !strings.Contains(lostCapability.Content, "continuation is invalid or stale") || legacyReads != readsBefore || strings.Contains(lostCapability.Content, `"turn":7`) {
		t.Fatalf("valid continuation after remote capability loss fell back to legacy Read: reads=%d->%d result=%s", readsBefore, legacyReads, lostCapability.Content)
	}
}
