package k8slease

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const testNamespace = "mecatl"

// fakeClock is an advanceable port.Clock to cross the TTL without real sleeps.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newFakeLease(t *testing.T) (*Lease, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	cs := fake.NewSimpleClientset()
	return New(cs, testNamespace, 30*time.Second, clk), clk
}

// TestObjectNameEncoding pins Open Risk #2: arbitrary, long, and
// delegation-prefixed session ids all encode to a valid RFC-1123 Lease name
// (≤253 chars, lowercase-alnum-and-dash), and the encoding is collision-free
// (distinct ids → distinct names).
func TestObjectNameEncoding(t *testing.T) {
	ids := []session.SessionID{
		"simple",
		"With Spaces And UPPER",
		"team-abc123/member-1",
		"subagent-deadbeef-0",
		"parallel-callid-3",
		session.SessionID(strings.Repeat("x", 4096)),
		"unicode-héllo-世界",
		"",
	}
	seen := map[string]session.SessionID{}
	for _, id := range ids {
		name := objectName(id)
		if len(name) > 253 {
			t.Errorf("objectName(%q) length %d > 253", id, len(name))
		}
		if !isRFC1123Subdomain(name) {
			t.Errorf("objectName(%q) = %q is not a valid RFC-1123 subdomain", id, name)
		}
		if prev, dup := seen[name]; dup {
			t.Errorf("collision: %q and %q both encode to %q", id, prev, name)
		}
		seen[name] = id
	}
	// Same id is stable across calls.
	if a, b := objectName("stable"), objectName("stable"); a != b {
		t.Errorf("objectName not stable: %q vs %q", a, b)
	}
}

// isRFC1123Subdomain is a minimal validator: lowercase alphanumerics and dashes,
// dots allowed between labels, starting/ending alphanumeric. Our names are
// prefix + hex so they are a single label.
func isRFC1123Subdomain(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for i, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if !ok {
			return false
		}
		if (i == 0 || i == len(s)-1) && r == '-' {
			return false
		}
	}
	return true
}

