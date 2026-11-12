package session

import (
	"context"
	"errors"
	"math"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

// ErrBatch identifies an invalid, closed, frozen or already occupied batch slot.
var ErrBatch = errors.New("session batch unavailable")

// ErrReloadRequired means this object cannot write again; construct a freshly
// loaded Session. It does not imply that an uncertain proposal was rejected.
var ErrReloadRequired = errors.New("session requires fresh load")

// ErrConflict means History definitively rejected this submission as a conflict.
var ErrConflict = errors.New("session commit conflict; fresh load required")

// ErrRetryExpired means acceptance can no longer be adjudicated by retry. It is
// not a rejection verdict; a fresh load is required.
var ErrRetryExpired = errors.New("session commit retry expired; fresh load required")

// ErrAcceptanceUnknown means an append attempt has no known acceptance verdict.
// The exact frozen batch retains the slot for explicit retry; a replacement write
// is unsafe. Underlying causes remain available through errors.Is and errors.As.
var ErrAcceptanceUnknown = errors.New("session append acceptance unknown")

// ErrAcceptedApply means storage accepted the returned nonzero reference, but
// local application failed. Do not resubmit; a fresh load is required.
var ErrAcceptedApply = errors.New("session commit accepted but local application failed; fresh load required")

// SessionCommit is a single-session, single-owner ordered batch. Only the exact
// pointer returned by BeginCommit is usable: value copies are invalid handles.
// Additions own encoded bytes and complete decoded facts, not caller slices.
// Provisional IDs are not accepted until SubmitCommit succeeds. Additions eagerly
// enforce payload limits and lifecycle legality, but promise no aggregate admission.
// Typed additions validate the live handle, then explicit destination scope, then
// zero-only synthesized DTO fields before inspecting or sizing payloads. Only a
// bounded local DTO copy receives persisted identity; caller backing is not mutated.
// AddUserInput and AddModelRequest instead require the exact active run first,
// returning ErrTransition on mismatch. Other additions return codec.ErrInvalidData
// for invalid top-level identities and ErrIdentity for valid foreign histories,
// even when their payload is malformed or oversized.
// Complete submission checks commit/cumulative limits and closeout room. Zero is unusable.
type SessionCommit struct { //nolint:revive // Explicit name distinguishes a provisional session batch from history.Commit.
	owner     *Session
	records   []*materializedRecord
	admission admissionContext
	frozen    bool
	completed bool
	proposal  prepared
}

// BeginCommit reserves the sole batch slot without creating or persisting facts.
// An open or uncertain batch blocks another batch. Reload-required objects cannot
// begin batches. Operations on this object must be caller-serialized.
func (s *Session) BeginCommit() (*SessionCommit, error) {
	if err := s.writeBarrier(); err != nil {
		return nil, err
	}
	if err := s.policy.Validate(); err != nil {
		return nil, err
	}
	if s.history == nil {
		return nil, ErrIdentity
	}
	b := &SessionCommit{owner: s, admission: newAdmission(s.state.view)}
	s.batch = b
	return b, nil
}

// writeBarrier prevents new proposals while a batch owns the slot or a fresh
// load is required. Internal reserve candidates need no bound History or slot.
func (s *Session) writeBarrier() error {
	if s.reload {
		return ErrReloadRequired
	}
	if s.batch != nil {
		return ErrBatch
	}
	return nil
}

// check requires the original live handle; frozen handles are only resubmittable.
func (b *SessionCommit) check(add bool) error {
	if b == nil || b.owner == nil || b.owner.batch != b || b.completed || (add && b.frozen) {
		return ErrBatch
	}
	if b.owner.reload {
		return ErrReloadRequired
	}
	return nil
}

// add retains owner-validated backing and validates its local transition. Typed
// callers check explicit destination and synthesized fields before materialization.
// Aggregate policy
// is checked only on submission; this trusted in-process builder is not a sandbox.
func (b *SessionCommit) add(r *materializedRecord, err error) error {
	if err != nil {
		return err
	}
	if r == nil {
		return codec.ErrInvalidData
	}
	if len(b.records) == math.MaxInt {
		return codec.ErrLimitExceeded
	}
	candidate := b.admission
	if err := candidate.admit(b.owner.state.view.historyID, r); err != nil {
		return err
	}
	b.records = append(b.records, r)
	b.admission = candidate
	return nil
}

// Create adds the first and unique creation fact. Construction and BeginCommit
// never create it implicitly. Failed additions leave earlier facts unchanged.
func (b *SessionCommit) Create() error {
	if err := b.check(true); err != nil {
		return err
	}
	return b.add(newCreatedRecord(Created{}, b.owner.policy.PayloadLimits))
}

// AddUserInput allocates a provisional identity in the explicitly named active
// run. Incoming RunID and ID must be zero, even if matching the destination.
// It owns encoded content before returning; failure consumes no ID or slot.
func (b *SessionCommit) AddUserInput(run RunID, request UserInput) (UserInputID, error) {
	if err := b.check(true); err != nil {
		return UserInputID{}, err
	}
	if run == (RunID{}) || run != b.admission.candidate.activeRun {
		return UserInputID{}, ErrTransition
	}
	if request.RunID != 0 || request.ID != 0 {
		return UserInputID{}, codec.ErrInvalidData
	}
	s := b.owner
	ordinal, err := nextOrdinal(b.admission.candidate.inputs)
	if err != nil {
		return UserInputID{}, err
	}
	id := UserInputID{runID: run, ordinal: ordinal}
	if err := b.add(newUserInputRecord(request, id, s.policy.PayloadLimits)); err != nil {
		return UserInputID{}, err
	}
	return id, nil
}

// AddEffectiveInput adds an ordered incremental contribution in the named run.
// Incoming RunID must be zero. Relative references resolve within run, including
// earlier same-batch inputs, without proving the extracted component's origin.
// Failure changes no additions.
func (b *SessionCommit) AddEffectiveInput(run RunID, v EffectiveInput) error {
	if err := b.check(true); err != nil {
		return err
	}
	if err := run.Validate(); err != nil {
		return codec.ErrInvalidData
	}
	if run.historyID != b.owner.state.view.historyID {
		return ErrIdentity
	}
	if v.RunID != 0 {
		return codec.ErrInvalidData
	}
	return b.add(newEffectiveInputRecord(v, run, b.owner.policy.PayloadLimits))
}

// ReplaceInstructions adds a complete history-scoped replacement after creation,
// including while idle. Applicability survives runs; failure changes nothing.
func (b *SessionCommit) ReplaceInstructions(scope historyid.HistoryID, v Instructions) error {
	if err := b.check(true); err != nil {
		return err
	}
	if err := validateHistoryID(scope); err != nil {
		return codec.ErrInvalidData
	}
	if scope != b.owner.state.view.historyID {
		return ErrIdentity
	}
	return b.add(newInstructionsRecord(v, b.owner.policy.PayloadLimits))
}

// ReplaceToolCatalog adds a complete history-scoped advertised catalog replacement.
// Creation is required, not an active run; failure preserves earlier additions.
func (b *SessionCommit) ReplaceToolCatalog(scope historyid.HistoryID, v ToolCatalog) error {
	if err := b.check(true); err != nil {
		return err
	}
	if err := validateHistoryID(scope); err != nil {
		return codec.ErrInvalidData
	}
	if scope != b.owner.state.view.historyID {
		return ErrIdentity
	}
	return b.add(newToolCatalogRecord(v, b.owner.policy.PayloadLimits))
}

// ReplaceModelBinding adds complete history-scoped preparation choices after
// creation, including while idle. It performs no resolution; failure changes nothing.
func (b *SessionCommit) ReplaceModelBinding(scope historyid.HistoryID, v ModelBinding) error {
	if err := b.check(true); err != nil {
		return err
	}
	if err := validateHistoryID(scope); err != nil {
		return codec.ErrInvalidData
	}
	if scope != b.owner.state.view.historyID {
		return ErrIdentity
	}
	return b.add(newModelBindingRecord(v, b.owner.policy.PayloadLimits))
}

// StartRun allocates a provisional history-scoped run identity. Input-free runs
// are legal. Failed additions consume neither IDs nor batch space.
func (b *SessionCommit) StartRun() (RunID, error) {
	if err := b.check(true); err != nil {
		return RunID{}, err
	}
	s := b.owner
	ordinal, err := nextOrdinal(b.admission.candidate.runs)
	if err != nil {
		return RunID{}, err
	}
	id := RunID{historyID: s.state.view.historyID, ordinal: ordinal}
	if err := b.add(newRunStartedRecord(RunStarted{ID: id.ordinal}, s.policy.PayloadLimits)); err != nil {
		return RunID{}, err
	}
	return id, nil
}

// AddRunEnded records explicit terminal ownership. Ordinary endings require every
// tool call individually settled; Completed also requires accounted inputs and no
// pending model outcome. BulkToolCloseout is allowed only for Limit/CaptureFailed
// and derives settlement without individual records or resolving a pending model.
// Incoming RunID must be zero; model capture references are relative to run.
func (b *SessionCommit) AddRunEnded(run RunID, v RunEnded) error {
	if err := b.check(true); err != nil {
		return err
	}
	if err := run.Validate(); err != nil {
		return codec.ErrInvalidData
	}
	if run.historyID != b.owner.state.view.historyID {
		return ErrIdentity
	}
	if v.RunID != 0 {
		return codec.ErrInvalidData
	}
	return b.add(newRunEndedRecord(v, run, b.owner.policy.PayloadLimits))
}

// EndRun adds an ordinary ending for the provisional active run and requires all
// tool calls individually settled. Exceptional closeout must explicitly set
// BulkToolCloseout through AddRunEnded; EndRun never bypasses soft limits.
func (b *SessionCommit) EndRun(reason RunEndedReason) error {
	if err := b.check(true); err != nil {
		return err
	}
	if b.admission.candidate.activeRun == (RunID{}) {
		return ErrTransition
	}
	return b.AddRunEnded(b.admission.candidate.activeRun, RunEnded{Reason: reason})
}

// AddModelRequest allocates a provisional same-run invocation identity.
// Failure consumes no identity. Prerequisites come from the pinned accepted source.
// Incoming RunID and ID must be zero. RetryOf resolves within run without origin
// provenance; only earlier accepted requests are eligible.
func (b *SessionCommit) AddModelRequest(run RunID, v ModelRequest) (ModelRequestID, error) {
	if err := b.check(true); err != nil {
		return ModelRequestID{}, err
	}
	if run == (RunID{}) || run != b.admission.candidate.activeRun {
		return ModelRequestID{}, ErrTransition
	}
	if v.RunID != 0 || v.ID != 0 {
		return ModelRequestID{}, codec.ErrInvalidData
	}
	ordinal, err := nextOrdinal(b.admission.candidate.requests)
	if err != nil {
		return ModelRequestID{}, err
	}
	id := ModelRequestID{runID: run, requestOrdinal: ordinal}
	if err := b.add(newModelRequestRecord(v, id, b.owner.policy.PayloadLimits)); err != nil {
		return ModelRequestID{}, err
	}
	return id, nil
}

// AddModelResponse records one complete response for an earlier accepted request.
// Incoming RunID and RequestID must be zero, even if matching request.
func (b *SessionCommit) AddModelResponse(request ModelRequestID, v ModelResponse) error {
	if err := b.check(true); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return codec.ErrInvalidData
	}
	if request.runID.historyID != b.owner.state.view.historyID {
		return ErrIdentity
	}
	if v.RunID != 0 || v.RequestID != 0 {
		return codec.ErrInvalidData
	}
	return b.add(newModelResponseRecord(v, request, b.owner.policy.PayloadLimits))
}

