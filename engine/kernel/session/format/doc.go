//go:generate go -C ../../../.. run ./internal/cborgen/cmd/cborgen -dir engine/kernel/session/format -types Created,UserInput,RunStarted,RunInterrupted -out zz_cbor_generated.go

// Package format owns the isolated session's mutable persistence DTOs and
// payload-local validation. Generated CBOR operations enforce canonical encoding
// and explicit codec limits. Keep borrowed values stable during operations.
// The package has no aggregate, I/O, or default resource policy.
//
// Occurrences and references are nonzero local ordinals. History scope belongs to
// the root history envelope and verified ancestry, never these payloads. Local
// validity proves neither lifecycle legality nor history membership or acceptance.
package format

// Version is the supported version of each lifecycle payload schema.
const Version int64 = 1

const (
	// KindCreated identifies the empty session creation payload.
	KindCreated = "session.created"
	// KindUserInput identifies an opaque input owned by a run.
	KindUserInput = "session.user_input"
	// KindRunStarted identifies a run start, independently of input facts.
	KindRunStarted = "session.run_started"
	// KindRunInterrupted identifies an explicit run interruption.
	KindRunInterrupted = "session.run_interrupted"
)
