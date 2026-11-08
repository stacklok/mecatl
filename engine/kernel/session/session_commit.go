package session

import (
	"context"
	"errors"
	"math"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
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
// callers check target history before conversion erases scope. Aggregate policy
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
	return b.add(newCreatedRecord(b.owner.state.view.historyID, Created{HistoryID: b.owner.state.view.historyID}, b.owner.policy.PayloadLimits))
}

// AddUserInput allocates a provisional identity in the explicitly named active
// run. It owns encoded content before returning; failure consumes no ID or slot.
func (b *SessionCommit) AddUserInput(request UserInput) (UserInputID, error) {
	if err := b.check(true); err != nil {
		return UserInputID{}, err
	}
	if request.RunID == (RunID{}) || request.RunID != b.admission.candidate.activeRun {
		return UserInputID{}, ErrTransition
	}
	s := b.owner
	ordinal, err := nextOrdinal(b.admission.candidate.inputs)
	if err != nil {
		return UserInputID{}, err
	}
	id := UserInputID{runID: request.RunID, ordinal: ordinal}
	if err := b.add(newUserInputRecord(s.state.view.historyID, request, id, s.policy.PayloadLimits)); err != nil {
		return UserInputID{}, err
	}
	return id, nil
}

// AddEffectiveInput adds an ordered incremental contribution in the active run.
// References include earlier same-batch inputs; failure changes no additions.
func (b *SessionCommit) AddEffectiveInput(v EffectiveInput) error {
	if err := b.check(true); err != nil {
		return err
	}
	return b.add(newEffectiveInputRecord(b.owner.state.view.historyID, v, b.owner.policy.PayloadLimits))
}

// ReplaceInstructions adds a complete history-scoped replacement after creation,
// including while idle. Applicability survives runs; failure changes nothing.
func (b *SessionCommit) ReplaceInstructions(v Instructions) error {
	if err := b.check(true); err != nil {
		return err
	}
	return b.add(newInstructionsRecord(b.owner.state.view.historyID, v, b.owner.policy.PayloadLimits))
}

// ReplaceToolCatalog adds a complete history-scoped advertised catalog replacement.
// Creation is required, not an active run; failure preserves earlier additions.
func (b *SessionCommit) ReplaceToolCatalog(v ToolCatalog) error {
	if err := b.check(true); err != nil {
		return err
	}
	return b.add(newToolCatalogRecord(b.owner.state.view.historyID, v, b.owner.policy.PayloadLimits))
}

// ReplaceModelBinding adds complete history-scoped preparation choices after
// creation, including while idle. It performs no resolution; failure changes nothing.
func (b *SessionCommit) ReplaceModelBinding(v ModelBinding) error {
	if err := b.check(true); err != nil {
		return err
	}
	return b.add(newModelBindingRecord(b.owner.state.view.historyID, v, b.owner.policy.PayloadLimits))
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
	if err := b.add(newRunStartedRecord(s.state.view.historyID, RunStarted{ID: id}, s.policy.PayloadLimits)); err != nil {
		return RunID{}, err
	}
	return id, nil
}

// AddRunEnded records explicit terminal ownership. Completed requires accounted
// inputs and no pending outcome or calls; other reasons preserve unresolved facts.
func (b *SessionCommit) AddRunEnded(v RunEnded) error {
	if err := b.check(true); err != nil {
		return err
	}
	return b.add(newRunEndedRecord(b.owner.state.view.historyID, v, b.owner.policy.PayloadLimits))
}

// EndRun adds a minimal ending for the provisional active run. It is the fixed
// shape used for terminal escape; callers needing evidence use AddRunEnded.
func (b *SessionCommit) EndRun(reason RunEndedReason) error {
	if err := b.check(true); err != nil {
		return err
	}
	if b.admission.candidate.activeRun == (RunID{}) {
		return ErrTransition
	}
	return b.AddRunEnded(RunEnded{RunID: b.admission.candidate.activeRun, Reason: reason})
}

// AddModelRequest allocates a provisional same-run invocation identity.
// Failure consumes no identity. Prerequisites come from the pinned accepted source.
func (b *SessionCommit) AddModelRequest(v ModelRequest) (ModelRequestID, error) {
	if err := b.check(true); err != nil {
		return ModelRequestID{}, err
	}
	if v.RunID == (RunID{}) || v.RunID != b.admission.candidate.activeRun {
		return ModelRequestID{}, ErrTransition
	}
	ordinal, err := nextOrdinal(b.admission.candidate.requests)
	if err != nil {
		return ModelRequestID{}, err
	}
	id := ModelRequestID{runID: v.RunID, requestOrdinal: ordinal}
	if err := b.add(newModelRequestRecord(b.owner.state.view.historyID, v, id, b.owner.policy.PayloadLimits)); err != nil {
		return ModelRequestID{}, err
	}
	return id, nil
}

// AddModelResponse records one complete response for an earlier accepted request.
func (b *SessionCommit) AddModelResponse(v ModelResponse) error {
	if err := b.check(true); err != nil {
		return err
	}
	return b.add(newModelResponseRecord(b.owner.state.view.historyID, v, b.owner.policy.PayloadLimits))
}

// AddInvocationFailure records an observed failure, not a fabricated response.
func (b *SessionCommit) AddInvocationFailure(v InvocationFailure) error {
	if err := b.check(true); err != nil {
		return err
	}
	return b.add(newInvocationFailureRecord(b.owner.state.view.historyID, v, b.owner.policy.PayloadLimits))
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
