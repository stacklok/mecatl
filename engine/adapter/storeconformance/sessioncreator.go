package storeconformance

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// RunSessionCreator executes the optional atomic first-publication contract.
// newSharedStores must return two handles backed by the same authoritative
// storage so the concurrent case exercises backend, not process-local,
// exclusion.
func RunSessionCreator(t *testing.T, newSharedStores func(t *testing.T) (port.SessionStore, port.SessionStore)) {
	t.Helper()
	ctx := context.Background()

	t.Run("create-load round trip", func(t *testing.T) {
		st, _ := newSharedStores(t)
		creator := sessionCreator(t, st)
		want := representativeSession(t, "create-roundtrip")
		if err := creator.Create(ctx, want); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := st.Load(ctx, want.ID)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		assertSessionEqual(t, got, want)
	})

	t.Run("save-existing snapshot collides", func(t *testing.T) {
		st, _ := newSharedStores(t)
		winner := representativeSession(t, "create-after-save")
		if err := st.Save(ctx, winner); err != nil {
			t.Fatalf("Save(winner): %v", err)
		}
		loser := newSession(winner.ID)
		loser.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/different", Revision: "in-tree-v1"}
		if err := sessionCreator(t, st).Create(ctx, loser); !errors.Is(err, port.ErrSessionAlreadyExists) {
			t.Fatalf("Create(after Save) = %v, want ErrSessionAlreadyExists", err)
		}
		got, err := st.Load(ctx, winner.ID)
		if err != nil {
			t.Fatalf("Load winner: %v", err)
		}
		assertSessionEqual(t, got, winner)
	})

	t.Run("same owner collision preserves winner", func(t *testing.T) {
		st, _ := newSharedStores(t)
		creator := sessionCreator(t, st)
		owner := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
		winner := representativeSession(t, "create-same-owner")
		mustOK(t, "RestoreLabels(winner)", winner.RestoreLabels(owner, session.Authority{}))
		if err := creator.Create(ctx, winner); err != nil {
			t.Fatalf("Create(winner): %v", err)
		}

		loser := newSession(winner.ID)
		loser.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/different", Revision: "in-tree-v1"}
		mustOK(t, "RestoreLabels(loser)", loser.RestoreLabels(owner, session.Authority{}))
		if err := creator.Create(ctx, loser); !errors.Is(err, port.ErrSessionAlreadyExists) {
			t.Fatalf("Create(collision) = %v, want ErrSessionAlreadyExists", err)
		}
		got, err := st.Load(ctx, winner.ID)
		if err != nil {
			t.Fatalf("Load winner: %v", err)
		}
		assertSessionEqual(t, got, winner)
		assertOwnerEqual(t, got.Owner, winner.Owner)
	})

	t.Run("cross owner collision preserves winner", func(t *testing.T) {
		st, _ := newSharedStores(t)
		creator := sessionCreator(t, st)
		winner := representativeSession(t, "create-cross-owner")
		mustOK(t, "RestoreLabels(alice)", winner.RestoreLabels(
			&session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}, session.Authority{}))
		if err := creator.Create(ctx, winner); err != nil {
			t.Fatalf("Create(winner): %v", err)
		}

		loser := newSession(winner.ID)
		mustOK(t, "RestoreLabels(bob)", loser.RestoreLabels(
			&session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser}, session.Authority{}))
		if err := creator.Create(ctx, loser); !errors.Is(err, port.ErrSessionAlreadyExists) {
			t.Fatalf("Create(foreign collision) = %v, want ErrSessionAlreadyExists", err)
		}
		got, err := st.Load(ctx, winner.ID)
		if err != nil {
			t.Fatalf("Load winner: %v", err)
		}
		assertSessionEqual(t, got, winner)
		assertOwnerEqual(t, got.Owner, winner.Owner)
	})

	t.Run("collision preserves derivative metadata generation", func(t *testing.T) {
		st, _ := newSharedStores(t)
		pager, ok := st.(port.SessionMetadataPager)
		if !ok || !port.SupportsSessionMetadataPaging(st) {
			t.Skipf("%T does not expose derivative session metadata", st)
		}
		creator := sessionCreator(t, st)
		winner := representativeSession(t, "create-metadata-winner")
		if err := creator.Create(ctx, winner); err != nil {
			t.Fatalf("Create(winner): %v", err)
		}
		if err := creator.Create(ctx, newSession("create-metadata-companion")); err != nil {
			t.Fatalf("Create(companion): %v", err)
		}
		before, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10})
		if err != nil {
			t.Fatalf("PageSessionMetadata(before): %v", err)
		}
		cursorPage, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1})
		if err != nil {
			t.Fatalf("PageSessionMetadata(cursor): %v", err)
		}
		if cursorPage.NextCursor == nil {
			t.Fatal("metadata page did not return a continuation cursor")
		}

		loser := newSession(winner.ID)
		loser.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/different", Revision: "in-tree-v1"}
		if err := creator.Create(ctx, loser); !errors.Is(err, port.ErrSessionAlreadyExists) {
			t.Fatalf("Create(collision) = %v, want ErrSessionAlreadyExists", err)
		}
		after, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 10})
		if err != nil {
			t.Fatalf("PageSessionMetadata(after): %v", err)
		}
		if !reflect.DeepEqual(after, before) {
			t.Errorf("metadata changed on collision:\n got %+v\nwant %+v", after, before)
		}
		if _, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{
			Limit: 1, Cursor: cursorPage.NextCursor,
		}); err != nil {
			t.Fatalf("pre-collision cursor invalidated: %v", err)
		}
	})

	t.Run("concurrent creators have one winner", func(t *testing.T) {
		first, second := newSharedStores(t)
		creators := []port.SessionCreator{sessionCreator(t, first), sessionCreator(t, second)}
		candidates := []*session.Session{newSession("create-concurrent"), newSession("create-concurrent")}
		candidates[0].EnvironmentRef = session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/first", Revision: "in-tree-v1"}
		candidates[1].EnvironmentRef = session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/second", Revision: "in-tree-v1"}
		start := make(chan struct{})
		results := make(chan struct {
			index int
			err   error
		}, len(creators))
		for i := range creators {
			go func() {
				<-start
				results <- struct {
					index int
					err   error
				}{index: i, err: creators[i].Create(ctx, candidates[i])}
			}()
		}
		close(start)

		winner := -1
		collisions := 0
		for range creators {
			result := <-results
			switch {
			case result.err == nil:
				if winner != -1 {
					t.Fatalf("multiple successful creators: %d and %d", winner, result.index)
				}
				winner = result.index
			case errors.Is(result.err, port.ErrSessionAlreadyExists):
				collisions++
			default:
				t.Fatalf("Create[%d] = %v", result.index, result.err)
			}
		}
		if winner == -1 || collisions != 1 {
			t.Fatalf("winner=%d collisions=%d, want one of each", winner, collisions)
		}
		got, err := first.Load(ctx, candidates[winner].ID)
		if err != nil {
			t.Fatalf("Load winner: %v", err)
		}
		assertSessionEqual(t, got, candidates[winner])
	})

	t.Run("create isolates input and loaded copies", func(t *testing.T) {
		st, _ := newSharedStores(t)
		creator := sessionCreator(t, st)
		input := representativeSession(t, "create-isolation")
		if err := creator.Create(ctx, input); err != nil {
			t.Fatalf("Create: %v", err)
		}
		baseline, err := st.Load(ctx, input.ID)
		if err != nil {
			t.Fatalf("Load baseline: %v", err)
		}
		input.ModelID = "mutated-input"
		baseline.ModelID = "mutated-load"
		got, err := st.Load(ctx, input.ID)
		if err != nil {
			t.Fatalf("Load after mutations: %v", err)
		}
		if got.ModelID == "mutated-input" || got.ModelID == "mutated-load" {
			t.Fatalf("stored session aliased caller data: ModelID=%q", got.ModelID)
		}
	})

	t.Run("nil session is rejected", func(t *testing.T) {
		st, _ := newSharedStores(t)
		if err := sessionCreator(t, st).Create(ctx, nil); err == nil {
			t.Fatal("Create(nil) succeeded")
		}
	})
}

func sessionCreator(t *testing.T, st port.SessionStore) port.SessionCreator {
	t.Helper()
	creator, ok := st.(port.SessionCreator)
	if !ok || !port.SupportsSessionCreate(st) {
		t.Fatalf("%T does not implement port.SessionCreator", st)
	}
	return creator
}

func assertOwnerEqual(t *testing.T, got, want *session.Principal) {
	t.Helper()
	if got == nil || want == nil {
		if got != nil || want != nil {
			t.Errorf("Owner = %+v want %+v", got, want)
		}
		return
	}
	if *got != *want {
		t.Errorf("Owner = %+v want %+v", *got, *want)
	}
}
