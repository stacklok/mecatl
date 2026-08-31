package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

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
	controller       *automaticAdmissionController
	tokenCounter     agent.TokenCounter
	sensitivity      learning.Sensitivity
	metrics          func(learning.Activity)
	procedure        func(context.Context, learning.ProposalRecord, learning.Mode) error
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
	if o == nil || o.mode == learning.Off || o.coordinator == nil {
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

func (o *reflectionObserver) createDurableAttempt(ctx context.Context, input learning.Input, owner *session.Principal, class learning.AdmissionClass) (learning.AttemptRecord, bool, error) {
	if o.attempts == nil {
		return learning.AttemptRecord{}, false, errors.New("durable learning attempt repository is not configured")
	}
	if o.sourceStore == nil || input.Trajectory.RunID == "" {
		return learning.AttemptRecord{}, false, errors.New("durable learning admission requires a persisted ADR-0249 run ID")
	}
	persisted, err := o.sourceStore.Load(ctx, input.Trajectory.SessionID)
	if err != nil || persisted.RunID() != input.Trajectory.RunID {
		return learning.AttemptRecord{}, false, errors.New("durable learning admission requires an exact persisted ADR-0249 run ID")
	}
	digest, err := automaticTrajectoryDigest("", input)
	if err != nil {
		return learning.AttemptRecord{}, false, err
	}
	source := learning.AttemptSource{
		SessionID:       input.Trajectory.SessionID,
		RunID:           learning.DurableRunID(input.Trajectory.RunID),
		CanonicalDigest: learning.CanonicalDigest(digest),
	}
	prompt, err := currentPromptBinding(input, class)
	if err != nil {
		return learning.AttemptRecord{}, false, err
	}
	provenance, err := learning.NewAdmissionProvenance(class, source, prompt)
	if err != nil {
		return learning.AttemptRecord{}, false, err
	}
	caller := reflectionPrincipal(owner)
	partition, err := learning.DeriveAttemptPartition(caller)
	if err != nil {
		return learning.AttemptRecord{}, false, err
	}
	id, err := learning.DeterministicAttemptID(caller, source)
	if err != nil {
		return learning.AttemptRecord{}, false, err
	}
	if existing, found, getErr := o.attempts.Get(ctx, partition, id); getErr != nil {
		return learning.AttemptRecord{}, false, getErr
	} else if found {
		return existing, true, nil
	}
	record, err := o.attempts.Create(ctx, partition, learning.AttemptCreate{ID: id, Provenance: provenance})
	return record, false, err
}

//nolint:gocyclo // explicit/direct and automatic/queued paths share one bounded input and disposition funnel
func (o *reflectionObserver) submit(ctx context.Context, trajectory learning.Trajectory, events []session.Event, async, automatic bool) (reflectionReceipt, error) {
	if o == nil || o.reflector == nil || o.repository == nil {
		return reflectionReceipt{}, errors.New("reflection is not configured")
	}
	if size := reflectionTrajectoryBytes(trajectory) + reflectionEventsBytes(events, defaultReflectionJobBytes); size > defaultReflectionJobBytes {
		return reflectionReceipt{}, fmt.Errorf("reflection input exceeds %d-byte job limit", defaultReflectionJobBytes)
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
	input := learning.NewInput(trajectory, events, hostSignals, memoryExisting(ctx, stores...))
	signals := append([]learning.Signal(nil), hostSignals...)
	signals = append(signals, learning.DetectSignals(input)...)
	decision := learning.AdmissionDecision{Admitted: true, Class: learning.AdmissionHostRequested, Reasons: []learning.AdmissionReason{learning.ReasonHostRequested}, Signals: signals}
	if automatic {
		policy := o.policy
		if policy == nil {
			policy = learning.ThresholdPolicy{Sensitivity: o.sensitivity}
		}
		decision = policy.Decide(learning.AdmissionRequest{Input: input})
		o.emitAdmission(decision)
		if !decision.Admitted {
			return reflectionReceipt{}, nil
		}
		signals = decision.Signals
	}
	automaticDigest, digestErr := automaticTrajectoryDigest(reflectionPrincipal(owner), input)
	if digestErr != nil {
		return reflectionReceipt{}, digestErr
	}
	reservationTokens := 0
	if automatic {
		estimator, ok := o.reflector.(interface {
			RequestTokenEstimate(learning.Input) (int, error)
		})
		if !ok {
			return reflectionReceipt{}, errors.New("automatic reflection requires an exact request token estimator")
		}
		reservationTokens, digestErr = estimator.RequestTokenEstimate(input)
		if digestErr != nil {
			return reflectionReceipt{}, digestErr
		}
	}
	if o.coordinator == nil {
		jobCtx, cancel := context.WithTimeout(ctx, defaultReflectionJobTimeout)
		defer cancel()
		outcome, err := o.reflector.Reflect(jobCtx, input)
		if err != nil {
			return reflectionReceipt{Disposition: reflectionFailed}, err
		}
		digest, err := reflectionInputDigest(input)
		if err != nil {
			return reflectionReceipt{Disposition: reflectionFailed}, err
		}
		receipt := reflectionReceipt{ID: reflectionJobID(reflectionPrincipal(owner) + "\x00" + string(input.Trajectory.SessionID) + "\x00" + digest), Disposition: reflectionCompleted}
		if outcome.Kind == learning.OutcomeAbstained {
			receipt.Abstained = true
			return receipt, nil
		}
		processed, err := o.job(input, signals, owner, "", nil, nil).process(jobCtx, digest, outcome)
		processed.ID, processed.Disposition = receipt.ID, reflectionCompleted
		return processed, err
	}
	// The legacy interval is a post-threshold downsampler. Hard and explicit
	// requests bypass it; 0/1 are inert.
	if automatic && decision.Class == learning.AdmissionWeighted && o.admission != nil && !o.admission.admit() {
		return reflectionReceipt{}, nil
	}
	attempt, existingAttempt, err := o.createDurableAttempt(ctx, input, owner, decision.Class)
	if err != nil {
		return reflectionReceipt{Disposition: reflectionFailed}, err
	}
	if existingAttempt && attempt.State != learning.AttemptQueued {
		return reflectionReceipt{ID: string(attempt.ID), Disposition: reflectionDuplicate}, nil
	}
	var reserve func() bool
	var complete func(reflectionReceipt)
	if automatic && o.controller != nil {
		reserve = func() bool {
			return o.controller.reserve(reflectionPrincipal(owner), automaticDigest, reservationTokens, decision.Class, o.sensitivity)
		}
		complete = func(receipt reflectionReceipt) {
			o.controller.complete(automaticDigest)
			o.emitReflection(receipt)
		}
	}
	receipt, err := o.coordinator.Enqueue(o.job(input, signals, owner, automaticDigest, reserve, complete, string(attempt.ID)))
	if automatic && o.metrics != nil {
		kind := learning.ActivityKind("")
		reason := learning.AdmissionReason("")
		switch receipt.Disposition {
		case reflectionDuplicate:
			kind, reason = learning.ActivityDuplicate, learning.ReasonDuplicate
		case reflectionQueueFull:
			kind, reason = learning.ActivityQueueFull, learning.ReasonQueueFull
		case reflectionClosed:
			kind, reason = learning.ActivityClosed, learning.ReasonCoordinatorClosed
		}
		if kind.Valid() {
			o.metrics(learning.Activity{Kind: kind, Reason: reason, Sensitivity: o.sensitivity, Count: 1})
		}
	}
	if err != nil || async || receipt.Disposition != reflectionQueued && receipt.Disposition != reflectionDuplicate {
		return receipt, err
	}
	done, waitErr := o.coordinator.Wait(ctx, receipt.ID)
	if waitErr != nil {
		return receipt, waitErr
	}
	return done, nil
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

func (o *reflectionObserver) emitReflection(receipt reflectionReceipt) {
	if o == nil || o.metrics == nil {
		return
	}
	emit := func(kind learning.ActivityKind, reason learning.AdmissionReason, count int) {
		if count > 0 {
			o.metrics(learning.Activity{Kind: kind, Reason: reason, Sensitivity: o.sensitivity, Count: int64(count)})
		}
	}
	if receipt.Disposition == reflectionTimedOut {
		emit(learning.ActivityTimedOut, learning.ReasonTimeout, 1)
		return
	}
	if receipt.Disposition == reflectionFailed {
		emit(learning.ActivityFailed, learning.ReasonReflectionFailed, 1)
		return
	}
	if receipt.Abstained {
		emit(learning.ActivityAbstained, learning.ReasonAbstained, 1)
	}
	emit(learning.ActivityStaged, learning.ReasonStaged, receipt.Staged)
	emit(learning.ActivityPromoted, learning.ReasonPromoted, receipt.Promoted)
	emit(learning.ActivityConflicted, learning.ReasonConflicted, receipt.Conflicted)
}

func (o *reflectionObserver) job(input learning.Input, signals []learning.Signal, owner *session.Principal, dedupeKey string, reserve func() bool, complete func(reflectionReceipt), durableIDs ...string) reflectionJob {
	principal := reflectionPrincipal(owner)
	durableID := ""
	if len(durableIDs) > 0 {
		durableID = durableIDs[0]
	}
	return reflectionJob{
		principal: principal,
		input:     input,
		reflector: o.reflector,
		dedupeKey: dedupeKey,
		durableID: durableID,
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
	partition learning.ProposalPartition
	store     tool.MemoryStore
	items     []learning.Candidate
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
	for _, candidate := range outcome.Candidates {
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
		for _, record := range records {
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
		attempts: cfg.attemptRepository, sourceStore: cfg.learningSourceStore,
		operatorMemory: operatorMemory, projectMemory: projectMemory,
		mode: cfg.LearningMode, trusted: projectIngestionAdmitted(cfg), projectWorkspace: cfg.Workspace,
		admission: admission, policy: learning.ThresholdPolicy{Sensitivity: cfg.LearningSensitivity},
		controller: func() *automaticAdmissionController {
			if admission != nil {
				return admission.controller
			}
			return nil
		}(),
		tokenCounter: buildTokenCounter(modelCfg), sensitivity: cfg.LearningSensitivity,
		metrics: cfg.LearningMetricsEmitter, procedure: processProcedure,
	}
}
