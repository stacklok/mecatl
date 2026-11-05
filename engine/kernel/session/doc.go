// Package session interprets accepted history as lifecycle state for the isolated
// kernel. It validates and submits ordered batches through a bound History;
// it does not execute work.
//
// Public values carry history scope. Persistence DTOs in session/format omit
// context supplied by verified history ancestry. Payload-local validation and
// aggregate lifecycle legality are separate responsibilities.
//
// SessionCommit collects provisional facts for [Session.SubmitCommit]. Submission,
// Apply, and Load share the decoded application path, publishing accepted state.
// Session operations must be caller-serialized; captured snapshots and their
// child handles support concurrent readers independently of the session owner.
// See [SessionCommit], [Policy], and [Snapshot] for admission and ownership contracts.
package session
