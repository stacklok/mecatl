// Package sessiongroup sketches the hosting residency boundary for the isolated
// kernel. A SessionGroup represents the shared placement, exclusive ownership,
// and lifetime of permanently assigned Sessions, whether loaded or not. It is
// not a conversation, a root Session, or an execution loop. Empty groups are valid.
//
// A group manages lazy SessionRuntime residency; each runtime runs one Session
// and privately serializes its passive aggregate. Membership and the loaded set
// are distinct: unloading changes residency, never membership or group ownership.
// GroupID remains stable across hosts and ownership incarnations. SessionIDs are
// independent peer identities, not derived from GroupID or a first member.
//
// These are provisional declarations, not an operational host. Open and every
// lifecycle/navigation operation return ErrNotImplemented. They perform no I/O,
// start no goroutines, retain no arguments, and establish no ownership. Storage,
// authoritative membership, lease maintenance, resource policy, and private
// runtime construction are deliberately left for subsequent contract slices.
// Revisit these signatures with those consumers before implementing them.
//
// The intended implementation permits concurrent member progress, converges
// concurrent loads to one mutable runtime per member, and orders loading, closing,
// and ownership loss without lifecycle locks across storage or external work.
// Loss terminally retires the group; member-local failure need not retire peers.
// Neither group nor runtime exposes a backend, lease, or mutable Session.
package sessiongroup
