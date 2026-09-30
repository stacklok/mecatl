package consumer_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"

	"github.com/stacklok/mecatl/adapters/redisstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// The hook is deliberately installed AFTER both production constructors and
// writes: initialization can SETNX missing markers and is not a read-only API.
// EVAL/EVALSHA are permitted only because these read paths use a page script;
// miniredis cannot attest the script's effect or enforce real Redis ACLs.
func TestExternalConsumer_RedisReadCommandsAfterInitialization(t *testing.T) {
	broker := miniredis.RunT(t)
	writer, err := redisstore.New(broker.Addr())
	check(t, err)
	reader, err := redisstore.New(broker.Addr())
	check(t, err)
	t.Cleanup(func() { check(t, reader.Close()); check(t, writer.Close()) })
	ctx := t.Context()
	s := session.New("read", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "external", Revision: "1"}, session.Limits{}, time.Now())
	check(t, writer.Save(ctx, s))
	_, err = writer.AppendEvent(ctx, s.ID, session.Event{Type: session.EvMessageDelta, Text: "record", Seq: 1})
	check(t, err)
	var rejected atomic.Int32
	broker.Server().SetPreHook(func(peer *server.Peer, cmd string, _ ...string) bool {
		if broker.IsReadOnlyCommand(cmd) || strings.EqualFold(cmd, "evalsha") || strings.EqualFold(cmd, "eval") || strings.EqualFold(cmd, "ping") || strings.EqualFold(cmd, "client") {
			return false
		}
		rejected.Add(1)
		peer.WriteError("write rejected by read-command test: " + cmd)
		return true
	})
	got, err := reader.Load(ctx, s.ID)
	check(t, err)
	if got.ID != s.ID {
		t.Fatalf("read wrong session: %s", got.ID)
	}
	page, err := reader.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1})
	check(t, err)
	if len(page.Sessions) != 1 || page.Sessions[0].ID != s.ID {
		t.Fatalf("metadata read skipped row: %+v", page)
	}
	lineage, err := reader.ReadSessionLineage(ctx, port.SessionLineageQuery{RootID: s.ID, RootIncarnation: s.Incarnation(), Limit: 1})
	check(t, err)
	if len(lineage.Records) != 1 {
		t.Fatalf("lineage read: %+v", lineage)
	}
	count := 0
	for record, err := range reader.ReadAfter(ctx, s.ID, "", port.ReadOptions{}) {
		check(t, err)
		if record.Kind != port.LogRecordEvent {
			t.Fatalf("record: %+v", record)
		}
		count++
	}
	if count != 1 || rejected.Load() != 0 {
		t.Fatalf("read count=%d rejected writes=%d", count, rejected.Load())
	}
	// Canceling read paths must not leave a follower attached to the broker.
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	for _, err := range reader.ReadAfter(cancelCtx, s.ID, "", port.ReadOptions{Follow: true}) {
		if err != nil && err != context.Canceled {
			t.Fatal(err)
		}
	}
}
