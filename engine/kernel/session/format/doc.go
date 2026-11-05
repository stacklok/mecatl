//go:generate go -C ../../../.. run ./internal/cborgen/cmd/cborgen -dir engine/kernel/session/format -types Created,UserInput,RunStarted,RunInterrupted,EffectiveInput,Instructions,ToolCatalog,ModelBinding -out zz_cbor_generated.go

// Package format owns the isolated session's mutable persistence DTOs and
// payload-local validation. Generated CBOR operations enforce canonical encoding
// and explicit codec limits. Keep borrowed values stable during operations.
// The package has no aggregate, I/O, or default resource policy. Validate methods
// check local invariants only; generated traversal bounds and validates children
// before their parents. Direct Validate calls do not impose operational bounds.
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
	// KindRunInterrupted identifies an explicit run interruption.
	KindRunInterrupted = "session.run_interrupted"
)
