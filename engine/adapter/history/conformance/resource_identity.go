package conformance

import (
	"context"
	"errors"
	"testing"

	resourceid "github.com/stacklok/mecatl/engine/id"
	"github.com/stacklok/mecatl/engine/kernel/history"
)

// resourceIdentityKinds proves that history and storage use the caller's whole
// resource identity, including its owner-selected prefix, without a second key.
func resourceIdentityKinds(t *testing.T, backend Factory) {
	ctx := context.Background()
	store := backend(t)
	h, err := history.New(store, policy())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"FOO", "SES"} {
		id, err := resourceid.Parse(kind + "1111111111111111111111")
		if err != nil {
			t.Fatal(err)
		}
		root, rootRef := encoded(t, id, history.CommitRef{}, "root")
		appendAccepted(t, h, id, root, rootRef)
		next, head := encoded(t, id, rootRef, "next")
		appendAccepted(t, h, id, next, head)
		observation, err := store.Observe(ctx, id, 1)
		if err != nil || observation.Head != head || observation.Commit != rootRef {
			t.Fatalf("same resource key: %+v, %v", observation, err)
		}
		page, err := h.Read(ctx, id, history.ReadRequest{Through: head, MaxCommits: 2})
		if err != nil || len(page.Commits) != 2 || page.Next != head {
			t.Fatalf("generic history round trip: %+v, %v", page, err)
		}
		if got := page.Commits[0].Commit; got.HistoryID == nil || *got.HistoryID != id || !got.Equal(root) {
			t.Fatal("root did not retain the supplied generic identity")
		}
		foreign := root.Clone()
		foreign.HistoryID = new(resourceid.New("BAR"))
		if ref, err := h.Identify(id, foreign); !errors.Is(err, history.ErrInvalid) || ref != (history.CommitRef{}) {
			t.Fatalf("foreign root accepted: %v, %v", ref, err)
		}
	}
}
