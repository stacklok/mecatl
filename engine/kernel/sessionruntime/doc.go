// Package sessionruntime sketches the live execution owner for one isolated
// kernel Session. A SessionRuntime runs the Session: loop sequencing, canonical
// submission coordination, and supervised model/tool work belong here, not on
// the passive session.Session aggregate or on resourcegroup.Group.
//
// A concrete handwritten Factory captures typed Session dependencies and supplies
// resourcegroup.Loader metadata. Hosts register it with a resourcegroup.Manager;
// package Load and Create are typed domain entry points, not Manager lookups.
// Dependency direction is sessionruntime to resourcegroup, never the reverse.
// Construction receives only a controlled resourcegroup.Scope, never a raw storage
// backend or caller-owned construction context. No production wiring is changed.
//
// These are provisional declarations. NewFactory, Factory.Load, package Load and
// Create, Snapshot, Run, and Cancel return the shared ErrNotImplemented sentinel,
// perform no I/O, retain no input, and execute nothing. Create allocates no ID.
// Only configuration validation, metadata, ID decoding, and identity accessors are
// operational. Identity accessors are safe on zero/nil runtimes and return unset
// identities. No residency, ownership, or actual factory construction is implemented.
//
// A run is not started by construction, history replay, or an accepted request
// commitment. Execution admission, cancellation, shutdown, and uncertainty reports
// need a concrete first interaction consumer before these signatures are final.
// This slice selects neither steering/queue behavior nor a public async handle.
package sessionruntime
