package redisstore_test

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/adapters/redisstore"
	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacyInvocationStableAfterDurablePrefixPruning(t *testing.T) {
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
	var positions []string
	for _, raw := range []string{
		`{"Type":"user_prompt","UserPrompt":{"Text":"unrelated"}}`,
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
	inc := session.LegacyIncarnationID(id, 0, nil)
	read := func(st *redisstore.Store) eventcodec.Projection {
		t.Helper()
		var events []session.Event
		for ev, err := range st.Read(ctx, id) {
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, ev)
		}
		p, err := eventcodec.Project(events, inc)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	before := read(st)
	first, second := before.Events[1].ToolCall.ID, before.Events[3].ToolCall.ID
	if first == second || read(st).Events[3].ToolCall.ID != second {
		t.Fatal("reads before first save disagree or reuse collapsed")
	}
	folded, err := eventsource.Fold(eventsource.SessionMeta{ID: id, Incarnation: inc, Mode: session.ModeDefault, EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/fixture", Revision: "v1"}}, st.Read(ctx, id))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(ctx, folded); err != nil {
		t.Fatal(err)
	}
	reopened, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved, err := reopened.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Conversation.Messages[1].ToolCalls[0].ID != first || read(reopened).Events[3].ToolCall.ID != second {
		t.Fatal("save/restart changed invocation")
	}
	// Redis retains immutable stream positions when a prefix is removed.
	for _, removed := range [][]string{positions[:1], positions[1:3]} {
		if err := client.XDel(ctx, key, removed...).Err(); err != nil {
			t.Fatal(err)
		}
		got := read(reopened)
		lastCall := got.Events[len(got.Events)-2].ToolCall
		if lastCall.ID != second || got.Events[len(got.Events)-1].ToolResult.CallID != second || lastCall.ID == first {
			t.Fatal("prefix pruning renamed surviving durable invocation")
		}
		if got.Events[0].LegacyIdentity().Ordinal != -1 {
			t.Fatal("retained index mistaken for original evidence ordinal")
		}
	}
}
