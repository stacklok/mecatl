//go:generate go -C ../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/session -emit view -out zz_views_generated.go

// Package session interprets accepted history as lifecycle state and retained
// context for the isolated kernel. It owns session admission and reconstruction;
// it does not execute model requests or tool calls, or resolve content references.
//
// Payloads are mutable DTOs defined in session/format. That package validates
// individual payloads; this package checks history membership and cross-record
// legality. A typed [SessionID] identifies both the Session and its canonical
// history under the same resource key, without another allocation or mapping.
// Occurrence IDs carry their enclosing Session or run scope, but
// reconstructing an ID alone establishes neither membership nor authority.
//
// # Authoring and reconstruction
//
// [New] binds a [Session] to one history and explicit [Config]. Config supplies
// required [Policy], a caller-chosen nonzero writer-instance ID, and an optional
// clock. [Session.BeginCommit] opens a [SessionCommit] for ordered provisional facts;
// [Session.SubmitCommit] submits the batch atomically. [Load] reconstructs accepted
// state from stored history. SessionCommit and Policy document admission, resource
// limits, and submission failure handling.
//
// Records receive signed Unix-nanosecond timestamps on successful local admission;
// commits receive a separate timestamp at first submission preparation. Frozen
// retries preserve exact metadata and bytes. These are diagnostic recording times,
// not action durations, storage receipt times, or an ordering guarantee. Loading
// preserves stored metadata without sampling the clock; subsequent writes use the
// loading writer's Config. History validates supplied metadata but never stamps it.
// Metadata affects commit content identity, not the SessionID or logical
// model-request bytes or identity.
//
// # Inspection and ownership
//
// [Snapshot] pins accepted state. [CommitView] pins a batch's accepted prefix and
// provisional additions; inspection does not accept those additions. Both expose
// chronological records and typed views. Derived inspections, including
// [Snapshot.Usages], document their own scanning and allocation costs.
// [RecordView.Timestamp] exposes recording time for accepted and proposed records;
// [RecordView.Commit] exposes enclosing metadata only for accepted records.
// Captured proposed views never acquire accepted metadata retroactively.
//
// Callers serialize Session operations and keep inputs stable during calls.
// Captured views support concurrent readers independently of the session owner
// and remain pinned across later additions. They retain immutable backing for
// their lifetime; snapshot and commit-view capture do not copy payloads.
//
// Scalar and presence reads borrow backing without cloning sibling payloads.
// Byte getters return views.Bytes; Copy detaches the selected leaf. Optional
// values distinguish absence from explicit zero or empty values. View methods
// document zero values and indexing bounds; MutableClone methods detach the
// selected DTO and its children.
package session
