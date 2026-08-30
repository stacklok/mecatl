// Package storeconformance provides a shared conformance test suite for the
// port.SessionStore interface. Adapters (the in-memory default, the JSONL
// replay store, remote drivers, ...) call Run with a factory that constructs
// a fresh store, and the suite exercises only the port.SessionStore
// interface against sessions built through the session aggregate's public
// API.
//
// Importing "testing" in a non-_test.go file is intentional here: this is a
// test-helper package whose sole purpose is to be imported by adapter tests,
// the conventional Go pattern for shared conformance suites (cf.
// testing/fstest and the sibling fsconformance/memconformance packages).
//
// The suite pins the CONTRACT, not the implementation: snapshot encoding,
// durability across reopen, and file layout are adapter-internal and
// deliberately NOT asserted here. There is no separate self-test — the
// memstore run site IS the in-engine validation (memstore is the reference
// in-memory store; adding a second in-memory fake would only duplicate it).
package storeconformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Run executes the shared SessionStore conformance table against the store
// produced by newStore. newStore must return a fresh, isolated store each
// call.
func Run(t *testing.T, newStore func(t *testing.T) port.SessionStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("save-load round trip", func(t *testing.T) {
		st := newStore(t)
		want := representativeSession(t, "conf-roundtrip")
		if err := st.Save(ctx, want); err != nil {
			t.Fatalf("Save: %v", err)
		}
		got, err := st.Load(ctx, want.ID)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		assertSessionEqual(t, got, want)
	})

	t.Run("kind relationship round trip", func(t *testing.T) {
		cases := []struct {
			id   session.SessionID
			kind session.SessionKind
			rel  session.SessionRelationship
		}{
			{id: "conf-kind-main", kind: session.SessionKindMain},
			{id: "conf-kind-scheduled", kind: session.SessionKindScheduled, rel: session.SessionRelationship{ScheduleName: "nightly", OriginSessionID: "origin"}},
			{id: "conf-kind-subagent", kind: session.SessionKindSubagent, rel: session.SessionRelationship{ParentSessionID: "parent", CallID: "call-sub"}},
			{id: "conf-kind-parallel", kind: session.SessionKindParallelBranch, rel: session.SessionRelationship{ParentSessionID: "parent", CallID: "call-par", BranchIndex: intPointer(2)}},
			{id: "conf-kind-team", kind: session.SessionKindTeamMember, rel: session.SessionRelationship{TeamID: "team", MemberName: "reviewer", ParentSessionID: "parent"}},
			{id: "conf-kind-debug", kind: session.SessionKindDebug, rel: session.SessionRelationship{DebugTargetID: "target"}},
		}
		for _, tc := range cases {
			t.Run(string(tc.kind), func(t *testing.T) {
				st := newStore(t)
				want := newSession(tc.id)
				if err := want.RestoreSessionMetadata(tc.kind, tc.rel); err != nil {
					t.Fatalf("RestoreSessionMetadata: %v", err)
				}
				got := roundTrip(t, st, want)
				if got.Kind != tc.kind || !reflect.DeepEqual(got.Relationship, tc.rel) {
					t.Errorf("metadata = (%q, %+v), want (%q, %+v)", got.Kind, got.Relationship, tc.kind, tc.rel)
				}
			})
		}
	})

	t.Run("save isolates relationship branch index", func(t *testing.T) {
		st := newStore(t)
		want := newSession("conf-kind-isolated")
		if err := want.RestoreSessionMetadata(session.SessionKindParallelBranch, session.SessionRelationship{
			ParentSessionID: "parent", CallID: "call", BranchIndex: intPointer(2),
		}); err != nil {
			t.Fatalf("RestoreSessionMetadata: %v", err)
		}
		if err := st.Save(ctx, want); err != nil {
			t.Fatalf("Save: %v", err)
		}
		*want.Relationship.BranchIndex = 3
		got, err := st.Load(ctx, want.ID)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Relationship.BranchIndex == nil || *got.Relationship.BranchIndex != 2 {
			t.Errorf("loaded BranchIndex = %v, want 2", got.Relationship.BranchIndex)
		}
	})

	t.Run("lifecycle-state fidelity", func(t *testing.T) {
		t.Run("idle", func(t *testing.T) {
			st := newStore(t)
			s := newSession("conf-idle")
			saveLoad := roundTrip(t, st, s)
			if saveLoad.State != session.StateIdle {
				t.Errorf("State = %q want %q", saveLoad.State, session.StateIdle)
			}
		})
		t.Run("completed with non-default stop", func(t *testing.T) {
			st := newStore(t)
			s := newSession("conf-completed")
			mustOK(t, "BeginTurn", s.BeginTurn())
			mustOK(t, "Stop", s.Stop(session.StopBudget))
			got := roundTrip(t, st, s)
			if got.State != session.StateCompleted {
				t.Errorf("State = %q want %q", got.State, session.StateCompleted)
			}
			reason, ok := got.RecordedStopReason()
			if !ok || reason != session.StopBudget {
				t.Errorf("RecordedStopReason = (%q, %v) want (%q, true)", reason, ok, session.StopBudget)
			}
		})
		t.Run("awaiting with pending ask", func(t *testing.T) {
			st := newStore(t)
			s := newSession("conf-awaiting")
			mustOK(t, "BeginTurn", s.BeginTurn())
			ask := session.PendingAsk{
				AskID:  "ask-1",
				Tool:   "Bash",
				Args:   json.RawMessage(`{"command":"true"}`),
				Reason: "mutating command",
			}
			mustOK(t, "PauseForApproval", s.PauseForApproval(ask))
			got := roundTrip(t, st, s)
			if got.State != session.StateAwaiting {
				t.Fatalf("State = %q want %q", got.State, session.StateAwaiting)
			}
			gotAsk, ok := got.PendingAsk()
			if !ok {
				t.Fatal("PendingAsk() not present after Load")
			}
			if !reflect.DeepEqual(gotAsk, ask) {
				t.Errorf("PendingAsk = %+v want %+v", gotAsk, ask)
			}
		})
		t.Run("cancelled", func(t *testing.T) {
			st := newStore(t)
			s := newSession("conf-cancelled")
			mustOK(t, "BeginTurn", s.BeginTurn())
			mustOK(t, "Cancel", s.Cancel())
			got := roundTrip(t, st, s)
			if got.State != session.StateCancelled {
				t.Errorf("State = %q want %q", got.State, session.StateCancelled)
			}
		})
	})

	t.Run("large snapshot (multi-megabyte media part)", func(t *testing.T) {
		// The size-contract subtest: a realistic screenshot-sized inline media
		// part (~5 MiB, well under session.MaxMediaBytes) must round-trip. A
		// LOCAL store passes trivially; a WIRE-backed store fails here unless
		// its transport accepts snapshot-sized messages (default gRPC limits
		// cap at 4 MiB — see grpcdriver.MaxSnapshotBytes).
		st := newStore(t)
		s := newSession("conf-large")
		data := make([]byte, 5<<20)
		for i := range data {
			data[i] = byte(i) // non-uniform so the payload is honest, not degenerate
		}
		img, err := session.NewImageContent("image/png", data)
		if err != nil {
			t.Fatalf("NewImageContent: %v", err)
		}
		mustOK(t, "RecordUserPromptWithParts", s.RecordUserPromptWithParts("big screenshot", []session.Content{img}, nil))
		got := roundTrip(t, st, s)
		msgs := got.Conversation.Messages
		if len(msgs) != 1 || len(msgs[0].Parts) != 1 {
			t.Fatalf("loaded %d messages (want 1, with 1 media part)", len(msgs))
		}
		if gotData := msgs[0].Parts[0].Data; !bytes.Equal(gotData, data) {
			t.Errorf("loaded media part is %d bytes and/or differs from the saved part (%d bytes)", len(gotData), len(data))
		}
	})

	t.Run("two sessions under distinct ids", func(t *testing.T) {
		// Kills a single-slot driver: the store must key by session id, not
		// hold "the most recent" session.
		st := newStore(t)
		a := newSession("conf-multi-a")
		mustOK(t, "RecordUserPrompt(a)", a.RecordUserPrompt("message for a", nil))
		b := newSession("conf-multi-b")
		mustOK(t, "RecordUserPrompt(b)", b.RecordUserPrompt("message for b", nil))
		if err := st.Save(ctx, a); err != nil {
			t.Fatalf("Save(a): %v", err)
		}
		if err := st.Save(ctx, b); err != nil {
			t.Fatalf("Save(b): %v", err)
		}
		gotA, err := st.Load(ctx, a.ID)
		if err != nil {
			t.Fatalf("Load(a): %v", err)
		}
		gotB, err := st.Load(ctx, b.ID)
		if err != nil {
			t.Fatalf("Load(b): %v", err)
		}
		if gotA.ID != a.ID || len(gotA.Conversation.Messages) != 1 || gotA.Conversation.Messages[0].Text != "message for a" {
			t.Errorf("Load(a) returned id=%q messages=%+v, want a's own snapshot", gotA.ID, gotA.Conversation.Messages)
		}
		if gotB.ID != b.ID || len(gotB.Conversation.Messages) != 1 || gotB.Conversation.Messages[0].Text != "message for b" {
			t.Errorf("Load(b) returned id=%q messages=%+v, want b's own snapshot", gotB.ID, gotB.Conversation.Messages)
		}
	})

	t.Run("long and opaque session ids", func(t *testing.T) {
		// session.SessionID is an OPAQUE string with no documented length or
		// charset bound, and an embedding host may supply a namespaced compound
		// id through CreateSession. So every backend must cope with a long id,
		// whatever it does to turn one into a physical key.
		//
		// This is stated at the port rather than per-adapter because it is a
		// property of the contract, and because a backend that encodes the id
		// INTO a filename can silently acquire a ceiling: jsonlstore briefly
		// base64'd whole ids into filenames, which inflates 4/3 with no cap and
		// capped ids at 175 bytes — past that every call returned ENAMETOOLONG,
		// a pre-existing session became permanently unwritable, and a long
		// provider-supplied child id was never persisted at all. An in-memory
		// codec test could not see it; only a real Save/Load could.
		for _, n := range []int{175, 176, 241, 1024} {
			t.Run(strconv.Itoa(n), func(t *testing.T) {
				st := newStore(t)
				id := session.SessionID(strings.Repeat("L", n))
				s := newSession(id)
				mustOK(t, "RecordUserPrompt", s.RecordUserPrompt("long-id message", nil))
				if err := st.Save(ctx, s); err != nil {
					t.Fatalf("Save with a %d-byte id: %v", n, err)
				}
				got, err := st.Load(ctx, id)
				if err != nil {
					t.Fatalf("Load with a %d-byte id: %v", n, err)
				}
				if got.ID != id {
					t.Errorf("Load returned id of %d bytes, want the %d-byte id back byte-exact", len(got.ID), n)
				}
				if err := st.Save(ctx, got); err != nil {
					t.Fatalf("second Save with a %d-byte id: %v", n, err)
				}
			})
		}

		t.Run("long ids differing only in the tail do not alias", func(t *testing.T) {
			// A bounded encoding must not become lossy: truncating a long id to
			// fit a key would collapse these two onto one session.
			st := newStore(t)
			base := strings.Repeat("P", 200)
			first, second := session.SessionID(base+"-one"), session.SessionID(base+"-two")
			a, b := newSession(first), newSession(second)
			mustOK(t, "RecordUserPrompt(first)", a.RecordUserPrompt("belongs to first", nil))
			mustOK(t, "RecordUserPrompt(second)", b.RecordUserPrompt("belongs to second", nil))
			if err := st.Save(ctx, a); err != nil {
				t.Fatalf("Save(first): %v", err)
			}
			if err := st.Save(ctx, b); err != nil {
				t.Fatalf("Save(second): %v", err)
			}
			gotA, err := st.Load(ctx, first)
			if err != nil {
				t.Fatalf("Load(first): %v", err)
			}
			if gotA.ID != first || gotA.Conversation.Messages[0].Text != "belongs to first" {
				t.Errorf("Load(first) returned %q / %q; the two long ids aliased onto one session",
					gotA.ID, gotA.Conversation.Messages[0].Text)
			}
		})
	})

	t.Run("load miss wraps sentinel", func(t *testing.T) {
		st := newStore(t)
		const id = "conf-no-such-session"
		_, err := st.Load(ctx, id)
		if err == nil {
			t.Fatal("Load(missing id) = nil error, want not-found")
		}
		if !errors.Is(err, port.ErrSessionNotFound) {
			t.Errorf("Load(missing id) error = %v, want errors.Is(_, port.ErrSessionNotFound)", err)
		}
		if !strings.Contains(err.Error(), id) {
			t.Errorf("Load(missing id) error %q does not name the id %q", err, id)
		}
	})

	t.Run("overwrite", func(t *testing.T) {
		st := newStore(t)
		s := newSession("conf-overwrite")
		if err := st.Save(ctx, s); err != nil {
			t.Fatalf("Save #1: %v", err)
		}
		mustOK(t, "RecordUserPrompt", s.RecordUserPrompt("second save", nil))
		if err := st.Save(ctx, s); err != nil {
			t.Fatalf("Save #2: %v", err)
		}
		got, err := st.Load(ctx, s.ID)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		msgs := got.Conversation.Messages
		if len(msgs) != 1 || msgs[0].Text != "second save" {
			t.Errorf("Load after overwrite = %d messages %+v, want the single latest message", len(msgs), msgs)
		}
	})

	t.Run("nil-session save errors", func(t *testing.T) {
		st := newStore(t)
		if err := st.Save(ctx, nil); err == nil {
			t.Error("Save(nil) = nil error, want rejection")
		}
	})

	t.Run("isolation", func(t *testing.T) {
		st := newStore(t)
		s := newSession("conf-isolation")
		branchIndex := 2
		mustOK(t, "RestoreSessionMetadata", s.RestoreSessionMetadata(session.SessionKindParallelBranch, session.SessionRelationship{
			ParentSessionID: "parent",
			CallID:          "call",
			BranchIndex:     &branchIndex,
		}))
		mustOK(t, "RecordUserPrompt", s.RecordUserPrompt("original", nil))
		if err := st.Save(ctx, s); err != nil {
			t.Fatalf("Save: %v", err)
		}
		// Mutate the ORIGINAL after Save; the store must have captured the
		// state AT Save time (marshal/copy on Save), not retained the caller's
		// pointer.
		*s.Relationship.BranchIndex = 7
		mustOK(t, "RecordUserPrompt(original mutation)", s.RecordUserPrompt("mutation on the original", nil))
		loaded, err := st.Load(ctx, s.ID)
		if err != nil {
			t.Fatalf("Load #1: %v", err)
		}
		if got := len(loaded.Conversation.Messages); got != 1 {
			t.Errorf("Load saw %d messages, want 1 (the original's post-Save mutation must not reach the store)", got)
		}
		if got := *loaded.Relationship.BranchIndex; got != 2 {
			t.Errorf("Load branch index = %d, want 2 (relationship must not alias the original)", got)
		}
		// Mutate the LOADED copy through the aggregate; the stored state must
		// not alias it either.
		mustOK(t, "RecordUserPrompt(loaded mutation)", loaded.RecordUserPrompt("mutation on the loaded copy", nil))
		again, err := st.Load(ctx, s.ID)
		if err != nil {
			t.Fatalf("Load #2: %v", err)
		}
		if got := len(again.Conversation.Messages); got != 1 {
			t.Errorf("re-Load saw %d messages, want 1 (the loaded copy's mutation must not reach the store)", got)
		}
	})
}

