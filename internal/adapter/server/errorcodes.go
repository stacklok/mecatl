package server

import (
	"errors"
	"net/http"

	"google.golang.org/grpc/codes"

	"github.com/stacklok/mecatl/engine/port"
)

// An error code is a STABLE OPEN STRING, not an enum value (ADR 0248).
//
// A closed proto enum would make every added code a wire-compat event needing
// codegen and a proto review, and would leave an older client decoding new
// values as UNKNOWN while its exhaustive switch grew a dead branch. Adding a
// server error code should be a minor SDK release. This is the same discipline
// AGENTS.md records for the event taxonomy, where `type`/`stop` are string
// passthroughs rather than enums.
//
// Codes are STABLE ONCE PUBLISHED: a deployed client branches on the exact
// string, so renaming one is a break dressed as a refactor.

// errorCodeEntry maps one sentinel to its stable code and both transports'
// status representations.
//
// Having ONE row own both the gRPC code and the HTTP status is the point. Before
// this registry, `toStatus` (grpc.go) and `writeServiceError` (http.go) were two
// hand-maintained 49-case switches that had to agree by discipline alone. They
// did agree — verified across all 45 shared sentinels when this landed — but
// nothing enforced it, and a new sentinel added to one and forgotten in the
// other would silently report a different class on each transport. That is
// exactly the divergence an SDK promising one normalized error surface cannot
// tolerate.
type errorCodeEntry struct {
	// Sentinel is the error this row classifies, matched with errors.Is.
	Sentinel error
	// Code is the stable machine identifier both transports carry.
	Code string
	// GRPC is the status code the gRPC surface returns.
	GRPC codes.Code
	// HTTPStatus is the status code the HTTP surface returns.
	HTTPStatus int
	// Title is a short, stable, human-readable summary of the code — the RFC
	// 9457 `title` member. It describes the CODE, never a specific occurrence;
	// occurrence-specific text belongs in `detail`.
	Title string
}

// genericErrorEntry is the fallback for an error no registry row matches.
//
// It exists so an unregistered failure DEGRADES to a generic code instead of
// leaking an unmapped internal error string as if it were a contract. A caller
// gets an honest "internal" rather than a code that looks stable but is not.
var genericErrorEntry = errorCodeEntry{
	Sentinel:   nil,
	Code:       "internal",
	GRPC:       codes.Internal,
	HTTPStatus: http.StatusInternalServerError,
	Title:      "Internal error",
}