// AddInvocationFailure records an observed failure, not a fabricated response.
// Incoming RunID and RequestID must be zero, even if matching request.
func (b *SessionCommit) AddInvocationFailure(request ModelRequestID, v InvocationFailure) error {
	if err := b.check(true); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return codec.ErrInvalidData
	}
	if request.runID.historyID != b.owner.state.view.historyID {
		return ErrIdentity
	}
	if v.RunID != 0 || v.RequestID != 0 {
		return codec.ErrInvalidData
	}
	return b.add(newInvocationFailureRecord(v, request, b.owner.policy.PayloadLimits))
}

// AddToolInvocation retains effective committed intent for the named call.
// Incoming RunID, RequestID and CallID must be zero, even if matching call.
// Live handle, full destination scope and zero fields precede payload sizing.
// Failure leaves all earlier additions unchanged and grants no execution authority.
func (b *SessionCommit) AddToolInvocation(call RequestedCallID, v ToolInvocation) error {
	if err := b.check(true); err != nil {
		return err
	}
	if err := call.Validate(); err != nil {
		return codec.ErrInvalidData
	}
	if call.requestID.runID.historyID != b.owner.state.view.historyID {
		return ErrIdentity
	}
	if v.RunID != 0 || v.RequestID != 0 || v.CallID != 0 {
		return codec.ErrInvalidData
	}
	return b.add(newToolInvocationRecord(v, call, b.owner.policy.PayloadLimits))
}

