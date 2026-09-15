package server

import (
	"errors"
	"fmt"

	"github.com/stacklok/mecatl/engine/port"
)

// Sentinel errors the service returns; the gRPC and HTTP adapters map these to
// their respective status codes (codes.InvalidArgument / NotFound, HTTP 400 /
// 404).
var (
	// ErrManagementUnauthorized is deliberately resource-free: an unauthorized
	// caller learns neither backend support nor aggregate storage scope.
	ErrManagementUnauthorized = errors.New("server: management authorization required")
	// ErrMaintenanceExclusionUnavailable reports that destructive storage
	// maintenance cannot prove active-session exclusion in this deployment.
	ErrMaintenanceExclusionUnavailable = errors.New("server: storage maintenance exclusion unavailable")
	// ErrStorageHealthBackend is the sanitized aggregate-health backend failure.
	ErrStorageHealthBackend = errors.New("server: storage health unavailable")
	// ErrMigrationUnsupported reports that the configured session store has no
	// physical v1-to-v2 maintenance capability.
	ErrMigrationUnsupported = errors.New("server: session migration is not supported")
	// ErrMigrationConflict reports an invalid durable job transition.
	ErrMigrationConflict = errors.New("server: migration job conflict")
	// ErrMigrationBackend is the only caller-visible backend failure. Raw paths,
	// records, and backend error strings stay behind the adapter boundary.
	ErrMigrationBackend = errors.New("server: storage maintenance failed")
	// ErrCleanupPlanStale reports that catalog candidates, scope, or policy changed
	// after dry-run. No item is deleted from a stale plan.
	ErrCleanupPlanStale = errors.New("server: cleanup plan is stale")
	// ErrCleanupUnsupported honestly reports a backend without indexed pruning.
	ErrCleanupUnsupported = errors.New("server: session cleanup is unsupported")
	// ErrCleanupBackend is the sanitized stable maintenance failure.
	ErrCleanupBackend = errors.New("server: storage maintenance failed")
	// ErrStaleRunControl is returned when a control (approve / cancel / steer)
	// carries an expected_run_id that does NOT name the run it would affect
	// (ADR 0249). The control is refused and the current run is left untouched.
	//
	// It is a PRECONDITION-class failure, not a bad request: the request is
	// well-formed and the caller's belief was simply overtaken by events — the run
	// they meant to act on has already ended and another has begun. Adapters map
	// it to Aborted / HTTP 409 Conflict, alongside the other
	// you-lost-a-race sentinels (ErrMigrationConflict, ErrProposalConflict), so a
	// client can distinguish "retry against the current run" from "fix your
	// arguments".
	ErrStaleRunControl = errors.New("server: control targets a run that is no longer current")
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
	// ErrFailedStepRetryIneligible is the stable precondition sentinel returned when a
	// session cannot retry its failed model step from persisted conversation state.
	// Eligibility is based only on typed persisted state.
	ErrFailedStepRetryIneligible = fmt.Errorf("%w: failed-step retry is not eligible", ErrFailedPrecondition)
	// ErrFailedPrecondition signals the request is well-formed but the server is
	// in a state that forbids it — typically a server-side misconfiguration the
	// client cannot fix by changing its arguments (e.g. spawning a Mutating team
	// member when no EnvironmentForker is wired, or a member catalog that violates
	// the read-only-share invariant). Adapters map it to FailedPrecondition /
	// HTTP 412, distinguishing it from a bad request (ErrInvalidArgument).
	ErrFailedPrecondition = errors.New("server: failed precondition")
	// ErrLearningUnavailable means reflection/proposal persistence is not wired.
	ErrLearningUnavailable = errors.New("server: learning proposals are not configured")
	// ErrAttemptVersionConflict reports an opaque expected-version CAS mismatch.
	ErrAttemptVersionConflict = errors.New("server: learning attempt version conflict")
	// ErrAttemptTerminalConflict reports a lifecycle state that cannot perform the requested control.
	ErrAttemptTerminalConflict = errors.New("server: learning attempt terminal conflict")
	// ErrAttemptLiveClaimConflict reports an attempt currently fenced by a live worker claim.
	ErrAttemptLiveClaimConflict = errors.New("server: learning attempt has a live claim")
	// ErrReflectionCancelled reports caller cancellation during explicit reflection.
	ErrReflectionCancelled = errors.New("server: reflection cancelled")
	// ErrReflectionDeadline reports an explicit reflection deadline.
	ErrReflectionDeadline = errors.New("server: reflection timed out")
	// ErrReflectionQueueFull reports bounded coordinator admission exhaustion.
	ErrReflectionQueueFull = errors.New("server: reflection queue is full")
	// ErrReflectionFailed is the sanitized typed boundary for provider or persistence faults.
	ErrReflectionFailed = errors.New("server: reflection service failed")
	// ErrProposalConflict reports a stale proposal version or invalid lifecycle transition.
	ErrProposalConflict = errors.New("server: proposal conflict")
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
	// ErrUnavailable is returned by the run-entry funnel (acquireLease, covering
	// StartRunContent + resumeFromAwaiting) when the server is DRAINING — it has
	// been asked to stop accepting new runs (mecak8s graceful shutdown, ADR 0048).
	// A drained run-entry is rejected before leasing/launching so a rolling update
	// steers new traffic to a survivor. In-flight runs are cancelled (not drained
	// to completion); a same-process Approve on a LIVE run is NOT a new run-entry
	// and stays allowed (it delivers a verdict to an already-running run). Adapters
	// map it to Unavailable / HTTP 503. The gate starts false (byte-identical
	// default); Service.Drain arms it.
	ErrUnavailable = errors.New("server: draining, not accepting new runs")
	// ErrContextWindowUnavailable is a transient admission failure: running with
	// the speculative floor could irreversibly compact valid persisted history.
	ErrContextWindowUnavailable = errors.New("server: context window unavailable")
	// ErrNotAwaitingPlan is returned by ApprovePlan when the session is not parked
	// awaiting a PLAN-ORIGINATED permission ask (issue #206, Wave 4): either a run
	// is LIVE for the session (an approve mid-run — use the Converse ResumeApproval
	// frame for a live run), the session is not in StateAwaiting, or its pending
	// ask is a generic tool-permission ask rather than the plan-approval gate's
	// PresentPlan signalling call. It is a PRECONDITION failure (the session exists
	// and is well-formed, it is just not in the state this atomic RPC requires),
	// NOT a bad request. Adapters map it to FailedPrecondition / HTTP 409 Conflict
	// (distinct from ErrNoActiveRun's "known session, no live run" — here the
	// session may well be live, just not awaiting a plan ask).
	ErrNotAwaitingPlan = errors.New("server: session is not awaiting a plan approval")
	// Schedule-surface sentinels (internal/adapter/server/schedule.go). Defined
	// here so toStatus/writeServiceError map them in the one error-classification
	// chokepoint alongside the team/session sentinels.
	// ErrNoScheduleStore signals that no ScheduleStore is available — the
	// configured store backend does not expose one. Adapters map it to
	// Unimplemented / HTTP 501.
	ErrNoScheduleStore = errors.New("server: scheduled tasks are not supported by the configured store")
	// ErrNoEventLog signals that no durable EventLog (cloud-native Phase 3a,
	// port.EventLog) is configured — the configured store backend does not expose
	// one. It is the EventLog analogue of ErrNoScheduleStore:
	// StreamSessionEvents returns it so the wire adapters map to UNIMPLEMENTED
	// (HTTP 501), honestly reporting that the read-back surface is absent rather
	// than pretending an unknown id. ListSessions does NOT use it (it degrades to
	// an empty list via PrunableStore instead).
	ErrNoEventLog = errors.New("server: no durable event log configured")
	// ErrClientMCPUnsupported means this DEPLOYMENT does not accept
	// client-provided MCP servers on session creation (ADR 0237's listener-scoped
	// authority, applied to outbound MCP). It is the deployment's refusal, not the
	// build's: the RPC and the field exist, this deployment just does not offer
	// them, exactly as ErrNoEventLog reports a wired-storage fact one level up.
	// Both map to UNIMPLEMENTED / 501 for that reason, and a client that wants to
	// know BEFORE it asks reads mcp_servers_on_create from GetCompatibilityInfo.
	ErrClientMCPUnsupported = errors.New("server: client-provided MCP servers are not accepted on this deployment")
	// ErrClientMCPUnreachable means the deployment DID accept the request but at
	// least one requested MCP server could not be connected, so the session was
	// not created. It is the counterpart of ErrClientMCPUnsupported and a
	// deliberately DIFFERENT code: "this deployment refuses the field" is
	// permanent and a client should stop asking, while "your server did not
	// answer" is transient and retryable once the client's own endpoint is up.
	// Collapsing them into one code would make an SDK unable to tell a
	// misconfigured deployment from a sleeping sidecar.
	//
	// Creation is ALL-OR-NOTHING on the wire for the reason this sentinel exists:
	// a partially-mounted session is one the client cannot detect, since the
	// unreachable-server WARN goes to the operator's log and the create otherwise
	// returns a perfectly ordinary session id.
	ErrClientMCPUnreachable = errors.New("server: a requested client-provided MCP server could not be connected")
	// ErrSessionDeleteUnsupported means the configured store cannot physically
	// remove snapshots and their sidecars.
	ErrSessionDeleteUnsupported = errors.New("server: session deletion is not supported by the configured store")
	// ErrSchedulerNotRunning is returned by FireNow when a ScheduleStore IS
	// available (Create/Get/List/etc. all work) but no scheduler.Scheduler is
	// wired on this process (s.scheduler == nil — e.g. --scheduler was not
	// passed, or this is a store-only replica). It is distinct from
	// ErrNoScheduleStore (which means the STORE itself cannot hold schedules at
	// all): here the schedule exists and is well-formed, there is just no
	// in-process scheduler to drive a manual fire. Adapters map it to
	// FailedPrecondition / HTTP 412, the same class as ErrScheduleDisabled.
	ErrSchedulerNotRunning = errors.New("server: scheduler is not running")
	// ErrScheduleDisabled is returned by FireNow when the schedule is not enabled
	// (paused or done). It wraps scheduler.ErrFireNowDisabled. Adapters map it to
	// FailedPrecondition / HTTP 412.
	ErrScheduleDisabled = errors.New("server: schedule is disabled")
	// ErrFireNowOverlap is returned by FireNow when the singleton overlap check
	// found a prior fire still running. It wraps scheduler.ErrFireNowOverlap AND
	// port.ErrFireNowOverlap (the port-level sentinel a layer that may not import
	// this adapter — e.g. engine/agent's Schedule tool — matches via errors.Is,
	// the same create-seam default-true Singleton holding through every surface).
	// Adapters map it to FailedPrecondition / HTTP 412 (the schedule exists and
	// is well-formed, it is just running — the same precondition-failed class as
	// ErrScheduleDisabled; the two surfaces agree).
	ErrFireNowOverlap = fmt.Errorf("server: fire-now skipped (prior fire still running): %w", port.ErrFireNowOverlap)
	// ErrScheduleExhausted is returned by FireNow when a one-shot schedule has
	// already fired (FireCount > 0). It wraps scheduler.ErrFireNowExhausted.
	// Adapters map it to FailedPrecondition / HTTP 412.
	ErrScheduleExhausted = errors.New("server: one-shot schedule already fired")
	// ErrScheduleNotLeader is returned by FireNow when this replica is not the
	// scheduler leader (a multi-replica deployment where a peer holds the
	// `__scheduler__` lease). It wraps scheduler.ErrNotLeader. Adapters map it
	// to FailedPrecondition / HTTP 412; the message names the current leader
	// (when known) so a client can redirect.
	ErrScheduleNotLeader = errors.New("server: not the scheduler leader")
)