// RunPrunable executes the shared port.PrunableStore conformance table
// against the store produced by newStore (which must also implement
// port.PrunableStore — the suite fails fast otherwise). Like Run, it pins the
// MECHANISM only: List returns what was saved with sane ModifiedAt values,
// Delete removes and is idempotent. Retention POLICY (which ids are prunable,
// age thresholds, per-family caps) is the caller's business and is
// deliberately NOT asserted here.
func RunPrunable(t *testing.T, newStore func(t *testing.T) port.SessionStore) {
	t.Helper()
	ctx := context.Background()

	prunable := func(t *testing.T) (port.SessionStore, port.PrunableStore) {
		t.Helper()
		st := newStore(t)
		p, ok := st.(port.PrunableStore)
		if !ok {
			t.Fatalf("store %T does not implement port.PrunableStore", st)
		}
		return st, p
	}

	t.Run("list returns saved ids across families", func(t *testing.T) {
		st, p := prunable(t)
		// One id per delegation-family prefix plus an unprefixed main id: List
		// is an UNFILTERED inventory, so all four must appear (prefix policy is
		// the caller's, never the store's).
		ids := []session.SessionID{
			"subagent-conf-1", "parallel-conf-1-0", "team-conf-1-lead", "conf-main-1",
		}
		before := time.Now()
		for _, id := range ids {
			if err := st.Save(ctx, newSession(id)); err != nil {
				t.Fatalf("Save(%q): %v", id, err)
			}
		}
		entries, err := p.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		byID := make(map[session.SessionID]port.StoredSession, len(entries))
		for _, e := range entries {
			byID[e.ID] = e
		}
		for _, id := range ids {
			e, ok := byID[id]
			if !ok {
				t.Errorf("List is missing saved id %q (got %d entries)", id, len(entries))
				continue
			}
			// Sane ModifiedAt: non-zero and not wildly outside the save window.
			// (File mtimes may have coarse granularity, so allow a small slack
			// before `before` rather than demanding exact ordering.)
			if e.ModifiedAt.IsZero() {
				t.Errorf("List(%q).ModifiedAt is the zero time", id)
			} else if e.ModifiedAt.Before(before.Add(-time.Minute)) || e.ModifiedAt.After(time.Now().Add(time.Minute)) {
				t.Errorf("List(%q).ModifiedAt = %v, want within a minute of the save window [%v, now]", id, e.ModifiedAt, before)
			}
		}
	})

	t.Run("modified-at tracks save order", func(t *testing.T) {
		st, p := prunable(t)
		early := newSession("conf-prune-early")
		late := newSession("conf-prune-late")
		if err := st.Save(ctx, early); err != nil {
			t.Fatalf("Save(early): %v", err)
		}
		if err := st.Save(ctx, late); err != nil {
			t.Fatalf("Save(late): %v", err)
		}
		entries, err := p.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		var earlyAt, lateAt time.Time
		for _, e := range entries {
			switch e.ID {
			case early.ID:
				earlyAt = e.ModifiedAt
			case late.ID:
				lateAt = e.ModifiedAt
			}
		}
		// Monotone non-decreasing is the contract (equal is fine: file mtimes
		// and coarse clocks legitimately collide within one tick).
		if lateAt.Before(earlyAt) {
			t.Errorf("later save's ModifiedAt %v is before the earlier save's %v", lateAt, earlyAt)
		}
	})

	t.Run("modified-at is stable across reads", func(t *testing.T) {
		// Kills an adapter that stamps ModifiedAt at LIST time (e.g. Now() per
		// call) instead of recording the SAVE time: such a store would report
		// every snapshot as perpetually fresh and neuter any age-based
		// retention built on the seam. Two Lists with no intervening Save must
		// agree exactly.
		st, p := prunable(t)
		s := newSession("conf-prune-stable")
		if err := st.Save(ctx, s); err != nil {
			t.Fatalf("Save: %v", err)
		}
		readAt := func(call string) time.Time {
			entries, err := p.List(ctx)
			if err != nil {
				t.Fatalf("List (%s): %v", call, err)
			}
			for _, e := range entries {
				if e.ID == s.ID {
					return e.ModifiedAt
				}
			}
			t.Fatalf("List (%s) is missing the saved id %q", call, s.ID)
			return time.Time{}
		}
		first := readAt("first")
		time.Sleep(10 * time.Millisecond) // let a Now()-stamping bug actually drift
		second := readAt("second")
		if !second.Equal(first) {
			t.Errorf("ModifiedAt changed between Lists with no Save: %v then %v (List must report the SAVE time, not the read time)", first, second)
		}
	})

	t.Run("delete removes from list and load", func(t *testing.T) {
		st, p := prunable(t)
		keep := newSession("conf-prune-keep")
		drop := newSession("conf-prune-drop")
		for _, s := range []*session.Session{keep, drop} {
			if err := st.Save(ctx, s); err != nil {
				t.Fatalf("Save(%q): %v", s.ID, err)
			}
		}
		if err := p.Delete(ctx, drop.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := st.Load(ctx, drop.ID); !errors.Is(err, port.ErrSessionNotFound) {
			t.Errorf("Load(deleted id) error = %v, want errors.Is(_, port.ErrSessionNotFound)", err)
		}
		entries, err := p.List(ctx)
		if err != nil {
			t.Fatalf("List after Delete: %v", err)
		}
		var sawKeep bool
		for _, e := range entries {
			if e.ID == drop.ID {
				t.Errorf("List still contains the deleted id %q", drop.ID)
			}
			if e.ID == keep.ID {
				sawKeep = true
			}
		}
		if !sawKeep {
			t.Errorf("List lost the UNdeleted id %q", keep.ID)
		}
		// The undeleted sibling must remain loadable (Delete is per-id).
		if _, err := st.Load(ctx, keep.ID); err != nil {
			t.Errorf("Load(kept id) after a sibling Delete: %v", err)
		}
	})

	t.Run("delete is idempotent on an unknown id", func(t *testing.T) {
		_, p := prunable(t)
		if err := p.Delete(ctx, "conf-prune-never-saved"); err != nil {
			t.Errorf("Delete(unknown id) = %v, want nil (idempotent)", err)
		}
	})
}

// RunMetadataPager executes the shared optional metadata-pager contract against
// every store that advertises port.SessionMetadataPager.
func RunMetadataPager(t *testing.T, newStore func(t *testing.T) port.SessionStore) {
	t.Helper()
	ctx := context.Background()
	st := newStore(t)
	pager, ok := st.(port.SessionMetadataPager)
	if !ok {
		t.Fatalf("store %T does not implement port.SessionMetadataPager", st)
	}
	alice := &session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
	bob := &session.Principal{Issuer: "https://issuer.example", Subject: "bob"}
	for _, fixture := range []struct {
		id    session.SessionID
		owner *session.Principal
	}{
		{id: "b", owner: alice},
		{id: "a", owner: alice},
		{id: "foreign", owner: bob},
		{id: "ownerless"},
	} {
		s := newSession(fixture.id)
		if err := s.RestoreLabels(fixture.owner, session.Authority{}); err != nil {
			t.Fatalf("RestoreLabels(%q): %v", fixture.id, err)
		}
		if err := st.Save(ctx, s); err != nil {
			t.Fatalf("Save(%q): %v", fixture.id, err)
		}
	}

	first, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{
		Limit: 1, OwnershipEnforced: true, Owner: alice,
	})
	if err != nil {
		t.Fatalf("PageSessionMetadata(first): %v", err)
	}
	if len(first.Sessions) != 1 || first.TotalCount != 2 || first.NextCursor == nil {
		t.Fatalf("first page = %+v, want one of two owned rows plus a cursor", first)
	}
	second, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{
		Limit: 1, OwnershipEnforced: true, Owner: alice, Cursor: first.NextCursor,
	})
	if err != nil {
		t.Fatalf("PageSessionMetadata(second): %v", err)
	}
	if len(second.Sessions) != 1 || second.TotalCount != 2 || second.NextCursor != nil {
		t.Fatalf("second page = %+v, want final owned row", second)
	}
	if first.Sessions[0].ID == second.Sessions[0].ID {
		t.Fatalf("cursor repeated %q", first.Sessions[0].ID)
	}
	if first.Sessions[0].ModifiedAt.Equal(second.Sessions[0].ModifiedAt) && first.Sessions[0].ID > second.Sessions[0].ID {
		t.Fatalf("equal-time IDs ordered %q then %q, want ascending", first.Sessions[0].ID, second.Sessions[0].ID)
	}
	if _, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{
		Limit: 1, OwnershipEnforced: true, Owner: bob, Cursor: first.NextCursor,
	}); !errors.Is(err, port.ErrSessionMetadataCursorRestart) {
		t.Fatalf("owner-mismatched cursor error = %v, want restart", err)
	}
	foreignCursor := *first.NextCursor
	foreignCursor.Continuation = "foreign-pager-continuation"
	if _, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{
		Limit: 1, OwnershipEnforced: true, Owner: alice, Cursor: &foreignCursor,
	}); !errors.Is(err, port.ErrSessionMetadataCursorRestart) {
		t.Fatalf("foreign continuation error = %v, want restart", err)
	}
	changed := newSession("generation-change")
	if err := changed.RestoreLabels(alice, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels(generation-change): %v", err)
	}
	if err := st.Save(ctx, changed); err != nil {
		t.Fatalf("Save(generation-change): %v", err)
	}
	if _, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{
		Limit: 1, OwnershipEnforced: true, Owner: alice, Cursor: first.NextCursor,
	}); !errors.Is(err, port.ErrSessionMetadataCursorRestart) {
		t.Fatalf("generation-stale cursor error = %v, want restart", err)
	}
	for _, page := range []port.SessionMetadataPage{first, second} {
		for _, row := range page.Sessions {
			if row.ID == "foreign" || row.ID == "ownerless" {
				t.Fatalf("ownership filtering happened after paging: leaked %q", row.ID)
			}
			if row.EstimatedBytes <= 0 {
				t.Fatalf("discovery metadata for %q omitted a positive byte estimate", row.ID)
			}
			if row.Workspace != "/work/space" || row.Kind != session.SessionKindMain {
				t.Fatalf("discovery metadata for %q = workspace %q kind %q", row.ID, row.Workspace, row.Kind)
			}
		}
	}
}

