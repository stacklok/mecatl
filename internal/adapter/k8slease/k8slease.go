// Package k8slease is the Kubernetes-backed port.SessionLease: cross-process,
// cross-HOST single-writer enforcement for the multi-replica cloud-native
// posture (ADR 0027 Phase 4), backed by a coordination.k8s.io/v1 Lease object
// per session id. It is the multi-host story the single-host flock lease cannot
// give: an API-server-coordinated lease survives a replica moving between nodes.
//
// It is BUILT but UNWIRED by default: composition constructs it ONLY when an
// operator selects it with --session-lease-k8s-namespace, so the default path is
// byte-identical with no lease. k8slease never returns ErrLeaseUnsupported — it
// is a fully-supporting adapter; an RBAC Forbidden surfaces as a hard
// infrastructure error (the operator fixed the wrong thing), not a sticky
// disable.
//
// Object naming: a session id is arbitrary text (a team/subagent id, a UUID, a
// user string) and need not be a valid RFC-1123 object name, so the Lease object
// is named "mecatl-lease-" + hex(sha256(id))[:40] — always ≤253 chars, always
// RFC-1123-valid, and collision-free (one-way hash). The raw id is preserved in
// an annotation for operators eyeballing `kubectl get leases`.
//
// Fencing: spec.leaseTransitions is the canonical fencing counter — it advances
// on every takeover and is the port's Token. spec.renewTime +
// spec.leaseDurationSeconds is the expiry; a Get-then-Update with the read
// resourceVersion gives optimistic-concurrency CAS, so a lost race surfaces as a
// 409 Conflict → ErrLeaseHeld.
//
// RBAC: this adapter only ever calls Get/Create/Update/Delete (never List or
// Watch), so it needs get,create,update,delete on `leases` in the
// `coordination.k8s.io` API group, namespace-scoped (a Role + RoleBinding on the
// configured namespace). See user-docs/building/deployment/mecated.md.
package k8slease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// objectNamePrefix prefixes every Lease object name; the rest is the id hash.
const objectNamePrefix = "mecatl-lease-"

// rawIDAnnotation preserves the un-hashed session id for operator visibility.
const rawIDAnnotation = "mecatl.stacklok.com/session-id"

// Lease is a port.SessionLease over coordination.k8s.io Lease objects in one
// namespace.
type Lease struct {
	clientset kubernetes.Interface
	namespace string
	ttl       time.Duration
	clock     port.Clock
}

// compile-time assertion that *Lease satisfies the port.
var _ port.SessionLease = (*Lease)(nil)

// New constructs a k8s-backed lease over clientset in namespace, with the given
// TTL and clock. A non-positive ttl defaults to 30s.
func New(clientset kubernetes.Interface, namespace string, ttl time.Duration, clock port.Clock) *Lease {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Lease{clientset: clientset, namespace: namespace, ttl: ttl, clock: clock}
}

// Acquire grants the lease when the object is absent, expired, or already held by
// owner; otherwise ErrLeaseHeld. A takeover bumps leaseTransitions (the token)
// and uses the read resourceVersion as a CAS guard so a concurrent takeover loses
// with a 409 Conflict → ErrLeaseHeld.
func (l *Lease) Acquire(ctx context.Context, id session.SessionID, owner string) (port.Lease, error) {
	now := l.clock.Now()
	leases := l.clientset.CoordinationV1().Leases(l.namespace)

	cur, err := leases.Get(ctx, objectName(id), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return l.createLease(ctx, id, owner, now)
	}
	if err != nil {
		return port.Lease{}, fmt.Errorf("k8slease: get lease %q: %w", id, err)
	}

	holder := derefStr(cur.Spec.HolderIdentity)
	expired := !now.Before(expiryOf(cur))
	if !expired && holder != owner {
		return port.Lease{}, port.ErrLeaseHeld
	}
	// Free (expired) or our own lease → take over / refresh under a CAS Update.
	token := derefInt32(cur.Spec.LeaseTransitions)
	if expired || holder != owner {
		token++ // a takeover advances the fencing counter.
	}
	return l.updateLease(ctx, id, cur, owner, token, now)
}

