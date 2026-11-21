//go:generate go -C ../../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/session/format -emit cbor,clone,equal -out zz_cbor_generated.go

// Package format defines mutable persistence DTOs and payload-local validation
// for isolated kernel sessions. The enclosing history record supplies each
// payload's kind and schema version.
//
// Occurrence IDs and their references store relative components; field comments
// define their enclosing Session or run scope. Payloads omit the enclosing Session
// identity, which is also the history's resource key. The session aggregate checks
// membership and cross-record legality; payload validity alone establishes neither
// acceptance nor execution authority.
//
// Public Validate methods check payload contracts, including child values.
// Generated CBOR methods implement canonical encoding; kernel/codec helpers apply
// explicit per-operation resource limits. This package supplies no default
// resource policy.
//
// Assignment shares mutable storage. Generated Clone detaches it; Equal compares
// exact values, treating nil and empty slices alike but preserving pointer presence.
// Generated reads own decoded storage and leave receivers unchanged on failure.
// Inputs must remain stable for the duration of an operation.
package format

// Version is the supported version of all session payload schemas.
const Version int64 = 1

const (
	// KindCreated identifies the empty session creation payload.
	KindCreated = "session.created"
	// KindUserInput identifies one authored submission owned by a run.
	KindUserInput = "session.user_input"
	// KindEffectiveInput identifies an ordered incremental input contribution.
	KindEffectiveInput = "session.effective_input"
	// KindRootInstructions identifies a session-scoped harness root-contract replacement.
	KindRootInstructions = "session.root_instructions"
	// KindUserInstructions identifies a session-scoped prepared user-guidance replacement.
	KindUserInstructions = "session.user_instructions"
	// KindToolCatalog identifies a session-scoped advertised catalog replacement.
	KindToolCatalog = "session.tool_catalog"
	// KindModelBinding identifies a session-scoped declared model binding.
	KindModelBinding = "session.model_binding"
	// KindRunStarted identifies a run start, independently of input facts.
	KindRunStarted = "session.run_started"
	// KindRunEnded identifies explicit terminal run ownership.
	KindRunEnded = "session.run_ended"
	// KindModelRequest identifies an invocation commitment.
	KindModelRequest = "session.model_request"
	// KindModelResponse identifies a complete terminal response.
	KindModelResponse = "session.model_response"
	// KindUsage identifies a complete usage report with first-record definition.
	KindUsage = "session.usage"
	// KindInvocationFailure identifies an observed invocation failure.
	KindInvocationFailure = "session.invocation_failure"
	// KindToolInvocation identifies effective committed tool intent.
	KindToolInvocation = "session.tool_invocation"
	// KindToolDisposition identifies an explicit requested-call outcome.
	KindToolDisposition = "session.tool_disposition"
)
