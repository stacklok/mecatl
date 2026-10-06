package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// Exercise the same Build, persistence, and HTTP event relay used by mecated,
// with a separate Reader following the store while the run writes it.
func TestJSONLReaderPersistenceRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	dir := t.TempDir()
	usage := session.Usage{InputTokens: 17, OutputTokens: 3}
	turn := mockllm.TextTurn("done")
	for i := range turn.Chunks {
		if turn.Chunks[i].Kind == port.ChunkUsage {
			turn.Chunks[i].Usage = &usage
		}
	}
	cfg := Config{
		Workspace: t.TempDir(), NoSoul: true, StoreDir: dir, MemoryDir: t.TempDir(), UserModelDir: t.TempDir(),
		envDetector: fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}), liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("write", "Write", json.RawMessage(`{"path":"note.txt","content":"test"}`))), turn)
		},
	}
	built, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := jsonlstore.OpenReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.NewHTTPHandler(built.Service))
	defer srv.Close()
	type followed struct {
		cursor port.Cursor
		count  int
		err    error
	}
	done := make(chan followed, 1)
	go func() {
		result := followed{}
		for rec, err := range reader.ReadAfter(ctx, sess.ID, "", port.ReadOptions{Follow: true}) {
			if err != nil {
				result.err = err
				break
			}
			result.count++
			if rec.Kind == port.LogRecordEvent && rec.Event.Type == session.EvResult {
				result.cursor = rec.Cursor
				break
			}
		}
		done <- result
	}()
	sawAsk := promptOverHTTP(t, srv.URL, string(sess.ID), "write a note", func(ev sseEvent) {
		if ev.Type != "permission.ask" {
			return
		}
		// The relay persists an awaiting snapshot before delivering the ask.
		if saved, err := reader.Load(ctx, sess.ID); err != nil || saved.ID != sess.ID {
			t.Errorf("concurrent Load: %+v, %v", saved, err)
		}
		body, err := json.Marshal(map[string]any{"expected_run_id": ev.RunID, "ask_id": ev.Ask.AskID, "verdict": session.VerdictStringAllowOnce})
		if err != nil {
			t.Error(err)
			return
		}
		resp, err := http.Post(srv.URL+"/v1/sessions/"+string(sess.ID)+"/controls/resolve-ask", "application/json", strings.NewReader(string(body)))
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("approval status: %s", resp.Status)
		}
	})
	if !sawAsk {
		t.Fatal("relay did not deliver permission ask")
	}
	var result followed
	select {
	case result = <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if result.err != nil || result.cursor == "" || result.count < 3 {
		t.Fatalf("follow: %+v", result)
	}
	saved, err := reader.Load(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.UsageFor(session.UsageKindMain); got != usage {
		t.Fatalf("saved usage = %+v, want %+v", got, usage)
	}
	if _, err := writer.MetaList(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := reader.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1})
	if err != nil || len(page.Sessions) != 1 {
		t.Fatalf("metadata: %+v, %v", page, err)
	}
	gapCursor, err := writer.AppendGap(ctx, sess.ID, "test relay delivery gap")
	if err != nil {
		t.Fatal(err)
	}
	gaps := 0
	for rec, err := range reader.ReadAfter(ctx, sess.ID, result.cursor, port.ReadOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		if rec.Kind == port.LogRecordGap && rec.Cursor == gapCursor {
			gaps++
		}
	}
	if gaps != 1 {
		t.Fatalf("gap count: %d", gaps)
	}
	if err := built.Service.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Load(ctx, sess.ID); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("load pruned session: %v", err)
	}
	var replayErr error
	for _, err := range reader.ReadAfter(ctx, sess.ID, gapCursor, port.ReadOptions{}) {
		replayErr = err
	}
	if !errors.Is(replayErr, port.ErrCursorExpired) {
		t.Fatalf("pruned cursor: %v", replayErr)
	}
	lineage, err := reader.ReadSessionLineage(ctx, port.SessionLineageQuery{RootID: sess.ID, RootIncarnation: saved.Incarnation(), Limit: 10})
	if err != nil || len(lineage.Records) != 1 || lineage.Records[0].State != port.SessionLineagePruned {
		t.Fatalf("pruned lineage: %+v, %v", lineage, err)
	}
}