// RunConditionalPrunable executes the atomic metadata-revalidation cleanup
// contract against stores that advertise port.ConditionalPrunableStore.
func RunConditionalPrunable(t *testing.T, newStore func(t *testing.T) port.SessionStore) {
	t.Helper()
	ctx := context.Background()
	st := newStore(t)
	pager, ok := st.(port.SessionMetadataPager)
	if !ok {
		t.Fatalf("store %T does not implement port.SessionMetadataPager", st)
	}
	deleter, ok := st.(port.ConditionalPrunableStore)
	if !ok {
		t.Fatalf("store %T does not implement port.ConditionalPrunableStore", st)
	}
	owner := &session.Principal{Issuer: "https://issuer.example", Subject: "cleanup-owner"}
	s := newSession("conditional-delete")
	if err := s.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	page, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1, OwnershipEnforced: true, Owner: owner})
	if err != nil || len(page.Sessions) != 1 {
		t.Fatalf("PageSessionMetadata = %+v, %v", page, err)
	}
	stale := page.Sessions[0]
	if err := s.RecordUserPrompt("changed", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save changed: %v", err)
	}
	deleted, err := deleter.DeleteSessionIfUnchanged(ctx, stale)
	if err != nil || deleted {
		t.Fatalf("stale conditional delete = %v, %v; want false, nil", deleted, err)
	}
	page, err = pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 1, OwnershipEnforced: true, Owner: owner})
	if err != nil || len(page.Sessions) != 1 {
		t.Fatalf("PageSessionMetadata changed = %+v, %v", page, err)
	}
	deleted, err = deleter.DeleteSessionIfUnchanged(ctx, page.Sessions[0])
	if err != nil || !deleted {
		t.Fatalf("current conditional delete = %v, %v; want true, nil", deleted, err)
	}
	if _, err := st.Load(ctx, s.ID); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("Load after conditional delete = %v, want not found", err)
	}
}

