package resourcegroup

import (
	"context"
	"reflect"

	engineid "github.com/stacklok/mecatl/engine/id"
)

// ResourceID is a comparable domain identity exposing its generic storage key.
// Reconstruction establishes neither allocation, membership, nor authority.
type ResourceID interface {
	comparable
	// ResourceID returns the same identity as a generic storage key, or zero if unset.
	ResourceID() engineid.ID
}

// Resource exposes the stable domain identity of one resident instance.
type Resource[I ResourceID] interface {
	// ID returns this instance's stable domain identity.
	ID() I
}

// Factory declares stable, concurrency-safe, local, non-I/O registration metadata. One exact
// ResourceType and one Kind identify each registration. Implementations must not
// be typed nil; metadata alone cannot prove a compatible typed Loader exists.
// This is a handwritten contract, not a generic factory implementation.
type Factory interface {
	// ResourceType returns the exact resource T, not its ID or an assignable type.
	ResourceType() reflect.Type
	// Kind returns the domain-owned resource ID prefix.
	Kind() engineid.Kind
	// Policy returns immutable per-history and resident-instance bounds.
	Policy() FactoryPolicy
}

// Loader reconstructs a domain resource using only its controlled instance Scope.
// Implementations also provide exact Factory metadata. Neither Load nor a Create
// closure receives a caller-owned construction context or unfenced storage.
type Loader[T Resource[I], I ResourceID] interface {
	Factory
	// DecodeID checks the domain kind and reconstructs the same generic key;
	// success must round-trip exactly through ResourceID.
	DecodeID(engineid.ID) (I, error)
	// Load reconstructs one existing permanent member within the supplied Scope.
	Load(*Scope, I) (Loaded[T], error)
}

// Load proposes typed activation of an existing member, converging concurrent
// loads and permitting a load to join an in-flight Create. ctx cancels only this
// caller's wait, never shared construction. Before invoking domain code, typed
// lookup must verify exact T registration, Loader compatibility, kind, and ID
// decoding/round-trip agreement; metadata may lie. Returned resource identity must
// also agree before publication. No partial or late success may publish a usable
// instance after retirement. Retaining a resource pointer does not pin residency;
// after terminal completion a fresh instance may be loaded if policy permits.
// Currently it returns zero T, ErrNotImplemented without any lookup or callback.
func Load[T Resource[I], I ResourceID](context.Context, *Group, I) (T, error) {
	var zero T
	return zero, ErrNotImplemented
}

// Create proposes creating a resource whose ID was already allocated by its
// domain-specific Create helper. It resolves the exact registered F keyed by T,
// validates Loader/type/kind/ID round-trip agreement before construction, then
// invokes construct with that factory and a fresh Scope. Callers supply neither
// alternate dependencies nor a public service locator. ctx cancels only waiting.
//
// A resident, persisted, or constructing ID is a collision, never successful
// creation returning an existing instance. Loads may join this construction.
// Partial failure with a Completion is adopted for shutdown accounting; failure
// without Completion must already be cleaned up by domain code. Late success is
// never published as usable after retirement. These rules also apply to Load.
// Currently it returns zero T, ErrNotImplemented and never invokes construct.
func Create[T Resource[I], I ResourceID, F Loader[T, I]](context.Context, *Group, I, func(F, *Scope, I) (Loaded[T], error)) (T, error) {
	var zero T
	return zero, ErrNotImplemented
}