// AddToolDisposition retains one explicit outcome for the named requested call.
// Incoming RunID, RequestID and CallID must be zero, even if matching call.
// Live handle, full destination scope and zero fields precede payload sizing.
// Failure retains no partial output and leaves all earlier additions unchanged.
func (b *SessionCommit) AddToolDisposition(call RequestedCallID, v ToolDisposition) error {
	if err := b.check(true); err != nil {
		return err
	}
	if err := call.Validate(); err != nil {
		return codec.ErrInvalidData
	}
	if call.requestID.runID.historyID != b.owner.state.view.historyID {
		return ErrIdentity
	}
	if v.RunID != 0 || v.RequestID != 0 || v.CallID != 0 {
		return codec.ErrInvalidData
	}
	return b.add(newToolDispositionRecord(v, call, b.owner.policy.PayloadLimits))
}

// Discard releases an unsubmitted batch without consuming IDs. Frozen uncertain
// proposals cannot be discarded. After successful submission it is a harmless
// no-op, allowing defer b.Discard(). Invalid/stale/discarded handles return ErrBatch
// and can never release a newer slot.
func (b *SessionCommit) Discard() error {
	if b != nil && b.completed {
		return nil
	}
	if err := b.check(true); err != nil {
		return err
	}
	b.close(false)
	return nil
}

