// Package sessionruntime sketches the live execution owner for one isolated
// kernel Session. A SessionRuntime runs the Session: loop sequencing, canonical
// submission coordination, and supervised model/tool work belong here, not on
// the passive session.Session aggregate or on SessionGroup.
//
// Only a hosting SessionGroup is intended to activate runtimes. This package has
// no public constructor accepting a Session, backend, or ownership capability;
// the group-to-runtime private construction seam is deferred until ownership and
// storage composition are reviewed. Dependency direction is from sessiongroup
// to sessionruntime, never back; group correlation uses the identity leaf.
//
// These are provisional declarations. Snapshot, Run, and Cancel always return
// ErrNotImplemented, perform no I/O, retain no input, and execute nothing.
// Identity accessors are safe on zero/nil runtimes and return unset identities.
// A run is not started by construction, history replay, or an accepted request
// commitment. Execution admission, cancellation, shutdown, and uncertainty reports
// need a concrete first interaction consumer before these signatures are final.
// This slice selects neither steering/queue behavior nor a public async handle.
package sessionruntime
