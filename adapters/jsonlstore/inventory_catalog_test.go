package jsonlstore

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionStorageContinuity_PageRebuildRetriesConcurrentMutation(t *testing.T) {
	ctx := context.Background()
	first, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New first Store: %v", err)
	}
	sess := session.New("concurrent", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "current"}, session.Limits{}, time.Unix(1_700_000_000, 0).UTC())
	if err := first.Save(ctx, sess); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := first.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1}); err != nil {
		t.Fatalf("prime inventory page: %v", err)
	}
	if err := os.Remove(first.inventoryCatalogPath()); err != nil {
		t.Fatalf("remove inventory catalog: %v", err)
	}
	second, err := New(first.resolver.dir)
	if err != nil {
		t.Fatalf("New second Store: %v", err)
	}

	rebuilt := make(chan struct{})
	release := make(chan struct{})
	first.inventoryCatalogReadyObserver = func() {
		first.inventoryCatalogReadyObserver = nil
		close(rebuilt)
		<-release
	}
	pageDone := make(chan error, 1)
	go func() {
		_, pageErr := first.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1})
		pageDone <- pageErr
	}()
	awaitSignal(t, rebuilt, "inventory catalog was not rebuilt")
	loaded, err := second.Load(ctx, sess.ID)
	if err != nil {
		t.Fatalf("load concurrent session: %v", err)
	}
	if err := loaded.RenameTitle("changed while page prepared"); err != nil {
		t.Fatalf("rename concurrent session: %v", err)
	}
	if err := second.Save(ctx, loaded); err != nil {
		t.Fatalf("save concurrent session: %v", err)
	}
	close(release)
	if err := awaitError(t, pageDone, "inventory page did not finish"); err != nil {
		t.Fatalf("PageSessionMetadata after concurrent mutation: %v", err)
	}
}