// newSession constructs an idle session with non-default limits, workspace,
// mode and a fixed (whole-nanosecond, UTC) creation time so timestamp
// round-trip equality is well-defined.
func newSession(id session.SessionID) *session.Session {
	return session.New(
		id,
		session.ModeAccept,
		"/work/space",
		session.Limits{MaxTurns: 7, MaxToolCalls: 21, MaxConsecutiveFailures: 3},
		time.Date(2026, 6, 1, 12, 30, 45, 123456789, time.UTC),
	)
}

// representativeSession builds a session exercising every history shape
// through the PUBLIC aggregate API: a user prompt, a media-parts message, an
// assistant message carrying a reasoning blob plus tool calls, and the paired
// tool results. It is left running (mid-turn), so counters are non-zero.
func representativeSession(t *testing.T, id session.SessionID) *session.Session {
	t.Helper()
	s := newSession(id)
	// Inert creation labels (opaque to the domain) — every store driver must
	// round-trip them so a restarted process rebuilds the same engine.
	s.Profile = "no-fs"
	s.ProviderID = "openrouter"
	s.ModelID = "anthropic/claude-3.5-sonnet"
	s.ReasoningEffort = "high"
	mustOK(t, "RecordUserPrompt", s.RecordUserPrompt("please inspect the repo", []session.Message{
		session.NewSystemMessage("project instructions: be concise"),
	}))
	img, err := session.NewImageContent("image/png", []byte{0x89, 0x50, 0x4e, 0x47})
	if err != nil {
		t.Fatalf("NewImageContent: %v", err)
	}
	mustOK(t, "RecordUserPromptWithParts", s.RecordUserPromptWithParts("and this screenshot", []session.Content{img}, nil))
	mustOK(t, "BeginTurn", s.BeginTurn())
	// Record cumulative token usage while running so the snapshot carries a non-zero
	// budget the store must preserve (the MaxRunTokens brake reads it on restart).
	mustOK(t, "RecordUsage", s.RecordUsage(session.Usage{
		InputTokens: 1200, OutputTokens: 340, CacheReadTokens: 800, CacheWriteTokens: 200,
	}))
	calls := []session.ToolCall{
		// Keep Args COMPACT JSON: json.RawMessage round-trips verbatim only
		// for already-compact payloads.
		session.NewToolCall("call-1", "Read", json.RawMessage(`{"path":"a.txt"}`)),
		session.NewToolCall("call-2", "Grep", json.RawMessage(`{"pattern":"TODO"}`)),
	}
	mustOK(t, "RecordAssistant", s.RecordAssistant(session.NewAssistantMessage(
		"reading two files", "opaque-reasoning-replay-blob", calls)))
	mustOK(t, "RecordToolResults", s.RecordToolResults([]session.ToolResult{
		session.NewToolResult("call-1", "contents of a.txt"),
		session.NewToolError("call-2", "grep failed: no matches"),
	}))
	return s
}

