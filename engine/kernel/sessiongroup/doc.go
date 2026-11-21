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
// Configuration, bounded shutdown reporting, and the separate lease contracts
// declare group-scoped authority and fenced storage guarantees without providing
// an operational host. Config is a provisional construction boundary, not a
// general runtime capability bundle. Drain proposes nonblocking initiation;
// Status exposes provisional phase/authority observations, Done signals bounded
// completion, and Report returns the final accounting. LifecycleStatus describes
// normal drain and emergency cleanup paths, not a generic plugin protocol.
//
// These are provisional declarations, not an operational host. Open, Drain,
// Status, Report, and navigation operations return ErrNotImplemented. Done is
// nil, not a completion signal. They perform no I/O, start no goroutines, retain
// no arguments, and establish no ownership. Storage, authoritative membership,
// lease maintenance, and private runtime construction are deliberately left for
// subsequent contract slices. Revisit these signatures with concrete consumers
// before implementing them.
//
// The intended implementation permits concurrent member progress, converges
// concurrent loads to one mutable runtime per member, and orders loading, closing,
// and ownership loss without lifecycle locks across storage or external work.
// Loss terminally retires the group; member-local failure need not retire peers.
// Neither group nor runtime exposes a backend, lease, or mutable Session.
package sessiongroup