// close releases only the verified original slot and drops no accepted state.
func (b *SessionCommit) close(completed bool) {
	b.owner.batch = nil
	b.completed = completed
	b.records = nil
	b.proposal = prepared{}
}

// appendError preserves errors.Is/As causes without rendering untrusted backend
// payloads. Acceptance is unknown, including for cancellation and timeout.
type appendError struct{ cause error }

func (appendError) Error() string        { return "session append failed; acceptance unknown" }
func (e appendError) Unwrap() error      { return e.cause }
func (appendError) Is(target error) bool { return target == ErrAcceptanceUnknown }

// SubmitCommit freezes and preflights a batch, then makes exactly one History
// Append attempt (none for invalid handles/preflight). Capacity rejection before
// Append returns a zero ref and releases the slot without publication. Uncertain
// errors match ErrAcceptanceUnknown and keep the exact owned proposal frozen:
// invoke again on the same handle with a fresh context to retry. No retries or
// observations occur automatically. A limit cause alone is not rejection proof.
//
// Success returns this proposal's accepted reference, not a potentially newer
// observed head. A newer head leaves this object reload-required. Conflict and
// expiry close the batch and require fresh load; expiry is not rejection. If
// accepted application fails, the nonzero accepted reference accompanies
// ErrAcceptedApply. All other errors return a zero reference.
func (s *Session) SubmitCommit(ctx context.Context, b *SessionCommit) (history.CommitRef, error) {
	if b == nil || b.owner != s {
		return history.CommitRef{}, ErrBatch
	}
	if err := b.check(false); err != nil {
		return history.CommitRef{}, err
	}
	if !b.frozen {
		b.frozen = true
		proposal, err := s.prepare(b.records)
		if err != nil {
			b.close(false)
			return history.CommitRef{}, err
		}
		b.proposal = proposal
	}
	p := b.proposal
	result, err := s.history.Append(ctx, s.state.view.historyID, p.commit)
	if err != nil {
		return history.CommitRef{}, appendError{cause: err}
	}
	switch result.Status {
	case history.StatusAccepted:
		err := s.apply(history.Commit{Ref: p.ref, Commit: p.commit})
		b.close(true)
		if err != nil {
			s.reload = true
			return p.ref, ErrAcceptedApply
		}
		if result.Head != p.ref {
			s.reload = true
		}
		return p.ref, nil
	case history.StatusConflict:
		b.close(false)
		s.reload = true
		return history.CommitRef{}, ErrConflict
	case history.StatusRetryExpired:
		b.close(false)
		s.reload = true
		return history.CommitRef{}, ErrRetryExpired
	default:
		// History.Append cannot return NotObserved, but incoherent evidence must
		// never free a proposal whose acceptance remains unknown.
		return history.CommitRef{}, appendError{cause: history.ErrCorrupt}
	}
}