// errorRegistry is the single source of truth both transports read.
//
// ORDER IS LOAD-BEARING — this is a SLICE, not a map, and classifyError walks it
// in order. Sentinels wrap other sentinels (ErrFailedStepRetryIneligible wraps
// ErrFailedPrecondition), so errors.Is matches BOTH and the more specific row
// must come first. The order below is transcribed verbatim from the toStatus
// switch it replaces, preserving every existing classification exactly. A map
// would randomise iteration and silently reclassify wrapped sentinels.
var errorRegistry = []errorCodeEntry{
	{Sentinel: ErrManagementUnauthorized, Code: "management_unauthorized", GRPC: codes.PermissionDenied, HTTPStatus: http.StatusForbidden, Title: "Management authorization required"},
	{Sentinel: ErrStorageHealthBackend, Code: "storage_health_backend", GRPC: codes.Internal, HTTPStatus: http.StatusInternalServerError, Title: "Storage health unavailable"},
	{Sentinel: ErrMigrationUnsupported, Code: "migration_unsupported", GRPC: codes.Unimplemented, HTTPStatus: http.StatusNotImplemented, Title: "Session migration is not supported"},
	{Sentinel: ErrMigrationConflict, Code: "migration_conflict", GRPC: codes.Aborted, HTTPStatus: http.StatusConflict, Title: "Migration job conflict"},
	{Sentinel: ErrMigrationBackend, Code: "migration_backend", GRPC: codes.Internal, HTTPStatus: http.StatusInternalServerError, Title: "Storage maintenance failed"},
	{Sentinel: ErrCleanupPlanStale, Code: "cleanup_plan_stale", GRPC: codes.Aborted, HTTPStatus: http.StatusConflict, Title: "Cleanup plan is stale"},
	{Sentinel: ErrCleanupUnsupported, Code: "cleanup_unsupported", GRPC: codes.Unimplemented, HTTPStatus: http.StatusNotImplemented, Title: "Session cleanup is not supported"},
	{Sentinel: ErrCleanupBackend, Code: "cleanup_backend", GRPC: codes.Internal, HTTPStatus: http.StatusInternalServerError, Title: "Storage maintenance failed"},
	{Sentinel: ErrStaleRunControl, Code: "stale_run_control", GRPC: codes.Aborted, HTTPStatus: http.StatusConflict, Title: "Control targets a run that is no longer current"},
	{Sentinel: ErrInvalidArgument, Code: "invalid_argument", GRPC: codes.InvalidArgument, HTTPStatus: http.StatusBadRequest, Title: "Invalid argument"},
	{Sentinel: ErrNotFound, Code: "session_not_found", GRPC: codes.NotFound, HTTPStatus: http.StatusNotFound, Title: "Session not found"},
	{Sentinel: ErrTeamNotFound, Code: "team_not_found", GRPC: codes.NotFound, HTTPStatus: http.StatusNotFound, Title: "Team not found"},
	{Sentinel: ErrChildNotFound, Code: "child_not_found", GRPC: codes.NotFound, HTTPStatus: http.StatusNotFound, Title: "Child agent not found or already finished"},
	{Sentinel: ErrLearningUnavailable, Code: "learning_unavailable", GRPC: codes.Unimplemented, HTTPStatus: http.StatusNotImplemented, Title: "Learning proposals are not configured"},
	{Sentinel: ErrProposalConflict, Code: "proposal_conflict", GRPC: codes.Aborted, HTTPStatus: http.StatusConflict, Title: "Proposal conflict"},
	{Sentinel: ErrDreamUnavailable, Code: "dream_unavailable", GRPC: codes.Unimplemented, HTTPStatus: http.StatusNotImplemented, Title: "Manual dream is unavailable"},
	{Sentinel: ErrDreamNotFound, Code: "dream_not_found", GRPC: codes.NotFound, HTTPStatus: http.StatusNotFound, Title: "Dream plan not found"},
	{Sentinel: ErrDreamInProgress, Code: "dream_in_progress", GRPC: codes.Aborted, HTTPStatus: http.StatusConflict, Title: "Dream plan is already in progress"},
	{Sentinel: ErrDreamConflict, Code: "dream_conflict", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusPreconditionFailed, Title: "Dream plan conflict"},
	{Sentinel: ErrDreamTerminalConflict, Code: "dream_terminal_conflict", GRPC: codes.AlreadyExists, HTTPStatus: http.StatusGone, Title: "Dream plan already reached a terminal state"},
	{Sentinel: ErrDreamCapacity, Code: "dream_capacity", GRPC: codes.ResourceExhausted, HTTPStatus: http.StatusTooManyRequests, Title: "Dream capacity exhausted"},
	{Sentinel: ErrDreamGenerateFailed, Code: "dream_generate_failed", GRPC: codes.Internal, HTTPStatus: http.StatusInternalServerError, Title: "Dream plan generation failed"},
	{Sentinel: ErrDreamApplyFailed, Code: "dream_apply_failed", GRPC: codes.Internal, HTTPStatus: http.StatusInternalServerError, Title: "Dream plan application failed"},
	{Sentinel: ErrDreamDeadline, Code: "dream_deadline", GRPC: codes.DeadlineExceeded, HTTPStatus: http.StatusGatewayTimeout, Title: "Dream plan deadline exceeded"},
	{Sentinel: ErrDreamRequestFailed, Code: "dream_request_failed", GRPC: codes.Internal, HTTPStatus: http.StatusInternalServerError, Title: "Dream request failed"},
	{Sentinel: ErrFailedStepRetryIneligible, Code: "failed_step_retry_ineligible", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusConflict, Title: "Failed-step retry is not eligible"},
	{Sentinel: ErrFailedPrecondition, Code: "failed_precondition", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusPreconditionFailed, Title: "Failed precondition"},
	{Sentinel: ErrNoActiveRun, Code: "no_active_run", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusConflict, Title: "No active run for session"},
	{Sentinel: ErrNotAwaitingPlan, Code: "not_awaiting_plan", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusConflict, Title: "Session is not awaiting a plan approval"},
	{Sentinel: ErrSessionLeasedElsewhere, Code: "session_leased_elsewhere", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusConflict, Title: "Session is leased by another process"},
	{Sentinel: ErrUnavailable, Code: "draining", GRPC: codes.Unavailable, HTTPStatus: http.StatusServiceUnavailable, Title: "Server is draining and not accepting new runs"},
	{Sentinel: ErrNoMCPProvider, Code: "no_mcp_provider", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusPreconditionFailed, Title: "No MCP provider configured"},
	{Sentinel: ErrTeamsDisabled, Code: "teams_disabled", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusPreconditionFailed, Title: "Agent teams are not enabled"},
	{Sentinel: ErrTeamRunning, Code: "team_running", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusPreconditionFailed, Title: "Team is already running"},
	{Sentinel: ErrTeamNotRunning, Code: "team_not_running", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusPreconditionFailed, Title: "Team is not running"},
	{Sentinel: ErrTooManyTeams, Code: "too_many_teams", GRPC: codes.ResourceExhausted, HTTPStatus: http.StatusTooManyRequests, Title: "Too many live teams"},
	{Sentinel: ErrTooManySessionEngines, Code: "too_many_session_engines", GRPC: codes.ResourceExhausted, HTTPStatus: http.StatusTooManyRequests, Title: "Too many live per-session engines"},
	{Sentinel: ErrNoScheduleStore, Code: "no_schedule_store", GRPC: codes.Unimplemented, HTTPStatus: http.StatusNotImplemented, Title: "Scheduled tasks are not supported by the configured store"},
	{Sentinel: ErrNoEventLog, Code: "no_event_log", GRPC: codes.Unimplemented, HTTPStatus: http.StatusNotImplemented, Title: "No durable event log configured"},
	// The listener-scoped client-MCP refusal (ADR 0237, Scenario 9). It sits with
	// the two Unimplemented siblings above because it reports the same class of
	// fact: the surface exists in this BUILD but this DEPLOYMENT does not offer it.
	// It is deliberately NOT PermissionDenied — nothing about the CALLER is being
	// judged; the field is simply not accepted here, for any principal.
	{Sentinel: ErrClientMCPUnsupported, Code: "client_mcp_unsupported", GRPC: codes.Unimplemented, HTTPStatus: http.StatusNotImplemented, Title: "Client-provided MCP servers are not accepted on this deployment"},
	// The durable watch surface (ADR 0250). ErrWatchUnsupported sits beside
	// ErrNoEventLog because it is the same class of honest refusal one level in:
	// a log exists, it just cannot serve positions.
	{Sentinel: ErrWatchUnsupported, Code: "watch_unsupported", GRPC: codes.Unimplemented, HTTPStatus: http.StatusNotImplemented, Title: "Durable event watch is not supported by the configured event log"},
	// ResourceExhausted/429 says what actually happened — the bounded delivery
	// buffer ran out — and marks the failure as the client's to retry. It is
	// RESUMABLE: reconnect with the last cursor received.
	{Sentinel: ErrWatchLagging, Code: "watch_lagging", GRPC: codes.ResourceExhausted, HTTPStatus: http.StatusTooManyRequests, Title: "Watch terminated because the client fell behind"},
	// DataLoss is the one code that means what a gap means: events that should
	// have been recorded were not. A retry does not recover them, so this is
	// reported rather than dressed up as a transient fault.
	{Sentinel: ErrActivityGap, Code: "activity_gap", GRPC: codes.DataLoss, HTTPStatus: http.StatusInternalServerError, Title: "Durable event-log append failed; the watch has a delivery gap"},
	// The two cursor faults are port-level sentinels, classified here so both
	// transports report them identically. Expired is RECOVERABLE by restarting
	// from the beginning (the log's positional basis moved); malformed indicates a
	// bug or tampering and must not be silently retried, which is why they are
	// distinct codes rather than one "bad cursor".
	//
	// TRANSPORT NOTE for the watch route: these two HTTP statuses are NOT
	// observable on GET /v1/sessions/{id}/watch. watchLog validates the feature,
	// the cursor seam and ownership eagerly, but the cursor itself is decoded
	// inside log.ReadAfter — which runs after the 200 has been committed — so over
	// SSE a bad cursor is always a 200 plus a stream-terminal `event: error` frame
	// carrying the code below. gRPC is unaffected: toStatus fires before any Send.
	// The statuses stay registered because they ARE the right mapping wherever a
	// cursor fault can be raised before the first byte, and because a code's
	// transport pairing is a property of the sentinel, not of one route.
	{Sentinel: port.ErrCursorExpired, Code: "cursor_expired", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusConflict, Title: "Event-log cursor is from a superseded log generation"},
	{Sentinel: port.ErrCursorMalformed, Code: "cursor_malformed", GRPC: codes.InvalidArgument, HTTPStatus: http.StatusBadRequest, Title: "Event-log cursor is malformed"},
	{Sentinel: ErrSessionDeleteUnsupported, Code: "session_delete_unsupported", GRPC: codes.Unimplemented, HTTPStatus: http.StatusNotImplemented, Title: "Session deletion is not supported by the configured store"},
	{Sentinel: port.ErrSessionMetadataCursorRestart, Code: "session_metadata_cursor_restart", GRPC: codes.Aborted, HTTPStatus: http.StatusConflict, Title: "Session metadata cursor must restart"},
	{Sentinel: port.ErrSessionMetadataPagingUnsupported, Code: "session_metadata_paging_unsupported", GRPC: codes.Unimplemented, HTTPStatus: http.StatusNotImplemented, Title: "Session metadata paging is not supported"},
	{Sentinel: ErrSchedulerNotRunning, Code: "scheduler_not_running", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusPreconditionFailed, Title: "Scheduler is not running"},
	{Sentinel: ErrScheduleDisabled, Code: "schedule_disabled", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusPreconditionFailed, Title: "Schedule is disabled"},
	{Sentinel: ErrScheduleExhausted, Code: "schedule_exhausted", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusPreconditionFailed, Title: "One-shot schedule already fired"},
	{Sentinel: ErrFireNowOverlap, Code: "fire_now_overlap", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusPreconditionFailed, Title: "Fire-now skipped, a prior fire is still running"},
	{Sentinel: ErrScheduleNotLeader, Code: "schedule_not_leader", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusPreconditionFailed, Title: "Not the scheduler leader"},
	{Sentinel: port.ErrScheduleNotFound, Code: "schedule_not_found", GRPC: codes.NotFound, HTTPStatus: http.StatusNotFound, Title: "Schedule not found"},
	{Sentinel: port.ErrScheduleUnsupported, Code: "schedule_unsupported", GRPC: codes.Unimplemented, HTTPStatus: http.StatusNotImplemented, Title: "Schedules are not supported by the configured store"},
	{Sentinel: ErrInternal, Code: "internal", GRPC: codes.Internal, HTTPStatus: http.StatusInternalServerError, Title: "Internal error"}}

