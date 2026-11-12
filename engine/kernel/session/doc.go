//go:generate go -C ../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/session -emit view -out zz_views_generated.go

// Package session interprets accepted history as lifecycle and retained context
// for the isolated kernel. It validates and submits ordered batches through a
// bound History; it does not execute work or resolve retained references.
//
// Payloads are mutable DTOs defined in session/format. Payload-local validation
// is separate from aggregate lifecycle legality. Accepted views reconstruct
// scoped identities from verified history ancestry; identity reconstruction alone
// proves neither allocation nor membership.
//
// [SessionCommit] collects provisional facts for ordered, atomic acceptance through
// [Session.SubmitCommit]. [Load] reconstructs accepted state from stored history.
// See [SessionCommit] and [Policy] for admission and resource contracts.
//
// Session operations must be caller-serialized. Captured [Snapshot] values and
// their child views support concurrent readers independently of the session owner.
// Views retain immutable backing for their lifetime, potentially including the
// enclosing accepted record. Snapshot capture and direct payload navigation take
// O(1) time without copying payloads; derived lookups document their own costs.
// Byte getters borrow through views.Bytes; Copy detaches only the selected leaf.
// Scalar and presence reads never clone sibling payloads. Zero views are safe;
// optional children and scalars report presence separately, preserving explicit
// zero and empty values. Sequences are ordered, repeatable, and pinned to captured
// backing and bounds. [RecordView.Format] exports a detached transport DTO.
package session
