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

// Version is the supported version of unchanged lifecycle payload schemas.
const Version int64 = 1

const (
	// UserInputVersion replaces the opaque v1 payload with authored messages.
	UserInputVersion int64 = 2
	// EffectiveInputVersion is the supported prepared-input payload version.
	EffectiveInputVersion int64 = 1
	// InstructionsVersion is the supported instruction replacement version.
	InstructionsVersion int64 = 1
	// ToolCatalogVersion is the supported advertised catalog replacement version.
	ToolCatalogVersion int64 = 1
	// ModelBindingVersion is the supported declared model binding version.
	ModelBindingVersion int64 = 1
)

const (
	// KindCreated identifies the empty session creation payload.
	KindCreated = "session.created"
	// KindUserInput identifies ordered authored messages owned by a run.
	KindUserInput = "session.user_input"
	// KindEffectiveInput identifies a complete prepared revision of authored input.
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
