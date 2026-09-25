package redisstore

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestArtifactStorage_DeletionOutbox(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	st, err := NewWithConfig(Config{Addr: mr.Addr(), AllowPlaintext: true, ArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	saved := func(id session.SessionID) {
		t.Helper()
		if err := st.Save(ctx, session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	saved("direct")
	if err := st.Delete(ctx, "direct"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.testClient().ZScore(ctx, artifactDeletionOutboxKey, "direct").Result(); err != nil {
		t.Fatalf("direct delete missing durable outbox: %v", err)
	}
	saved("conditional")
	page, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var row port.SessionDiscoveryMeta
	for _, candidate := range page.Sessions {
		if candidate.ID == "conditional" {
			row = candidate
		}
	}
	if row.ID == "" {
		t.Fatal("conditional metadata missing")
	}
	deleted, err := st.DeleteSessionIfUnchanged(ctx, row)
	if err != nil || !deleted {
		t.Fatalf("conditional deletion = %v, %v", deleted, err)
	}
	if _, err := st.testClient().ZScore(ctx, artifactDeletionOutboxKey, "conditional").Result(); err != nil {
		t.Fatalf("conditional delete missing durable outbox: %v", err)
	}
	// A stale conditional delete must not enqueue cleanup for a live session.
	saved("stale")
	stale, err := st.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range stale.Sessions {
		if candidate.ID == "stale" {
			row = candidate
		}
	}
	sess, err := st.Load(ctx, "stale")
	if err != nil {
		t.Fatal(err)
	}
	sess.SetTitle("new title")
	if err := st.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	deleted, err = st.DeleteSessionIfUnchanged(ctx, row)
	if err != nil || deleted {
		t.Fatalf("stale deletion = %v, %v", deleted, err)
	}
	if _, err := st.testClient().ZScore(ctx, artifactDeletionOutboxKey, "stale").Result(); err == nil {
		t.Fatal("stale deletion enqueued cleanup")
	}
	// A malformed outbox must fail before removing the authoritative snapshot.
	if err := st.testClient().Del(ctx, artifactDeletionOutboxKey).Err(); err != nil {
		t.Fatal(err)
	}
	if err := st.testClient().Set(ctx, artifactDeletionOutboxKey, "wrong-type", 0).Err(); err != nil {
		t.Fatal(err)
	}
	saved("outbox-unavailable")
	if err := st.Delete(ctx, "outbox-unavailable"); err == nil {
		t.Fatal("delete succeeded with unusable outbox")
	}
	if _, err := st.Load(ctx, "outbox-unavailable"); err != nil {
		t.Fatalf("failed atomic delete removed snapshot: %v", err)
	}
}

func TestArtifactStorage_DisabledDeleteKeepsLegacyShape(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	st, err := New(mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.Save(ctx, session.New("legacy", session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(ctx, "legacy"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.testClient().Exists(ctx, artifactDeletionOutboxKey).Result(); err != nil || n != 0 {
		t.Fatalf("disabled delete created PDF outbox: exists=%d err=%v", n, err)
	}
}
