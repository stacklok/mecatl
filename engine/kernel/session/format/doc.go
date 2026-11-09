//go:generate go -C ../../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/session/format -emit cbor,clone,equal -out zz_cbor_generated.go

// Package format owns the isolated session's mutable persistence DTOs and
// payload-local validation. Generated CBOR operations enforce canonical encoding
// and explicit codec limits. Keep borrowed values stable during operations.
// The package has no aggregate, I/O, or default resource policy. Public composite
// Validate methods recursively check children, without operational bounds. Scalar
// validators check immediate invariants. Generated traversal bounds and validates
// children before invoking each parent's immediate codec hook;
// it does not invoke recursive public validators.
//
// Assignment shares mutable storage. Generated Clone detaches it; Equal compares
// exact values, treating nil and empty slices alike but preserving pointer presence.
// Generated reads own their storage and leave receivers unchanged on failure.
//
// Occurrences and references are nonzero local ordinals. History scope belongs to
// the root history envelope and verified ancestry, never these payloads. Local
// validity proves neither lifecycle legality nor history membership or acceptance.
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
	// KindInstructions identifies a session-scoped instruction replacement.
	KindInstructions = "session.instructions"
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
	// KindInvocationFailure identifies an observed invocation failure.
	KindInvocationFailure = "session.invocation_failure"
)
