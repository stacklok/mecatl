package redisstore_test

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

func TestCurrentRedisListLogIsRejectedUntouched(t *testing.T) {
	mr := miniredis.RunT(t)
	const id session.SessionID = "old-list"
	key := "mecatl:events:" + string(id)
	before := []string{`{"v":"eventlog-json/1","ev":{"type":"message.delta","text":"one"}}`}
	mr.RPush(key, before...)

	st, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var readErr error
	for _, err := range st.Read(context.Background(), id) {
		readErr = err
		break
	}
	if readErr == nil {
		t.Fatal("Read accepted a LIST at the current event-log key")
	}
	if _, err := st.AppendEvent(context.Background(), id, session.Event{Type: session.EvResult}); err == nil {
		t.Fatal("AppendEvent accepted a LIST at the current event-log key")
	}
	got, listErr := mr.List(key)
	if listErr != nil || len(got) != len(before) || got[0] != before[0] || mr.Type(key) != "list" {
		t.Fatalf("rejected current LIST changed: values=%q type=%q err=%v", got, mr.Type(key), listErr)
	}
	if mr.Exists("mecatl:events-gen:" + string(id)) {
		t.Fatal("rejected current LIST created an event generation")
	}
}
