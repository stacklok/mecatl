// Package resourcegroup declares generic process-local resource residency under
// shared group authority. Manager owns Groups; each Group owns resource instances
// constructed through registered domain factories. Registration is local routing,
// not placement, membership, or authority.
//
// This is a declaration-only skeleton. Operational calls return zero outputs and
// ErrNotImplemented without validating arguments, retaining dependencies, invoking
// callbacks, or starting work, even for nil receivers and canceled contexts.
// Context, Done, and Draining return nil. Only identity accessors and explicit
// configuration/policy Validate methods are functional.
//
// Comments describing eventual coordination are provisional contracts to revisit
// with real factory consumers, not guarantees implemented by this skeleton.
package resourcegroup