// roundTrip saves s and loads it back, failing the test on either error.
func roundTrip(t *testing.T, st port.SessionStore, s *session.Session) *session.Session {
	t.Helper()
	ctx := context.Background()
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := st.Load(ctx, s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return got
}

// assertSessionEqual compares the loaded session against the original
// field-wise across everything the store contract must preserve.
func assertSessionEqual(t *testing.T, got, want *session.Session) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID = %q want %q", got.ID, want.ID)
	}
	if got.State != want.State {
		t.Errorf("State = %q want %q", got.State, want.State)
	}
	if got.Mode != want.Mode {
		t.Errorf("Mode = %q want %q", got.Mode, want.Mode)
	}
	if got.Limits != want.Limits {
		t.Errorf("Limits = %+v want %+v", got.Limits, want.Limits)
	}
	if got.Counters != want.Counters {
		t.Errorf("Counters = %+v want %+v", got.Counters, want.Counters)
	}
	if got.Workspace != want.Workspace {
		t.Errorf("Workspace = %q want %q", got.Workspace, want.Workspace)
	}
	if got.Profile != want.Profile {
		t.Errorf("Profile = %q want %q", got.Profile, want.Profile)
	}
	if got.ProviderID != want.ProviderID {
		t.Errorf("ProviderID = %q want %q", got.ProviderID, want.ProviderID)
	}
	if got.ModelID != want.ModelID {
		t.Errorf("ModelID = %q want %q", got.ModelID, want.ModelID)
	}
	if got.ReasoningEffort != want.ReasoningEffort {
		t.Errorf("ReasoningEffort = %q want %q", got.ReasoningEffort, want.ReasoningEffort)
	}
	if got.Kind != want.Kind || !reflect.DeepEqual(got.Relationship, want.Relationship) {
		t.Errorf("session metadata = (%q, %+v) want (%q, %+v)", got.Kind, got.Relationship, want.Kind, want.Relationship)
	}
	if got.Usage != want.Usage {
		t.Errorf("Usage = %+v want %+v", got.Usage, want.Usage)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v want %v", got.CreatedAt, want.CreatedAt)
	}
	gotMsgs, wantMsgs := got.Conversation.Messages, want.Conversation.Messages
	if len(gotMsgs) != len(wantMsgs) {
		t.Fatalf("message count = %d want %d", len(gotMsgs), len(wantMsgs))
	}
	for i := range wantMsgs {
		if !reflect.DeepEqual(gotMsgs[i], wantMsgs[i]) {
			t.Errorf("message[%d] =\n%+v\nwant\n%+v", i, gotMsgs[i], wantMsgs[i])
		}
	}
}