// classifyError returns the registry row for err, or genericErrorEntry when no
// row matches. It walks errorRegistry IN ORDER; see the ordering note above.
func classifyError(err error) errorCodeEntry {
	for _, e := range errorRegistry {
		if errors.Is(err, e.Sentinel) {
			return e
		}
	}
	return genericErrorEntry
}

// entryForHTTPStatus derives a registry-shaped entry from a bare HTTP status.
//
// It serves writeError's ~40 call sites: request-shape failures raised inside a
// handler (malformed JSON, a missing required field, an oversized body) that
// carry a status but no service sentinel. Deriving the code from the status
// keeps every response machine-readable without inventing a distinct code per
// call site — those failures are genuinely the same class, and a code per call
// site would be a vocabulary nobody could enumerate.
//
// An unmapped status degrades to genericErrorEntry's code while KEEPING the
// caller's status: the handler knew the right HTTP semantics even when we have
// no name for the condition, so overriding it with 500 would be a regression.
func entryForHTTPStatus(status int) errorCodeEntry {
	if e, ok := httpStatusEntries[status]; ok {
		return e
	}
	return errorCodeEntry{
		Code:       genericErrorEntry.Code,
		GRPC:       genericErrorEntry.GRPC,
		HTTPStatus: status,
		Title:      genericErrorEntry.Title,
	}
}

