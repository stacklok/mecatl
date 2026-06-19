package server

import "errors"

// Sentinel errors the service returns; the gRPC and HTTP adapters map these to
// their respective status codes (codes.InvalidArgument / NotFound, HTTP 400 /
// 404).
var (
	// ErrInvalidArgument signals a malformed or missing required field.
	ErrInvalidArgument = errors.New("server: invalid argument")
	// ErrNotFound signals an unknown session id.
	ErrNotFound = errors.New("server: session not found")
	// ErrTeamNotFound signals an unknown team id. It is distinct from ErrNotFound
	// (whose message names a session) so a team lookup reports a team-appropriate
	// message rather than "session not found: team X". Adapters map it to the same
	// codes.NotFound / HTTP 404 as ErrNotFound.
	ErrTeamNotFound = errors.New("server: team not found")
	// ErrChildNotFound signals a CancelChild for a child id the session's
	// in-flight run does not hold live — unknown, or already finished (the
	// finished-as-you-pressed race). The wording is FAMILY-NEUTRAL ("child
	// agent", never "subagent"): the same error will cover team-member and
	// parallel-branch ids once their cancel wiring lands. Distinct from
	// ErrNotFound (whose message names a session) so the HTTP /cancel-child
	// mirror reports a child-appropriate message; adapters map it to the same
	// codes.NotFound / HTTP 404.
	ErrChildNotFound = errors.New("server: child agent not found or already finished")
	// ErrNoMCPProvider signals that an MCP inspection RPC requiring a live
	// provider (ReadMcpResource / GetMcpPrompt) was called but no MCP provider
	// is configured. Adapters map it to FailedPrecondition / HTTP 412.
	ErrNoMCPProvider = errors.New("server: no MCP provider configured")
	// ErrFailedPrecondition signals the request is well-formed but the server is
	// in a state that forbids it — typically a server-side misconfiguration the
	// client cannot fix by changing its arguments (e.g. spawning a Mutating team
	// member when no WorkspaceForker is wired, or a member catalog that violates
	// the read-only-share invariant). Adapters map it to FailedPrecondition /
	// HTTP 412, distinguishing it from a bad request (ErrInvalidArgument).
	ErrFailedPrecondition = errors.New("server: failed precondition")
	// ErrInternal signals a server-side fault that is NOT the client's fault — a
	// transport/protocol error talking to a downstream (e.g. an MCP server that
	// is connected but errors a read). Adapters map it to Internal / HTTP 500,
	// distinguishing it from a bad request (ErrInvalidArgument). It exists so the
	// MCP read methods can keep an unknown-server name as InvalidArgument while a
	// genuine fault on a known server is reported as a server error.
	ErrInternal = errors.New("server: internal error")
	// ErrTooManySessionEngines is returned by createSession when the per-session
	// engine registry is already at Config.MaxSessionEngines. It bounds the memory
	// growth (CWE-770) from per-session engines created (by a client-MCP session OR a
	// non-default provider/model selector) but never released via CloseSession /
	// EndSession — the gRPC/HTTP surfaces have no connection-teardown drain, so a
	// hostile authed client could otherwise grow the map unbounded. Releasing a
	// session frees a slot. Adapters map it to ResourceExhausted / HTTP 429, mirroring
	// ErrTooManyTeams.
	ErrTooManySessionEngines = errors.New("server: too many live per-session engines")
	// ErrSessionLeasedElsewhere is returned by the run-entry funnel
	// (StartRunContent / resumeFromAwaiting) when a cross-process session lease
	// (cloud-native Phase 4, ADR 0027) for the id is held by a DIFFERENT, still-live
	// process: in a multi-replica deployment another replica owns this session, so
	// this one must NOT drive it (the single-writer invariant). It is the
	// composition-side surfacing of port.ErrLeaseHeld at the run-entry gate.
	// Adapters map it to FailedPrecondition / HTTP 409 Conflict — distinct from
	// ErrNoActiveRun: the session exists and is well-formed, it is just owned
	// elsewhere right now (a later retry, after the holder releases or its lease
	// lapses, can succeed). Only ever returned when a SessionLease is wired.
	ErrSessionLeasedElsewhere = errors.New("server: session is leased by another process")
)
