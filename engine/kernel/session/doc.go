// Package session interprets accepted history as lifecycle and retained context
// for the isolated kernel. It validates and submits ordered batches through a
// bound History; it does not execute work or resolve retained references.
//
// UserInput retains one authored submission as ordered parts. Effective inputs
// append ordered whole-submission references or inline content with logical Actor,
// Purpose and Source, never provider roles or trust claims. Empty and duplicate
// contributions are retained without replacement, consumption or fallback.
// Instructions, tool catalogs, and model
// bindings are independent history-scoped replacements admitted during active runs;
// their selected values persist across runs. Snapshots pin these selections.
//
// Public values carry history scope. Persistence DTOs in session/format omit
// context supplied by verified history ancestry. Payload-local validation and
// aggregate lifecycle legality are separate responsibilities.
//
// SessionCommit collects provisional facts for [Session.SubmitCommit]. Submission,
// Apply, and Load share the decoded application path, publishing accepted state.
// Session operations must be caller-serialized; captured snapshots and their
// child handles support concurrent readers independently of the session owner.
// Views retain immutable backing storage for their lifetime, potentially including
// the enclosing accepted record. Capture and child navigation cost O(1), without
// copying payloads. Byte getters return independent field-only copies; Record and
// RecordView.Format explicitly copy encoded history records for transport.
//
// View sequences are immutable, ordered and repeatable, and retain their captured
// views' backing storage and bounds without copying it. Retention is not a payload
// copy. Direct traversal can avoid allocations, but opaque iterator consumption
// may allocate per traversal, accumulating per block in nested consumers. Indexed
// access remains available for strict allocation budgets; collecting sequences
// and exporting owned byte fields allocate independently of traversal.
// See [SessionCommit], [Policy], and [Snapshot] for admission and ownership contracts.
package session