// httpStatusEntries maps the statuses writeError's call sites actually use to a
// stable code. It is a MAP (not the ordered slice) because a status is an exact
// key, with none of the wrapped-sentinel specificity ordering errorRegistry has.
var httpStatusEntries = map[int]errorCodeEntry{
	http.StatusBadRequest:            {Code: "invalid_argument", GRPC: codes.InvalidArgument, HTTPStatus: http.StatusBadRequest, Title: "Invalid argument"},
	http.StatusNotFound:              {Code: "not_found", GRPC: codes.NotFound, HTTPStatus: http.StatusNotFound, Title: "Not found"},
	http.StatusRequestEntityTooLarge: {Code: "request_too_large", GRPC: codes.InvalidArgument, HTTPStatus: http.StatusRequestEntityTooLarge, Title: "Request body too large"},
	http.StatusForbidden:             {Code: "management_unauthorized", GRPC: codes.PermissionDenied, HTTPStatus: http.StatusForbidden, Title: "Management authorization required"},
	http.StatusConflict:              {Code: "conflict", GRPC: codes.Aborted, HTTPStatus: http.StatusConflict, Title: "Conflict"},
	http.StatusPreconditionFailed:    {Code: "failed_precondition", GRPC: codes.FailedPrecondition, HTTPStatus: http.StatusPreconditionFailed, Title: "Failed precondition"},
	http.StatusNotImplemented:        {Code: "unimplemented", GRPC: codes.Unimplemented, HTTPStatus: http.StatusNotImplemented, Title: "Not implemented"},
	http.StatusServiceUnavailable:    {Code: "draining", GRPC: codes.Unavailable, HTTPStatus: http.StatusServiceUnavailable, Title: "Server is draining and not accepting new runs"},
	http.StatusTooManyRequests:       {Code: "resource_exhausted", GRPC: codes.ResourceExhausted, HTTPStatus: http.StatusTooManyRequests, Title: "Resource exhausted"},
	http.StatusInternalServerError:   genericErrorEntry,
}