// Renew extends a lease the caller still holds (holder + token match, unexpired),
// keeping the token and refreshing renewTime under a CAS Update. A holder change,
// a token mismatch, an expiry, or a 409 Conflict → ErrLeaseHeld (the loss signal).
func (l *Lease) Renew(ctx context.Context, in port.Lease) (port.Lease, error) {
	now := l.clock.Now()
	leases := l.clientset.CoordinationV1().Leases(l.namespace)

	cur, err := leases.Get(ctx, objectName(in.SessionID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return port.Lease{}, port.ErrLeaseHeld // gone → we no longer hold it.
	}
	if err != nil {
		return port.Lease{}, fmt.Errorf("k8slease: get lease %q: %w", in.SessionID, err)
	}
	holder := derefStr(cur.Spec.HolderIdentity)
	token := derefInt32(cur.Spec.LeaseTransitions)
	if holder != in.Owner || tokenToUint(token) != in.Token || !now.Before(expiryOf(cur)) {
		return port.Lease{}, port.ErrLeaseHeld
	}
	return l.updateLease(ctx, in.SessionID, cur, in.Owner, token, now)
}

// Release relinquishes a lease the caller still holds (holder + token match) by
// writing a TOMBSTONE: the holder is cleared and the renewTime is wound back into
// the past so the object reads as already-expired, while leaseTransitions (the
// fencing token) is RETAINED. This keeps the per-id token monotone across release
// — the port.SessionLease contract a successful takeover after a release returns a
// strictly-greater token (pinned by the shared leaseconformance suite). Deleting
// the object instead would reset leaseTransitions to 1 on the next create and
// break that contract. Idempotent: a NotFound, a holder/token mismatch, or a
// concurrent change is a no-op success — Release only drops the caller's OWN hold.
func (l *Lease) Release(ctx context.Context, in port.Lease) error {
	leases := l.clientset.CoordinationV1().Leases(l.namespace)
	cur, err := leases.Get(ctx, objectName(in.SessionID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("k8slease: get lease %q: %w", in.SessionID, err)
	}
	if derefStr(cur.Spec.HolderIdentity) != in.Owner || tokenToUint(derefInt32(cur.Spec.LeaseTransitions)) != in.Token {
		return nil // not our hold; idempotent no-op.
	}
	// Write a tombstone under a CAS Update: clear the holder and set renewTime into
	// the past so the lease reads as expired, retaining leaseTransitions. A 409
	// Conflict (someone else took over under us) is a no-op success — we have
	// nothing to release.
	now := l.clock.Now()
	token := derefInt32(cur.Spec.LeaseTransitions)
	tomb := cur.DeepCopy()
	if tomb.Annotations == nil {
		tomb.Annotations = map[string]string{}
	}
	tomb.Annotations[rawIDAnnotation] = string(in.SessionID)
	emptyHolder := ""
	tomb.Spec.HolderIdentity = &emptyHolder
	past := metav1.NewMicroTime(now.Add(-l.ttl - time.Second))
	tomb.Spec.RenewTime = &past
	tomb.Spec.LeaseTransitions = &token
	if _, err := leases.Update(ctx, tomb, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return nil // lost the CAS race or gone — nothing to release.
		}
		return fmt.Errorf("k8slease: tombstone lease %q: %w", in.SessionID, err)
	}
	return nil
}

// createLease creates a fresh Lease object (transitions=1) and maps a 409
// AlreadyExists (a concurrent create won) onto ErrLeaseHeld.
func (l *Lease) createLease(ctx context.Context, id session.SessionID, owner string, now time.Time) (port.Lease, error) {
	micro := metav1.NewMicroTime(now)
	dur := l.durationSeconds()
	transitions := int32(1)
	obj := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:        objectName(id),
			Namespace:   l.namespace,
			Annotations: map[string]string{rawIDAnnotation: string(id)},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &owner,
			LeaseDurationSeconds: &dur,
			AcquireTime:          &micro,
			RenewTime:            &micro,
			LeaseTransitions:     &transitions,
		},
	}
	created, err := l.clientset.CoordinationV1().Leases(l.namespace).Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return port.Lease{}, port.ErrLeaseHeld // a racing create won.
	}
	if err != nil {
		return port.Lease{}, fmt.Errorf("k8slease: create lease %q: %w", id, err)
	}
	return toPort(id, created), nil
}

