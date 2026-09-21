package redisstore_test

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

func TestOldRedisListLogIsIgnoredUntouched(t *testing.T) {
	mr := miniredis.RunT(t)
	const id session.SessionID = "old-list"
	oldKey := "mecatl:events:" + string(id)
	before := []string{`{"v":"eventlog-json/1","ev":{"type":"message.delta","text":"one"}}`}
	mr.RPush(oldKey, before...)

	st, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	for range st.Read(context.Background(), id) {
		t.Fatal("old namespace event became visible through Read")
	}
	for range st.ReadAfter(context.Background(), id, "", port.ReadOptions{}) {
		t.Fatal("old namespace event became visible through ReadAfter")
	}
	if _, err := st.AppendEvent(context.Background(), id, session.Event{Type: session.EvResult}); err != nil {
		t.Fatalf("AppendEvent current namespace: %v", err)
	}
	got, listErr := mr.List(oldKey)
	if listErr != nil || len(got) != len(before) || got[0] != before[0] || mr.Type(oldKey) != "list" {
		t.Fatalf("current append changed old LIST: values=%q type=%q err=%v", got, mr.Type(oldKey), listErr)
	}
	currentKey := "mecatl:store:v2:events:" + string(id)
	if mr.Type(currentKey) != "stream" || !mr.Exists("mecatl:store:v2:events-gen:"+string(id)) {
		t.Fatalf("current event namespace missing: type=%q", mr.Type(currentKey))
	}
}