func intPointer(v int) *int { return &v }

// mustOK fails the test when a session-aggregate transition errors.
func mustOK(t *testing.T, op string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
}

// migrationJobID and migrationUnknownJobID are fixed, valid-format opaque job
// handles (both in-tree SessionMigrationStore implementations require
// exactly 32 hex characters).
var (
	migrationJobID        = strings.Repeat("c", 32)
	migrationUnknownJobID = strings.Repeat("d", 32)
)

// RunSessionMigration exercises the shared port.SessionMigrationStore contract:
// acquisition-gated mutation, cross-acquisition exclusion, ownership
// rejection, and durable job-checkpoint round-tripping. It does NOT assert on
// adapter-private physical format (on-disk layout, Redis key shapes, ...) —
// only the port-level behavior every implementation must honor identically.
//
// seedMigratable must write ONE pre-migration ("not yet on the current
// indexed format") session family directly, in whatever way is appropriate
// for that adapter, and return its ID. Neither in-tree adapter's ordinary
// port.SessionStore.Save path produces a migratable candidate — Save already
// writes the current format — so this cannot be done generically through the
// port alone; each adapter's own test supplies its existing internal seeding
// helper (e.g. jsonlstore's seedMigrationV1, or writing directly into
// miniredis for redisstore) as this hook.
func RunSessionMigration(t *testing.T, newStore func(t *testing.T) port.SessionStore, seedMigratable func(t *testing.T, st port.SessionStore) session.SessionID) {
	t.Helper()
	st := newStore(t)
	migrator, ok := st.(port.SessionMigrationStore)
	if !ok {
		t.Fatalf("store %T does not implement port.SessionMigrationStore", st)
	}

	unbound := context.Background()

	// Unbound context: every mutating/ownership-sensitive call must reject.
	if err := migrator.CheckSessionMigrationJobOwnership(unbound); err == nil {
		t.Fatal("CheckSessionMigrationJobOwnership(unbound) = nil, want a rejection")
	}
	if err := migrator.SaveSessionMigrationJob(unbound, port.SessionMigrationJob{ID: migrationJobID}); err == nil {
		t.Fatal("SaveSessionMigrationJob(unbound) = nil, want a rejection")
	}
	if _, err := migrator.MigrateSessionFamily(unbound, port.SessionMigrationFamily{}); err == nil {
		t.Fatal("MigrateSessionFamily(unbound) = nil, want a rejection")
	}

	// Acquire, then confirm a second acquisition of the SAME job id is
	// excluded while the first is still held.
	boundCtx, release, err := migrator.AcquireSessionMigrationJob(unbound, migrationJobID)
	if err != nil {
		t.Fatalf("AcquireSessionMigrationJob: %v", err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			_ = release()
		}
	})
	// A short-lived context: a lock-based implementation may retry until its
	// caller's context is done rather than failing on the first contended
	// attempt, so the caller supplying a bound is what makes this
	// deterministic here (a real caller is expected to do the same).
	contendedCtx, contendedCancel := context.WithTimeout(unbound, 200*time.Millisecond)
	_, _, contendedErr := migrator.AcquireSessionMigrationJob(contendedCtx, migrationJobID)
	contendedCancel()
	if contendedErr == nil {
		t.Fatal("second AcquireSessionMigrationJob(same id) = nil, want exclusion while the first acquisition is held")
	}
	if err := migrator.CheckSessionMigrationJobOwnership(boundCtx); err != nil {
		t.Fatalf("CheckSessionMigrationJobOwnership(bound) = %v, want nil", err)
	}

	// Seed one migratable family and find it via InspectSessionMigration.
	id := seedMigratable(t, st)
	inspection, err := migrator.InspectSessionMigration(boundCtx)
	if err != nil {
		t.Fatalf("InspectSessionMigration: %v", err)
	}
	if !inspection.Available {
		t.Fatalf("InspectSessionMigration.Available = false, want true (reason %q)", inspection.UnavailableReason)
	}
	var family port.SessionMigrationFamily
	found := false
	for _, f := range inspection.Families {
		if f.ID == id {
			family, found = f, true
			break
		}
	}
	if !found {
		t.Fatalf("InspectSessionMigration did not report seeded family %q among %d families", id, len(inspection.Families))
	}

	// A tampered fingerprint must be rejected as "changed", never silently
	// migrated over content that no longer matches what was inspected.
	tampered := family
	tampered.Fingerprint += "-tampered"
	if reason, err := migrator.MigrateSessionFamily(boundCtx, tampered); err != nil || reason != "changed" {
		t.Fatalf("MigrateSessionFamily(tampered fingerprint) = (%q, %v), want (\"changed\", nil)", reason, err)
	}

	// The real migration, still holding the same acquisition, must succeed.
	if reason, err := migrator.MigrateSessionFamily(boundCtx, family); err != nil || reason != "" {
		t.Fatalf("MigrateSessionFamily(real family) = (%q, %v), want (\"\", nil)", reason, err)
	}

	// Job checkpoint round-trip: Save requires the acquisition, Load does not.
	job := port.SessionMigrationJob{
		ID: migrationJobID, State: port.SessionMigrationRunning, Generation: inspection.Generation,
		V1Families: 1, Processed: 1, Migrated: 1, TerminalItems: map[string]bool{family.Handle: true},
	}
	if err := migrator.SaveSessionMigrationJob(boundCtx, job); err != nil {
		t.Fatalf("SaveSessionMigrationJob: %v", err)
	}
	loaded, err := migrator.LoadSessionMigrationJob(unbound, migrationJobID)
	if err != nil {
		t.Fatalf("LoadSessionMigrationJob: %v", err)
	}
	if loaded.ID != job.ID || loaded.State != job.State || loaded.Processed != job.Processed ||
		loaded.Migrated != job.Migrated || !loaded.TerminalItems[family.Handle] {
		t.Fatalf("LoadSessionMigrationJob round-trip = %+v, want %+v", loaded, job)
	}
	if _, err := migrator.LoadSessionMigrationJob(unbound, migrationUnknownJobID); err == nil {
		t.Fatal("LoadSessionMigrationJob(unknown id) = nil, want an error")
	}

	// Release, then confirm every acquisition-gated call rejects again.
	// context.WithoutCancel: releasing may cancel boundCtx itself (e.g.
	// redisstore's release cancels the acquisition's operation context), and
	// a plain cancelled-context error would make this pass even if the
	// adapter's OWN ownership fence were broken — it must be the fence being
	// tested, not ctx cancellation.
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	released = true
	releasedCtx := context.WithoutCancel(boundCtx)
	if err := migrator.CheckSessionMigrationJobOwnership(releasedCtx); err == nil {
		t.Fatal("CheckSessionMigrationJobOwnership(released) = nil, want a rejection")
	}
	if err := migrator.SaveSessionMigrationJob(releasedCtx, job); err == nil {
		t.Fatal("SaveSessionMigrationJob(released) = nil, want a rejection")
	}
}