// updateLease writes holder/token/renewTime onto cur (carrying its
// resourceVersion for the CAS) and maps a 409 Conflict onto ErrLeaseHeld.
func (l *Lease) updateLease(ctx context.Context, id session.SessionID, cur *coordinationv1.Lease, owner string, token int32, now time.Time) (port.Lease, error) {
	micro := metav1.NewMicroTime(now)
	dur := l.durationSeconds()
	next := cur.DeepCopy()
	if next.Annotations == nil {
		next.Annotations = map[string]string{}
	}
	next.Annotations[rawIDAnnotation] = string(id)
	next.Spec.HolderIdentity = &owner
	next.Spec.LeaseDurationSeconds = &dur
	next.Spec.RenewTime = &micro
	next.Spec.LeaseTransitions = &token
	if next.Spec.AcquireTime == nil {
		next.Spec.AcquireTime = &micro
	}
	updated, err := l.clientset.CoordinationV1().Leases(l.namespace).Update(ctx, next, metav1.UpdateOptions{})
	if apierrors.IsConflict(err) {
		return port.Lease{}, port.ErrLeaseHeld // lost the CAS race.
	}
	if err != nil {
		return port.Lease{}, fmt.Errorf("k8slease: update lease %q: %w", id, err)
	}
	return toPort(id, updated), nil
}

// durationSeconds renders the TTL as a clamped, non-negative int32 second count
// for the Lease object's leaseDurationSeconds (the k8s field is *int32). A TTL
// beyond ~68 years clamps to the int32 max — never a realistic lease, so the
// clamp is a defensive bound rather than a live path.
func (l *Lease) durationSeconds() int32 {
	secs := int64(l.ttl / time.Second)
	if secs < 0 {
		secs = 0
	}
	if secs > math.MaxInt32 {
		secs = math.MaxInt32
	}
	return int32(secs)
}

// toPort projects a Lease object onto the port value (token = leaseTransitions,
// expiry = renewTime + leaseDurationSeconds).
func toPort(id session.SessionID, obj *coordinationv1.Lease) port.Lease {
	return port.Lease{
		SessionID: id,
		Owner:     derefStr(obj.Spec.HolderIdentity),
		Token:     tokenToUint(derefInt32(obj.Spec.LeaseTransitions)),
		Expiry:    expiryOf(obj),
	}
}

// objectName encodes a session id into a collision-free RFC-1123 Lease name.
func objectName(id session.SessionID) string {
	sum := sha256.Sum256([]byte(id))
	return objectNamePrefix + hex.EncodeToString(sum[:])[:40]
}

// expiryOf computes a Lease's expiry: renewTime + leaseDurationSeconds. A Lease
// with no renewTime is treated as already expired (zero time).
func expiryOf(obj *coordinationv1.Lease) time.Time {
	if obj.Spec.RenewTime == nil || obj.Spec.LeaseDurationSeconds == nil {
		return time.Time{}
	}
	return obj.Spec.RenewTime.Add(time.Duration(*obj.Spec.LeaseDurationSeconds) * time.Second)
}

// tokenToUint maps the k8s leaseTransitions counter (a non-negative *int32) onto
// the port's uint64 fencing token. A negative value (never written by this
// adapter) clamps to 0.
func tokenToUint(t int32) uint64 {
	if t < 0 {
		return 0
	}
	return uint64(t)
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}
