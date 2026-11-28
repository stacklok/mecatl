package groupstorage

import (
	"context"
	"time"

	resourceid "github.com/stacklok/mecatl/engine/id"
	"github.com/stacklok/mecatl/engine/kernel/history"
	groupid "github.com/stacklok/mecatl/engine/kernel/resourcegroup/id"
)

// Backend is the sole group-scoped authority for compatible claim and append
// storage. Implementations are safe for concurrent calls. Mutation errors return
// zero results and can leave acceptance unknown; a known success is not masked by
// late context cancellation. There is deliberately no expiry-acceptance predicate:
// append acceptance depends on the current unreleased generation, membership, and
// full expected history head, not expiry.
type Backend interface {
	// GroupID returns this backend's nonzero resource-group identity.
	GroupID() groupid.GroupID

	// Claim acquires or reconciles authority for acquisition. acquisition must be
	// nonzero and ttl must be positive. Repeating a Claim for the current
	// acquisition reconciles response loss and returns the same grant; it does not
	// mint another generation. This contract has no strong abandon operation and
	// does not promise receipts forever. Claim serializes with takeover and Renew.
	// A successful grant has nonzero Generation and Validity. An error returns a
	// zero Grant and may leave acceptance unknown.
	Claim(context.Context, AcquisitionID, time.Duration) (Grant, error)

	// Renew extends a current generation for a positive ttl. A generation remains
	// eligible to renew after expiry made takeover eligible; renewal and takeover
	// serialize. Success returns conservative local-clock evidence. Error returns
	// zero Validity; a late result must not revive locally retired authority.
	Renew(context.Context, Generation, time.Duration) (Validity, error)

	// Release immediately retires generation. It is exact-generation scoped and
	// idempotent: stale cleanup never affects a successor. Nil proves release;
	// an error may leave acceptance unknown.
	Release(context.Context, Generation) error

	// MemberKinds returns unique valid resource kinds with permanent members,
	// bounded to at most 26^3 entries independently of member count. It does not
	// replay history. Errors return a nil slice.
	MemberKinds(context.Context) ([]resourceid.Kind, error)

	// Members returns one owned, bounded page of permanent members of kind. The
	// request must validate for kind. There is no cross-page snapshot: concurrent
	// additions behind the cursor may be omitted until a rescan. Errors return a
	// zero MemberPage.
	Members(context.Context, resourceid.Kind, MemberPageRequest) (MemberPage, error)

	// Bind returns a cheap, non-I/O history restriction for this group and current
	// generation. It permits observations and reads of an absent root so creation
	// can begin, but rejects other history keys and nonroot nonmembers. Root commit
	// acceptance atomically establishes permanent membership and first history.
	// Normal appends atomically require membership, the current unreleased
	// generation, and the full expected head; failed CAS observations provide retry
	// evidence. Passive bounded reads survive retirement. The returned backend
	// exposes no unfenced storage capability.
	Bind(Generation, resourceid.ID) history.Backend
}

// Grant is successful Claim evidence for one immutable authority incarnation.
type Grant struct {
	// Generation identifies the current authority incarnation and is nonzero on
	// successful Claim.
	Generation Generation
	// Validity is conservative current evidence and is nonzero on successful Claim.
	Validity Validity
}

// Validity is conservative evidence in the manager's injected local-clock domain.
// Until accounts for request delay and uncertainty, rather than being receipt time
// plus TTL. It is not a storage authority or an expiry-acceptance predicate.
type Validity struct {
	// Until is an exclusive local deadline. Zero is invalid; at or after Until this
	// evidence does not justify local authority.
	Until time.Time
}
