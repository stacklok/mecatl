package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memorypromotion"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	memoryadapter "github.com/stacklok/mecatl/internal/adapter/memory"
)

type reflectionObserver struct {
	coordinator      *reflectionCoordinator
	reflector        learning.Reflector
	repository       learning.ProposalRepository
	attempts         learning.AttemptRepository
	sourceStore      port.SessionStore
	operatorMemory   tool.MemoryStore
	projectMemory    tool.MemoryStore
	mode             learning.Mode
	trusted          bool
	projectWorkspace string
	admission        *learningAdmission
	policy           learning.AdmissionPolicy
	ledger           learning.AutomaticAdmissionLedger
	automatic        LearningAutomaticConfig
	tokenCounter     agent.TokenCounter
	sensitivity      learning.Sensitivity
	metrics          func(learning.Activity)
	procedure        func(context.Context, learning.ProposalRecord, learning.Mode) error
	lifecycle        *materializationLifecycle
}

func reflectionPrincipal(p *session.Principal) string {
	value := "ownerless"
	if p != nil {
		value = p.Issuer + "\x00" + p.Subject
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func reflectionContext(ctx context.Context, principal *session.Principal) context.Context {
	if principal != nil {
		return session.WithPrincipal(ctx, principal)
	}
	return ctx
}

func memoryExisting(ctx context.Context, stores ...tool.MemoryStore) []learning.ExistingFact {
	var result []learning.ExistingFact
	seen := make(map[string]bool)
	for _, store := range stores {
		if store == nil {
			continue
		}
		entries, err := store.List(ctx, "")
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if seen[entry.Key] || len(result) >= learning.MaxExistingFacts {
				continue
			}
			seen[entry.Key] = true
			result = append(result, learning.ExistingFact{
				Kind: learning.CandidateOperatorFact, Key: entry.Key,
				Value: entry.Value, Description: entry.Description,
			})
		}
	}
	return result
}

func (o *reflectionObserver) Observe(ctx context.Context, trajectory learning.Trajectory) error {
	if o == nil || o.mode == learning.Off {
		return nil
	}
	_, err := o.submit(ctx, trajectory, nil, true, true)
	return err
}

// Reflect explicitly submits a completed session. Unlike Observe, it bypasses
// automatic mode and cadence admission; off still stages proposals for review.
func (o *reflectionObserver) Reflect(ctx context.Context, trajectory learning.Trajectory, async bool) (reflectionReceipt, error) {
	return o.submit(ctx, trajectory, nil, async, false)
}

func (o *reflectionObserver) reflectWithEvents(ctx context.Context, trajectory learning.Trajectory, events []session.Event) (reflectionReceipt, error) {
	return o.submit(ctx, trajectory, events, false, false)
}

func reflectionTrajectoryBytes(trajectory learning.Trajectory) int {
	total := len(trajectory.Workspace) + len(trajectory.SessionID)
	for _, message := range trajectory.Messages {
		total += len(message.Text) + len(message.Reasoning) + len(message.ProviderPhase) + len(message.ReasoningItemID)
		for _, call := range message.ToolCalls {
			total += len(call.ID) + len(call.Name) + len(call.Args) + len(call.ItemID)
		}
		for _, part := range message.Parts {
			total += len(part.Data) + len(part.Text) + len(part.URL) + len(part.Name) + len(part.Title) + len(part.Description)
		}
		if message.ToolResult != nil {
			total += len(message.ToolResult.Content) + len(message.ToolResult.CallID)
			for _, part := range message.ToolResult.Parts {
				total += len(part.Data) + len(part.Text) + len(part.URL) + len(part.Name) + len(part.Title) + len(part.Description)
			}
		}
		if total > defaultReflectionJobBytes {
			return total
		}
	}
	return total
}

func reflectionEventsBytes(events []session.Event, limit int) int {
	total := 0
	for _, event := range events {
		total += len(event.Type) + len(event.Text)
		if event.ToolCall != nil {
			total += len(event.ToolCall.ID) + len(event.ToolCall.Name)
		}
		if event.ToolResult != nil {
			total += len(event.ToolResult.CallID) + len(event.ToolResult.Content)
			for _, part := range event.ToolResult.Parts {
				total += len(part.Data) + len(part.Text) + len(part.Name) + len(part.Title) + len(part.Description) + len(part.MIMEType)
			}
		}
		if total > limit {
			return total
		}
	}
	return total
}

func currentPromptBinding(input learning.Input, class learning.AdmissionClass) (learning.CurrentPromptBinding, error) {
	index := -1
	if input.Trajectory.Current.Valid(len(input.Trajectory.Messages)) {
		for i := input.Trajectory.Current.Start; i < input.Trajectory.Current.End; i++ {
			if session.IsGenuineUserPrompt(input.Trajectory.Messages[i]) {
				index = i
				break
			}
		}
	} else if class == learning.AdmissionHostRequested {
		for i := len(input.Trajectory.Messages) - 1; i >= 0; i-- {
			if session.IsGenuineUserPrompt(input.Trajectory.Messages[i]) {
				index = i
				break
			}
		}
	}
	if index < 0 {
		return learning.CurrentPromptBinding{}, learning.ErrInvalidAttempt
	}
	ref, err := learning.MessageEvidenceRef(input, index, "")
	if err != nil {
		return learning.CurrentPromptBinding{}, err
	}
	return learning.CurrentPromptBinding{
		Ordinal: index,
		Digest:  learning.CanonicalDigest(ref.Digest),
		Origin:  learning.PromptOriginCurrentPrincipal,
	}, nil
}

func (o *reflectionObserver) durableAttemptMaterial(ctx context.Context, input learning.Input, owner *session.Principal, class learning.AdmissionClass) (learning.AttemptPartition, learning.AttemptCreate, error) {
	if o.attempts == nil {
		return "", learning.AttemptCreate{}, errors.New("durable learning attempt repository is not configured")
	}
	if o.sourceStore == nil || input.Trajectory.RunID == "" {
		return "", learning.AttemptCreate{}, errors.New("durable learning admission requires a persisted ADR-0249 run ID")
	}
	persisted, err := o.sourceStore.Load(ctx, input.Trajectory.SessionID)
	if err != nil || persisted.RunID() != input.Trajectory.RunID {
		return "", learning.AttemptCreate{}, errors.New("durable learning admission requires an exact persisted ADR-0249 run ID")
	}
	digest, err := automaticTrajectoryDigest(input)
	if err != nil {
		return "", learning.AttemptCreate{}, err
	}
	source := learning.AttemptSource{
		SessionID:       input.Trajectory.SessionID,
		RunID:           learning.DurableRunID(input.Trajectory.RunID),
		CanonicalDigest: learning.CanonicalDigest(digest),
	}
	prompt, err := currentPromptBinding(input, class)
	if err != nil {
		return "", learning.AttemptCreate{}, err
	}
	provenance, err := learning.NewAdmissionProvenance(class, source, prompt)
	if err != nil {
		return "", learning.AttemptCreate{}, err
	}
	caller := reflectionPrincipal(owner)
	partition, err := learning.DeriveAttemptPartition(caller)
	if err != nil {
		return "", learning.AttemptCreate{}, err
	}
	id, err := learning.DeterministicAttemptID(caller, source)
	if err != nil {
		return "", learning.AttemptCreate{}, err
	}
	return partition, learning.AttemptCreate{ID: id, Provenance: provenance}, nil
}

func (o *reflectionObserver) createDurableAttempt(ctx context.Context, input learning.Input, owner *session.Principal, class learning.AdmissionClass) (learning.AttemptRecord, bool, error) {
	partition, create, err := o.durableAttemptMaterial(ctx, input, owner, class)
	if err != nil {
		return learning.AttemptRecord{}, false, err
	}
	if existing, found, getErr := o.attempts.Get(ctx, partition, create.ID); getErr != nil {
		return learning.AttemptRecord{}, false, getErr
	} else if found {
		return existing, true, nil
	}
	record, err := o.attempts.Create(ctx, partition, create)
	return record, false, err
}

func positiveAutomaticValue(value int) (uint64, error) {
	if value <= 0 {
		return 0, learning.ErrAutomaticAdmissionLimit
	}
	return uint64(value), nil //nolint:gosec // positivity excludes signed wraparound
}

func automaticAdmissionPolicy(cfg LearningAutomaticConfig) (learning.AutomaticAdmissionPolicy, error) {
	maxCount, err := positiveAutomaticValue(cfg.MaxReflections)
	if err != nil {
		return learning.AutomaticAdmissionPolicy{}, err
	}
	maxTokens, err := positiveAutomaticValue(cfg.MaxTokens)
	if err != nil {
		return learning.AutomaticAdmissionPolicy{}, err
	}
	maxPrincipalCount, err := positiveAutomaticValue(cfg.MaxReflectionsPerPrincipal)
	if err != nil {
		return learning.AutomaticAdmissionPolicy{}, err
	}
	maxPrincipalTokens, err := positiveAutomaticValue(cfg.MaxTokensPerPrincipal)
	if err != nil {
		return learning.AutomaticAdmissionPolicy{}, err
	}
	return learning.AutomaticAdmissionPolicy{
		Window: cfg.Window, Cooldown: cfg.Cooldown, DedupeWindow: learningDedupeTTL,
		MaxCount: maxCount, MaxTokens: maxTokens, MaxCountPerPrincipal: maxPrincipalCount, MaxTokensPerPrincipal: maxPrincipalTokens,
		ReservationClaimDuration: time.Minute,
	}, nil
}

func (o *reflectionObserver) createOrReserveAttempt(ctx context.Context, partition learning.AttemptPartition, create learning.AttemptCreate, tokens int) (learning.AttemptRecord, bool, error) {
	if o.ledger == nil {
		return learning.AttemptRecord{}, false, errors.New("durable automatic admission ledger is not configured")
	}
	existing, found, err := o.attempts.Get(ctx, partition, create.ID)
	if err != nil {
		return learning.AttemptRecord{}, false, err
	}
	reservationID, err := learning.AutomaticReservationIDForAttempt(create.ID)
	if err != nil {
		return learning.AttemptRecord{}, false, err
	}
	policy, err := automaticAdmissionPolicy(o.automatic)
	if err != nil {
		return learning.AttemptRecord{}, false, err
	}
	reservedTokens, err := positiveAutomaticValue(tokens)
	if err != nil {
		return learning.AttemptRecord{}, false, err
	}
	revision, err := learning.AutomaticAdmissionPolicyRevisionFor(policy)
	if err != nil {
		return learning.AttemptRecord{}, false, err
	}
	req := learning.AutomaticReservationRequest{
		ID: reservationID, AttemptID: create.ID, Principal: partition,
		Digest: create.Provenance.Source.CanonicalDigest, Class: create.Provenance.Class,
		Tokens: reservedTokens, ExpectedPolicyRevision: revision,
	}
	record, err := (automaticReservationReconciler{ledger: o.ledger, attempts: o.attempts}).reserveAndCreate(ctx, req, create)
	if err != nil {
		return learning.AttemptRecord{}, false, err
	}
	return record, found && existing.ID == record.ID, nil
}

func selectedCurrentSpan(manifest learning.MaterializationManifest, current learning.MessageSpan) learning.MessageSpan {
	start, end := -1, -1
	selected := 0
	for _, entry := range manifest.Entries {
		if entry.Locator != learning.EvidenceMessage || entry.OriginalMessage == nil {
			continue
		}
		if current.Contains(*entry.OriginalMessage) {
			if start < 0 {
				start = selected
			}
			end = selected + 1
		}
		selected++
	}
	if start < 0 {
		return learning.MessageSpan{}
	}
	return learning.MessageSpan{Start: start, End: end}
}

func boundedExisting(input learning.Input, existing []learning.ExistingFact) []learning.ExistingFact {
	for _, fact := range existing {
		candidate := append(input.Existing, fact)
		input.Existing = candidate
		if _, err := reflectionInputMaterial(input, defaultReflectionJobBytes); err != nil {
			return candidate[:len(candidate)-1]
		}
	}
	return input.Existing
}

//nolint:gocyclo // explicit/direct and automatic/queued paths share one bounded input and disposition funnel
func (o *reflectionObserver) submit(ctx context.Context, trajectory learning.Trajectory, events []session.Event, _ bool, automatic bool) (reflectionReceipt, error) {
	if o == nil || o.reflector == nil || o.repository == nil {
		return reflectionReceipt{}, errors.New("reflection is not configured")
	}
	if err := ctx.Err(); err != nil {
		return reflectionReceipt{}, err
	}
	if automatic && o.lifecycle != nil {
		operation, err := o.lifecycle.enter(ctx)
		if err != nil {
			return reflectionReceipt{}, err
		}
		defer operation.leave()
		ctx = operation.Context()
	}
	owner := trajectory.Principal.Clone()
	ctx = reflectionContext(ctx, owner)
	ctx = memoryadapter.WithWorkspace(ctx, trajectory.Workspace)
	stores := []tool.MemoryStore{o.operatorMemory}
	if o.trusted && trajectory.Workspace == o.projectWorkspace {
		stores = append(stores, o.projectMemory)
	}
	hostSignals := []learning.Signal(nil)
	if !automatic {
		hostSignals = []learning.Signal{{Kind: learning.SignalHostRequested}}
	}
	var input learning.Input
	var signals []learning.Signal
	var selectedBytes int
	decision := learning.AdmissionDecision{Admitted: true, Class: learning.AdmissionHostRequested, Reasons: []learning.AdmissionReason{learning.ReasonHostRequested}}
	if automatic {
		if err := ctx.Err(); err != nil {
			return reflectionReceipt{}, err
		}
		// Admission borrows the completed trajectory instead of NewInput's owned
		// full-source copy. The policy scans the complete source/current span; only
		// an admitted trajectory is copied into a bounded selected Input below.
		admissionInput := learning.Input{Trajectory: trajectory}
		policy := o.policy
		if policy == nil {
			policy = learning.ThresholdPolicy{Sensitivity: o.sensitivity}
		}
		decision = policy.Decide(learning.AdmissionRequest{Input: admissionInput})
		o.emitAdmission(decision)
		if !decision.Admitted {
			return reflectionReceipt{}, nil
		}
		if err := ctx.Err(); err != nil {
			return reflectionReceipt{}, err
		}
		var materialized learning.Materialization
		materialLimit := defaultReflectionJobBytes
		for {
			var err error
			materialized, err = learning.MaterializeEvidence(learning.MaterializationRequest{
				Trajectory: trajectory,
				Events:     events,
				Signals:    decision.Signals,
				Mandatory:  trajectory.Current,
				Limits:     learning.MaterializationLimits{MaxBytes: materialLimit},
			})
			if err != nil {
				return reflectionReceipt{}, err
			}
			if materialized.Disposition != learning.MaterializationSelected {
				return reflectionReceipt{}, nil
			}
			input = materialized.Input
			selectedBytes = len(materialized.Canonical)
			input.Trajectory.Workspace = trajectory.Workspace
			input.Trajectory.Principal = owner
			input.Trajectory.Kind = trajectory.Kind
			input.Trajectory.Counters = trajectory.Counters
			input.Trajectory.Current = selectedCurrentSpan(materialized.Manifest, trajectory.Current)
			signals = learning.DetectSignals(input)
			input.Signals = signals
			if _, err := reflectionInputMaterial(input, defaultReflectionJobBytes); err == nil {
				break
			}
			materialLimit /= 2
			if materialLimit == 0 {
				return reflectionReceipt{}, nil
			}
		}
		input.Existing = boundedExisting(input, memoryExisting(ctx, stores...))
		if err := learning.ValidateInput(input); err != nil {
			return reflectionReceipt{}, fmt.Errorf("validate automatic materialization: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return reflectionReceipt{}, err
		}
	} else {
		materialized, err := learning.MaterializeEvidence(learning.MaterializationRequest{
			Trajectory: trajectory,
			Events:     events,
			Signals:    append(hostSignals, learning.DetectSignals(learning.Input{Trajectory: trajectory})...),
			Limits:     learning.MaterializationLimits{MaxBytes: defaultReflectionJobBytes},
			Explicit:   true,
		})
		if err != nil {
			return reflectionReceipt{}, err
		}
		if materialized.Disposition != learning.MaterializationSelected {
			return reflectionReceipt{Disposition: reflectionCompleted, Abstained: true, Err: materialized.Reason.String()}, nil
		}
		input = materialized.Input
		selectedBytes = len(materialized.Canonical)
		input.Trajectory.Workspace = trajectory.Workspace
		input.Trajectory.Principal = owner
		input.Trajectory.Kind = trajectory.Kind
		input.Trajectory.Counters = trajectory.Counters
		input.Trajectory.Current = selectedCurrentSpan(materialized.Manifest, trajectory.Current)
		signals = append([]learning.Signal(nil), hostSignals...)
		signals = append(signals, learning.DetectSignals(input)...)
		input.Signals = signals
		input.Existing = boundedExisting(input, memoryExisting(ctx, stores...))
		if err := learning.ValidateInput(input); err != nil {
			return reflectionReceipt{}, fmt.Errorf("validate explicit materialization: %w", err)
		}
		decision.Signals = signals
		if err := ctx.Err(); err != nil {
			return reflectionReceipt{}, err
		}
	}
	reservationTokens := 0
	if automatic {
		estimator, ok := o.reflector.(interface {
			RequestTokenEstimate(learning.Input) (int, error)
		})
		if !ok {
			return reflectionReceipt{}, errors.New("automatic reflection requires an exact request token estimator")
		}
		estimated, estimateErr := estimator.RequestTokenEstimate(input)
		if estimateErr != nil {
			return reflectionReceipt{}, estimateErr
		}
		reservationTokens = estimated
	}
	if o.attempts == nil || o.mode == learning.Off {
		jobCtx, cancel := context.WithTimeout(ctx, defaultReflectionJobTimeout)
		defer cancel()
		outcome, err := o.reflector.Reflect(jobCtx, input)
		if err != nil {
			return reflectionReceipt{Disposition: reflectionFailed}, err
		}
		identity, digest, err := selectedEvidenceIdentity(input)
		if err != nil {
			return reflectionReceipt{Disposition: reflectionFailed}, err
		}
		receipt := reflectionReceipt{ID: reflectionJobID(reflectionPrincipal(owner) + "\x00" + identity), Disposition: reflectionCompleted}
		if outcome.Kind == learning.OutcomeAbstained {
			receipt.Abstained = true
			return receipt, nil
		}
		processed, err := o.job(input, signals, owner, selectedBytes, nil, nil).process(jobCtx, digest, outcome)
		processed.ID, processed.Disposition = receipt.ID, reflectionCompleted
		return processed, err
	}
	// The legacy interval is a post-threshold downsampler. Hard and explicit
	// requests bypass it; 0/1 are inert.
	if automatic && decision.Class == learning.AdmissionWeighted && o.admission != nil && !o.admission.admit() {
		return reflectionReceipt{}, nil
	}
	partition, create, err := o.durableAttemptMaterial(ctx, input, owner, decision.Class)
	if err != nil {
		return reflectionReceipt{Disposition: reflectionFailed}, err
	}
	var attempt learning.AttemptRecord
	var existingAttempt bool
	if automatic {
		attempt, existingAttempt, err = o.createOrReserveAttempt(ctx, partition, create, reservationTokens)
	} else {
		attempt, existingAttempt, err = o.createDurableAttempt(ctx, input, owner, decision.Class)
	}
	if err != nil {
		if automatic && (errors.Is(err, learning.ErrAutomaticAdmissionLimit) || errors.Is(err, learning.ErrAutomaticAdmissionCooldown) || errors.Is(err, learning.ErrAutomaticAdmissionDuplicate)) {
			o.emitAutomaticRefusal(err)
			return reflectionReceipt{ID: string(create.ID), Disposition: reflectionRateLimited}, nil
		}
		return reflectionReceipt{ID: string(create.ID), Disposition: reflectionFailed}, err
	}
	if automatic && !existingAttempt && o.metrics != nil {
		reason := learning.ReasonWeightedThreshold
		if decision.Class == learning.AdmissionHard {
			reason = learning.ReasonHardTrigger
		}
		o.metrics(learning.Activity{Kind: learning.ActivityReservedTokens, Reason: reason, Sensitivity: o.sensitivity, Count: int64(reservationTokens)})
	}
	if existingAttempt && attempt.State.Terminal() {
		return o.receiptForAttempt(ctx, attempt, input, reflectionPrincipal(owner)), nil
	}
	disposition := reflectionQueued
	if existingAttempt {
		disposition = reflectionDuplicate
		if automatic && o.metrics != nil {
			o.metrics(learning.Activity{Kind: learning.ActivityDuplicate, Reason: learning.ReasonDuplicate, Sensitivity: o.sensitivity, Count: 1})
		}
	}
	return reflectionReceipt{ID: string(create.ID), Disposition: disposition}, nil
}

func (o *reflectionObserver) receiptForAttempt(ctx context.Context, attempt learning.AttemptRecord, input learning.Input, principal string) reflectionReceipt {
	receipt := reflectionReceipt{ID: string(attempt.ID), Disposition: reflectionDuplicate, Abstained: attempt.Outcome == learning.AttemptOutcomeAbstained}
	if attempt.Outcome == learning.AttemptOutcomeFailed {
		receipt.Err = string(attempt.FailureCode)
	}
	if attempt.Outcome == learning.AttemptOutcomeSucceeded {
		// A successful proposed attempt reaches terminal only after the downstream
		// proposal checkpoint; preserve that durable fact even if the proposal was
		// subsequently retired by its independent retention lifecycle.
		receipt.Staged = 1
	}
	if attempt.ProposalID == "" || o.repository == nil {
		return receipt
	}
	partitions := []learning.ProposalPartition{{Principal: principal}}
	if input.Trajectory.Workspace != "" {
		partitions = append(partitions, learning.ProposalPartition{Principal: principal, Project: input.Trajectory.Workspace})
	}
	for _, partition := range partitions {
		proposal, found, err := o.repository.Get(ctx, partition, attempt.ProposalID)
		if err != nil || !found {
			continue
		}
		receipt.Staged = 1
		switch proposal.Status {
		case learning.ProposalPromoted:
			receipt.Promoted = 1
		case learning.ProposalConflicted:
			receipt.Conflicted = 1
		}
		return receipt
	}
	return receipt
}

func (o *reflectionObserver) emitAdmission(decision learning.AdmissionDecision) {
	if o == nil || o.metrics == nil {
		return
	}
	kind := learning.ActivitySkipped
	if decision.Admitted {
		kind = learning.ActivityAdmitted
	}
	reason := learning.ReasonBelowThreshold
	if decision.Admitted {
		if decision.Class == learning.AdmissionHard {
			reason = learning.ReasonHardTrigger
		} else {
			reason = learning.ReasonWeightedThreshold
		}
	} else if len(decision.Reasons) > 0 {
		reason = decision.Reasons[0]
	}
	o.metrics(learning.Activity{Kind: kind, Reason: reason, Sensitivity: o.sensitivity, Count: 1})
}

func (o *reflectionObserver) emitAutomaticRefusal(err error) {
	if o == nil || o.metrics == nil {
		return
	}
	activity := learning.Activity{Kind: learning.ActivityRateLimited, Reason: learning.ReasonRateLimit, Sensitivity: o.sensitivity, Count: 1}
	if errors.Is(err, learning.ErrAutomaticAdmissionDuplicate) {
		activity.Kind, activity.Reason = learning.ActivityDuplicate, learning.ReasonDuplicate
	}
	o.metrics(activity)
}

func (o *reflectionObserver) job(input learning.Input, signals []learning.Signal, owner *session.Principal, selectedBytes int, reserve func() bool, complete func(reflectionReceipt)) reflectionJob {
	principal := reflectionPrincipal(owner)
	return reflectionJob{
		principal: principal, input: input, selectedBytes: selectedBytes,
		reflector: o.reflector,
		reserve:   reserve,
		complete:  complete,
		process: func(ctx context.Context, digest string, outcome learning.Outcome) (reflectionReceipt, error) {
			return processReflectionOutcome(memoryadapter.WithWorkspace(reflectionContext(ctx, owner), input.Trajectory.Workspace), o.repository,
				o.operatorMemory, o.projectMemory, principal, input,
				digest, outcome, signals, o.mode, o.trusted, o.projectWorkspace, o.procedure)
		},
	}
}

type stagedGroup struct {
	partition    learning.ProposalPartition
	store        tool.MemoryStore
	items        []learning.Candidate
	primary      bool
	primaryIndex int
}

func checkpointFromReflectionReceipt(receipt reflectionReceipt) (learning.AttemptCheckpoint, learning.AttemptFailureCode, error) {
	if receipt.ProposalID == "" {
		return learning.AttemptCheckpoint{}, learning.FailureEvaluationRejected, learning.ErrInvalidProposal
	}
	checkpoint := learning.AttemptCheckpoint{Stage: learning.AttemptCheckpointProposalLinked, ProposalID: receipt.ProposalID}
	if receipt.SkillID != "" {
		checkpoint.Stage = learning.AttemptCheckpointSkillLinked
		checkpoint.SkillID = receipt.SkillID
	}
	return checkpoint, learning.FailureNone, nil
}

func evidenceRefEqual(a, b learning.EvidenceRef) bool {
	return a.SessionID == b.SessionID && a.Locator == b.Locator && a.Ordinal == b.Ordinal && a.ToolCallID == b.ToolCallID && a.Digest == b.Digest
}

// autoPromotionEligible keeps model-selected evidence from becoming authority.
// Both fact kinds require an explicit principal-authored remember request. Trusted
// project facts additionally require the exact configured root.
func autoPromotionEligible(input learning.Input, candidate learning.Candidate, trusted bool, projectWorkspace string) bool {
	current := input.Trajectory.Current
	if !current.Valid(len(input.Trajectory.Messages)) {
		return false
	}
	userEvidence := func(ref learning.EvidenceRef) bool {
		return ref.Locator == learning.EvidenceMessage && current.Contains(ref.Ordinal) && ref.Ordinal >= 0 && ref.Ordinal < len(input.Trajectory.Messages) && session.IsGenuineUserPrompt(input.Trajectory.Messages[ref.Ordinal])
	}
	if candidate.Kind == learning.CandidateProjectFact && (!trusted || input.Trajectory.Workspace == "" || input.Trajectory.Workspace != projectWorkspace) {
		return false
	}
	for _, signal := range learning.DetectSignals(input) {
		if signal.Kind != learning.SignalExplicitRemember {
			continue
		}
		for _, signalRef := range signal.Evidence {
			if !userEvidence(signalRef) {
				continue
			}
			for _, candidateRef := range candidate.Evidence {
				if evidenceRefEqual(signalRef, candidateRef) {
					return true
				}
			}
		}
	}
	return false
}

// processReflectionOutcome validates the complete outcome before its first
// durable operation, then atomically stages each partition. Provider and parse
// failures therefore cannot leave proposals or memory writes behind.
//
//nolint:gocyclo // validation, partitioning, staging, deferral, and promotion stay visibly ordered
func processReflectionOutcome(
	ctx context.Context,
	repository learning.ProposalRepository,
	operatorMemory, projectMemory tool.MemoryStore,
	principal string,
	input learning.Input,
	digest string,
	outcome learning.Outcome,
	signals []learning.Signal,
	mode learning.Mode,
	trusted bool,
	projectWorkspace string,
	procedures ...func(context.Context, learning.ProposalRecord, learning.Mode) error,
) (reflectionReceipt, error) {
	var procedure func(context.Context, learning.ProposalRecord, learning.Mode) error
	if len(procedures) > 0 {
		procedure = procedures[0]
	}
	var receipt reflectionReceipt
	if outcome.Kind == learning.OutcomeAbstained {
		receipt.Abstained = true
		return receipt, nil
	}
	if outcome.Kind != learning.OutcomeProposed || len(outcome.Candidates) == 0 {
		return receipt, learning.ErrInvalidOutcome
	}

	workspace := input.Trajectory.Workspace
	operator := stagedGroup{partition: learning.ProposalPartition{Principal: principal}, store: operatorMemory}
	project := stagedGroup{partition: learning.ProposalPartition{Principal: principal, Project: workspace}, store: projectMemory}
	for candidateIndex, candidate := range outcome.Candidates {
		group := &operator
		if candidate.Kind == learning.CandidateProjectFact || candidate.Kind == learning.CandidateProcedure {
			// Project candidates remain reviewable in their source partition. The
			// exact-root trust and convergence checks apply only at promotion/undo.
			if workspace == "" {
				return receipt, learning.ErrInvalidOutcome
			}
			group = &project
		}
		if err := learning.ValidateProposalMaterial(group.partition, digest, candidate, signals); err != nil {
			return receipt, err
		}
		if candidateIndex == 0 {
			group.primary = true
			group.primaryIndex = len(group.items)
		}
		group.items = append(group.items, candidate)
	}

	for _, group := range []*stagedGroup{&operator, &project} {
		if len(group.items) == 0 {
			continue
		}
		records, err := repository.StageBatch(ctx, group.partition, digest, group.items, signals)
		if err != nil {
			return receipt, err
		}
		receipt.Staged += len(records)
		for recordIndex, record := range records {
			isPrimary := group.primary && recordIndex == group.primaryIndex
			if isPrimary {
				receipt.ProposalID = record.ID
				receipt.SkillID = record.SkillID
			}
			switch record.Status {
			case learning.ProposalPromoted:
				receipt.Promoted++
				continue
			case learning.ProposalConflicted:
				receipt.Conflicted++
				continue
			case learning.ProposalRejected, learning.ProposalUndone:
				continue
			case learning.ProposalDeferredUnsupported:
				if record.Candidate.Kind != learning.CandidateProcedure || procedure == nil || mode == learning.Off {
					continue
				}
			}
			if record.Candidate.Kind == learning.CandidateProcedure {
				claimed := record
				var err error
				if record.Status == learning.ProposalStaged {
					claimed, err = repository.ClaimPromotion(ctx, group.partition, record.ID, record.Version)
					if err == nil {
						claimed, err = repository.Finalize(ctx, group.partition, record.ID, claimed.Version,
							learning.ProposalDeferredUnsupported, nil, learning.Decision{
								Kind: learning.DecisionDefer, Actor: "standard-policy",
								Reason: "awaiting learned-skill evaluation",
							})
					}
				}
				if err != nil {
					return receipt, err
				}
				if procedure != nil && mode != learning.Off {
					if err := procedure(ctx, claimed, mode); err != nil {
						return receipt, err
					}
					if isPrimary {
						authoritative, found, err := repository.Get(ctx, group.partition, record.ID)
						if err != nil {
							return receipt, err
						}
						if !found || authoritative.ID != record.ID {
							return receipt, learning.ErrProposalNotFound
						}
						receipt.ProposalID = authoritative.ID
						receipt.SkillID = authoritative.SkillID
					}
				}
				continue
			}
			if mode != learning.Auto || group.store == nil || !autoPromotionEligible(input, record.Candidate, trusted, projectWorkspace) {
				continue
			}
			updated, err := (memorypromotion.Promoter{Proposals: repository, Memory: group.store}).Process(
				ctx, group.partition, record.ID, record.Version,
				memorypromotion.PolicyInput{Mode: mode, TrustedProject: trusted},
			)
			if err != nil {
				return receipt, err
			}
			switch updated.Status {
			case learning.ProposalPromoted:
				receipt.Promoted++
			case learning.ProposalConflicted:
				receipt.Conflicted++
			}
		}
	}
	return receipt, nil
}

func buildReflectionObserver(
	cfg Config,
	provider port.LLMProvider,
	model string,
	operatorMemory, projectMemory tool.MemoryStore,
	repository learning.ProposalRepository,
	coordinator *reflectionCoordinator,
	admission *learningAdmission,
	procedure ...func(context.Context, learning.ProposalRecord, learning.Mode) error,
) learning.Observer {
	if cfg.LearningMode == learning.Off {
		return nil
	}
	return buildConfiguredReflectionObserver(cfg, provider, model, operatorMemory, projectMemory, repository, coordinator, admission, procedure...)
}

func bindMaterializationLifecycle(observer learning.Observer, lifecycle *materializationLifecycle) learning.Observer {
	if reflection, ok := observer.(*reflectionObserver); ok {
		reflection.lifecycle = lifecycle
	}
	return observer
}

func buildExplicitReflectionObserver(
	cfg Config,
	provider port.LLMProvider,
	model string,
	operatorMemory, projectMemory tool.MemoryStore,
	repository learning.ProposalRepository,
	coordinator *reflectionCoordinator,
	procedure ...func(context.Context, learning.ProposalRecord, learning.Mode) error,
) *reflectionObserver {
	if cfg.LearningMode == learning.Off {
		cfg.LearningMode = learning.Review
	}
	observer, _ := buildConfiguredReflectionObserver(cfg, provider, model, operatorMemory, projectMemory, repository, coordinator, nil, procedure...).(*reflectionObserver)
	return observer
}

func buildConfiguredReflectionObserver(
	cfg Config,
	provider port.LLMProvider,
	model string,
	operatorMemory, projectMemory tool.MemoryStore,
	repository learning.ProposalRepository,
	coordinator *reflectionCoordinator,
	admission *learningAdmission,
	procedure ...func(context.Context, learning.ProposalRecord, learning.Mode) error,
) learning.Observer {
	if provider == nil || repository == nil {
		return nil
	}
	if selected, ok := resolveSlotModel(cfg, slotReflection, model); ok && selected != "" {
		model = selected
	}
	if cfg.LearningSensitivity == learning.SensitivityUnset {
		cfg.LearningSensitivity = learning.Balanced
	}
	modelCfg := cfg
	modelCfg.Model = model
	reflector, err := agent.NewEvidenceReflector(provider, model, buildTokenCounter(modelCfg), agent.ReflectionLimits{})
	if err != nil {
		cfg.diag().Log(context.Background(), port.LevelWarn, "automatic reflection unavailable", "error", err)
		return nil
	}
	var processProcedure func(context.Context, learning.ProposalRecord, learning.Mode) error
	if len(procedure) > 0 {
		processProcedure = procedure[0]
	}
	return &reflectionObserver{
		coordinator: coordinator, reflector: reflector, repository: repository,
		attempts: cfg.attemptRepository, ledger: cfg.automaticAdmissionLedger, sourceStore: cfg.learningSourceStore,
		automatic:      cfg.LearningAutomatic,
		operatorMemory: operatorMemory, projectMemory: projectMemory,
		mode: cfg.LearningMode, trusted: projectIngestionAdmitted(cfg), projectWorkspace: cfg.Workspace,
		admission: admission, policy: learning.ThresholdPolicy{Sensitivity: cfg.LearningSensitivity},
		tokenCounter: buildTokenCounter(modelCfg), sensitivity: cfg.LearningSensitivity,
		metrics: cfg.LearningMetricsEmitter, procedure: processProcedure,
	}
}
