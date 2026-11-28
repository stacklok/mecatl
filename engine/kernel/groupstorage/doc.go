// Package groupstorage declares the storage authority for one resource group.
//
// One compatible store authority must adjudicate Claim and history appends together.
// The contract intentionally provides no expiry-acceptance predicate: expiry makes
// takeover eligible, but a current generation can renew after eligibility, and
// renewal and takeover serialize. Storage implementations preserve their own
// durability guarantees, including possible acknowledged-write loss in a Redis
// failover. This package declares no persistence DTO or codec:
// backends serialize the opaque authority handles through ResourceID or String.
package groupstorage
