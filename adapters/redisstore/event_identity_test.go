package redisstore_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/adapters/redisstore"
	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacyMixedEventIdentityReadRestartAndCursor(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	st, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const id session.SessionID = "legacy-events"
	gap, err := st.AppendGap(ctx, id, "fixture gap")
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	for _, record := range []string{
		`{"v":"redisstore-eventlog/1","ev":{"Type":"tool.call","Seq":30,"ToolCall":{"ID":"private-provider","Name":"Read","Args":{}}}}`,
		`{"v":"redisstore-eventlog/1","ev":{"Type":"tool.result","Seq":2,"ToolResult":{"CallID":"private-provider","Content":"old"}}}`,
	} {
		if _, err := client.XAdd(ctx, &redis.XAddArgs{Stream: "mecatl:events:" + string(id), Values: map[string]any{"r": record}}).Result(); err != nil {
			t.Fatal(err)
		}
	}
	read := func(st *redisstore.Store) []session.Event {
		t.Helper()
		var es []session.Event
		for e, err := range st.Read(ctx, id) {
			if err != nil {
				t.Fatal(err)
			}
			es = append(es, e)
		}
		return es
	}
	es := read(st)
	if len(es) != 2 || es[0].ToolCall != nil || es[1].ToolResult != nil {
		t.Fatal("old provider references escaped decoder")
	}
	inc := session.LegacyIncarnationID(id, 0, nil)
	p, err := eventcodec.Project(es, inc)
	if err != nil {
		t.Fatal(err)
	}
	again, err := eventcodec.Project(read(st), inc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, again) {
		t.Fatal("reads before save unstable")
	}
	var cursorEvents []session.Event
	for rec, err := range st.ReadAfter(ctx, id, gap, port.ReadOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		cursorEvents = append(cursorEvents, rec.Event)
	}
	if len(cursorEvents) != 2 {
		t.Fatal("cursor ordering/gap changed")
	}
	for i, e := range cursorEvents {
		if e.LegacyIdentity().Coordinate != es[i].LegacyIdentity().Coordinate || e.LegacyIdentity().Namespace != es[i].LegacyIdentity().Namespace {
			t.Fatal("read/cursor source coordinates differ")
		}
	}
	c := session.NewToolCall(session.NewToolCallID(), "Read", nil)
	for _, e := range []session.Event{{Type: session.EvToolCall, ToolCall: &c}, {Type: session.EvToolResult, ToolResult: &session.ToolResult{CallID: c.ID, Content: "new"}}} {
		if err := st.Append(ctx, id, e); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	mixed, err := eventcodec.Project(read(reopened), inc)
	if err != nil {
		t.Fatal(err)
	}
	if len(mixed.Events) != 4 || mixed.Events[0].ToolCall.ID != p.Events[0].ToolCall.ID || mixed.Events[2].ToolCall.ID != c.ID {
		t.Fatal("restart changed mixed semantics")
	}
}
