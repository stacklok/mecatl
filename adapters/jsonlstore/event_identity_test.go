package jsonlstore_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func eventCursorPosition(t *testing.T, cursor port.Cursor) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(string(cursor))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Position string `json:"p"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Position
}

func TestLegacyMixedEventIdentityReadRestartAndCursor(t *testing.T) {
	ctx := t.Context()
	st, dir := newStore(t)
	const id session.SessionID = "legacy-events"
	after, err := st.AppendGap(ctx, id, "fixture gap")
	if err != nil {
		t.Fatal(err)
	}
	path := canonicalFamilyPath(dir, id, ".events.jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raws := []string{
		`{"v":"eventlog-json/1","ev":{"Type":"tool.call","Seq":22,"ToolCall":{"ID":"private-provider","Name":"Read","Args":{}}}}`,
		`{"v":"eventlog-json/1","ev":{"Type":"tool.result","Seq":1,"ToolResult":{"CallID":"private-provider","Content":"old"}}}`,
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range raws {
		if _, err := f.WriteString(raw + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for ev, err := range st.Read(ctx, id) {
		t.Fatalf("legacy event escaped omission: %+v %v", ev, err)
	}
	// Even an omitted-only page must expose its original physical position.
	offset := len(before)
	for _, raw := range raws {
		count := 0
		for rec, err := range st.ReadAfter(ctx, id, after, port.ReadOptions{Limit: 1}) {
			if err != nil {
				t.Fatal(err)
			}
			offset += len(raw) + 1
			if eventCursorPosition(t, rec.Cursor) != strconv.Itoa(offset) || rec.Kind != port.LogRecordGap || rec.GapReason != eventcodec.ErrSkipLegacyRecord.Error() || !reflect.DeepEqual(rec.Event, session.Event{}) {
				t.Fatalf("skip gap: %+v", rec)
			}
			after = rec.Cursor
			count++
		}
		if count != 1 {
			t.Fatalf("page size %d", count)
		}
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, unchanged) {
		t.Fatal("read rewrote legacy records")
	}
	// Converted identity-free events and current canonical IDs retain their sequence.
	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"v":"eventlog-json/1","ev":{"Type":"message.delta","Seq":37,"Text":"old text"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall(session.NewToolCallID(), "Read", []byte(`{}`))
	want := []session.Event{{Type: session.EvMessageDelta, Seq: 37, Text: "old text"}, {Type: session.EvToolCall, Seq: 90, ToolCall: &call}, {Type: session.EvToolResult, Seq: 91, ToolResult: &session.ToolResult{CallID: call.ID, Content: "new"}}}
	for _, ev := range want[1:] {
		if err := st.Append(ctx, id, ev); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []session.Event
	for ev, err := range reopened.Read(ctx, id) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ev)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed read: %+v", got)
	}
	count := 0
	for rec, err := range reopened.ReadAfter(ctx, id, after, port.ReadOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		if rec.Kind != port.LogRecordEvent || !reflect.DeepEqual(rec.Event, want[count]) {
			t.Fatalf("mixed cursor: %+v", rec)
		}
		count++
	}
	if count != len(want) {
		t.Fatalf("cursor count %d", count)
	}
}

func TestLegacySkippedTailFollow(t *testing.T) {
	st, dir := newStore(t)
	const id session.SessionID = "legacy-follow"
	after, err := st.AppendGap(t.Context(), id, "start")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(canonicalFamilyPath(dir, id, ".events.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"v":"eventlog-json/1","ev":{"Type":"tool.result","ToolResult":{"CallID":"provider"}}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	count := 0
	for rec, err := range st.ReadAfter(ctx, id, after, port.ReadOptions{Follow: true}) {
		if err != nil {
			t.Fatal(err)
		}
		count++
		if count == 1 {
			if rec.Kind != port.LogRecordGap {
				t.Fatal(rec)
			}
			if err := st.Append(ctx, id, session.Event{Type: session.EvMessageDelta, Seq: 77, Text: "live"}); err != nil {
				t.Fatal(err)
			}
		} else {
			if rec.Event.Seq != 77 {
				t.Fatal(rec)
			}
			cancel()
			break
		}
	}
	if count != 2 {
		t.Fatalf("follow count %d", count)
	}
}