// TestFakeNonConflictSubset exercises the contract paths the fake clientset CAN
// honour. The fake's ObjectTracker does NOT enforce resourceVersion CAS on Update
// (Open Risk #1, VERIFIED: a stale Update succeeds with no 409), so the
// CAS-conflict path is covered separately in TestUpdateConflictIsLeaseHeld via a
// PrependReactor; here we cover everything that turns on holder/expiry logic.
func TestFakeNonConflictSubset(t *testing.T) {
	ctx := context.Background()

	t.Run("acquire fresh creates the object", func(t *testing.T) {
		l, _ := newFakeLease(t)
		lease, err := l.Acquire(ctx, "fresh", "owner-a")
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		if lease.Owner != "owner-a" || lease.Token != 1 || lease.Expiry.IsZero() {
			t.Fatalf("Acquire returned %+v, want owner-a, token 1, non-zero expiry", lease)
		}
	})

	t.Run("contend by a live different owner fails", func(t *testing.T) {
		l, _ := newFakeLease(t)
		if _, err := l.Acquire(ctx, "contend", "owner-a"); err != nil {
			t.Fatalf("Acquire by A: %v", err)
		}
		_, err := l.Acquire(ctx, "contend", "owner-b")
		if !errors.Is(err, port.ErrLeaseHeld) {
			t.Fatalf("Acquire by B = %v, want ErrLeaseHeld", err)
		}
	})

	t.Run("same owner re-acquire keeps the token", func(t *testing.T) {
		l, _ := newFakeLease(t)
		first, err := l.Acquire(ctx, "reacq", "owner-a")
		if err != nil {
			t.Fatalf("Acquire #1: %v", err)
		}
		second, err := l.Acquire(ctx, "reacq", "owner-a")
		if err != nil {
			t.Fatalf("Acquire #2: %v", err)
		}
		if second.Token != first.Token {
			t.Errorf("same-owner re-acquire token = %d, want %d", second.Token, first.Token)
		}
	})

	t.Run("renew within window keeps token, extends expiry", func(t *testing.T) {
		l, clk := newFakeLease(t)
		first, err := l.Acquire(ctx, "renew", "owner-a")
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		clk.advance(10 * time.Second)
		renewed, err := l.Renew(ctx, first)
		if err != nil {
			t.Fatalf("Renew: %v", err)
		}
		if renewed.Token != first.Token {
			t.Errorf("Renew token = %d, want %d", renewed.Token, first.Token)
		}
		if !renewed.Expiry.After(first.Expiry) {
			t.Errorf("Renew expiry = %v, want after %v", renewed.Expiry, first.Expiry)
		}
	})

	t.Run("expiry takeover bumps the token", func(t *testing.T) {
		l, clk := newFakeLease(t)
		first, err := l.Acquire(ctx, "expire", "owner-a")
		if err != nil {
			t.Fatalf("Acquire by A: %v", err)
		}
		clk.advance(31 * time.Second) // past TTL
		second, err := l.Acquire(ctx, "expire", "owner-b")
		if err != nil {
			t.Fatalf("Acquire by B after expiry: %v", err)
		}
		if second.Token <= first.Token {
			t.Errorf("expiry-takeover token = %d, want strictly > %d", second.Token, first.Token)
		}
		if second.Owner != "owner-b" {
			t.Errorf("holder after takeover = %q, want owner-b", second.Owner)
		}
	})

	t.Run("renew after expiry-and-takeover is lost", func(t *testing.T) {
		l, clk := newFakeLease(t)
		first, err := l.Acquire(ctx, "lost", "owner-a")
		if err != nil {
			t.Fatalf("Acquire by A: %v", err)
		}
		clk.advance(31 * time.Second)
		if _, err := l.Acquire(ctx, "lost", "owner-b"); err != nil {
			t.Fatalf("takeover by B: %v", err)
		}
		_, err = l.Renew(ctx, first)
		if !errors.Is(err, port.ErrLeaseHeld) {
			t.Fatalf("Renew of a lost lease = %v, want ErrLeaseHeld", err)
		}
	})

	t.Run("release frees the lease and is idempotent", func(t *testing.T) {
		l, _ := newFakeLease(t)
		held, err := l.Acquire(ctx, "rel", "owner-a")
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		if err := l.Release(ctx, held); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if err := l.Release(ctx, held); err != nil {
			t.Fatalf("Release #2 (idempotent): %v", err)
		}
		if _, err := l.Acquire(ctx, "rel", "owner-b"); err != nil {
			t.Fatalf("Acquire after Release by other owner: %v", err)
		}
	})

	t.Run("release of a non-owned lease is a no-op", func(t *testing.T) {
		l, _ := newFakeLease(t)
		if _, err := l.Acquire(ctx, "noown", "owner-a"); err != nil {
			t.Fatalf("Acquire by A: %v", err)
		}
		// owner-b releasing must NOT drop A's hold.
		if err := l.Release(ctx, port.Lease{SessionID: "noown", Owner: "owner-b", Token: 1}); err != nil {
			t.Fatalf("Release by non-owner: %v", err)
		}
		_, err := l.Acquire(ctx, "noown", "owner-c")
		if !errors.Is(err, port.ErrLeaseHeld) {
			t.Fatalf("A's hold should survive a non-owner Release; Acquire by C = %v, want ErrLeaseHeld", err)
		}
	})

	t.Run("distinct sessions lease independently", func(t *testing.T) {
		l, _ := newFakeLease(t)
		if _, err := l.Acquire(ctx, "multi-a", "owner-a"); err != nil {
			t.Fatalf("Acquire a: %v", err)
		}
		if _, err := l.Acquire(ctx, "multi-b", "owner-b"); err != nil {
			t.Fatalf("Acquire b: %v", err)
		}
	})
}

// TestUpdateConflictIsLeaseHeld covers Open Risk #1: the fake clientset does not
// enforce resourceVersion CAS, so the 409-Conflict-on-takeover path is exercised
// with a PrependReactor that returns apierrors.NewConflict on the Update verb.
// A concurrent takeover that loses the CAS race must surface as ErrLeaseHeld, not
// a hard error. (A real cluster / envtest enforces the CAS natively; that path is
// NOT in CI — documented gap.)
func TestUpdateConflictIsLeaseHeld(t *testing.T) {
	ctx := context.Background()
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	cs := fake.NewSimpleClientset()

	// Seed an EXPIRED lease held by owner-a so the takeover path runs an Update.
	l := New(cs, testNamespace, 30*time.Second, clk)
	if _, err := l.Acquire(ctx, "conflict", "owner-a"); err != nil {
		t.Fatalf("seed Acquire: %v", err)
	}
	clk.advance(31 * time.Second) // expire it so owner-b's Acquire takes the Update branch.

	gvr := schema.GroupVersionResource{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"}
	cs.PrependReactor("update", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(gvr.GroupResource(), "mecatl-lease-conflict", errors.New("the object has been modified"))
	})

	_, err := l.Acquire(ctx, "conflict", "owner-b")
	if !errors.Is(err, port.ErrLeaseHeld) {
		t.Fatalf("Acquire with a 409 Conflict = %v, want ErrLeaseHeld", err)
	}
}
