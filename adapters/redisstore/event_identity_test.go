package redisstore_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/adapters/redisstore"
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
	mr := miniredis.RunT(t)
	st, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const id session.SessionID = "legacy-events"
	after, err := st.AppendGap(ctx, id, "fixture gap")
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	const key = "mecatl:events:" + string(id)
	var positions []string
	for _, record := range []string{
		`{"v":"redisstore-eventlog/1","ev":{"Type":"tool.call","Seq":30,"ToolCall":{"ID":"private-provider","Name":"Read","Args":{}}}}`,
		`{"v":"redisstore-eventlog/1","ev":{"Type":"tool.result","Seq":2,"ToolResult":{"CallID":"private-provider","Content":"old"}}}`,
	} {
		pos, err := client.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: map[string]any{"r": record}}).Result()
		if err != nil {
			t.Fatal(err)
		}
		positions = append(positions, pos)
	}
	before, err := client.XRange(ctx, key, "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	for ev, err := range st.Read(ctx, id) {
		t.Fatalf("legacy event escaped omission: %+v %v", ev, err)
	}
	for _, pos := range positions {
		count := 0
		for rec, err := range st.ReadAfter(ctx, id, after, port.ReadOptions{Limit: 1}) {
			if err != nil {
				t.Fatal(err)
			}
			if eventCursorPosition(t, rec.Cursor) != pos || rec.Kind != port.LogRecordGap || rec.GapReason != eventcodec.ErrSkipLegacyRecord.Error() || !reflect.DeepEqual(rec.Event, session.Event{}) {
				t.Fatalf("skip gap: %+v", rec)
			}
			after = rec.Cursor
			count++
		}
		if count != 1 {
			t.Fatalf("page size %d", count)
		}
	}
	unchanged, err := client.XRange(ctx, key, "-", "+").Result()
	if err != nil || !reflect.DeepEqual(before, unchanged) {
		t.Fatal("reader rewrote legacy stream")
	}
	if err := client.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: map[string]any{"r": `{"v":"redisstore-eventlog/1","ev":{"Type":"message.delta","Seq":37,"Text":"old text"}}`}}).Err(); err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall(session.NewToolCallID(), "Read", []byte(`{}`))
	want := []session.Event{{Type: session.EvMessageDelta, Seq: 37, Text: "old text"}, {Type: session.EvToolCall, Seq: 90, ToolCall: &call}, {Type: session.EvToolResult, Seq: 91, ToolResult: &session.ToolResult{CallID: call.ID, Content: "new"}}}
	for _, ev := range want[1:] {
		if err := st.Append(ctx, id, ev); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
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
	mr := miniredis.RunT(t)
	st, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const id session.SessionID = "legacy-follow"
	after, err := st.AppendGap(t.Context(), id, "start")
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	if err := client.XAdd(t.Context(), &redis.XAddArgs{Stream: "mecatl:events:" + string(id), Values: map[string]any{"r": `{"v":"redisstore-eventlog/1","ev":{"Type":"tool.result","ToolResult":{"CallID":"provider"}}}`}}).Err(); err != nil {
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
