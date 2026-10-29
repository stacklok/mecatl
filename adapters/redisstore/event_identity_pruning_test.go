package redisstore_test

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/adapters/redisstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacyOmissionAfterDurablePrefixPruning(t *testing.T) {
	ctx := t.Context()
	mr := miniredis.RunT(t)
	st, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	const id session.SessionID = "pruned-legacy-events"
	const key = "mecatl:events:" + string(id)
	after, err := st.AppendGap(ctx, id, "start")
	if err != nil {
		t.Fatal(err)
	}
	var positions []string
	for _, raw := range []string{
		`{"Type":"tool.call","ToolCall":{"ID":"reused-private","Name":"Read"}}`,
		`{"Type":"tool.result","ToolResult":{"CallID":"reused-private","Content":"first"}}`,
		`{"Type":"tool.call","ToolCall":{"ID":"reused-private","Name":"Read"}}`,
		`{"Type":"tool.result","ToolResult":{"CallID":"reused-private","Content":"second"}}`,
	} {
		pos, err := client.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: map[string]any{"r": `{"v":"redisstore-eventlog/1","ev":` + raw + `}`}}).Result()
		if err != nil {
			t.Fatal(err)
		}
		positions = append(positions, pos)
	}
	if err := client.XDel(ctx, key, positions[:2]...).Err(); err != nil {
		t.Fatal(err)
	}
	count := 0
	for rec, err := range st.ReadAfter(ctx, id, after, port.ReadOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		if rec.Kind != port.LogRecordGap || eventCursorPosition(t, rec.Cursor) != positions[count+2] {
			t.Fatalf("pruned gap: %+v", rec)
		}
		count++
	}
	if count != 2 {
		t.Fatalf("gap count %d", count)
	}
	for ev, err := range st.Read(ctx, id) {
		t.Fatalf("pruned old invocation escaped omission: %+v %v", ev, err)
	}
}
