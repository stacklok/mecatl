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
	operatorMemory   tool.MemoryStore
	projectMemory    tool.MemoryStore
	mode             learning.Mode
	trusted          bool
	projectWorkspace string
	admission        *learningAdmission
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
	input := learning.NewInput(trajectory, events, nil, memoryExisting(ctx, stores...))
	signals := learning.DetectSignals(input)
	if automatic && len(signals) == 0 {
		return reflectionReceipt{}, nil
	}
	if o.coordinator == nil {
		outcome, err := o.reflector.Reflect(ctx, input)
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
		processed, err := o.job(input, signals, owner).process(ctx, digest, outcome)
		processed.ID, processed.Disposition = receipt.ID, reflectionCompleted
		return processed, err
	}
	// The legacy interval is a process-wide admission/debounce after the signal
	// gate, so trivial completions neither spend a call nor consume its cadence.
	if automatic && o.admission != nil && !o.admission.admit() {
		return reflectionReceipt{}, nil
	}
	receipt, err := o.coordinator.Enqueue(o.job(input, signals, owner))
	if err != nil || async || receipt.Disposition != reflectionQueued && receipt.Disposition != reflectionDuplicate {
		return receipt, err
	}
	done, waitErr := o.coordinator.Wait(ctx, receipt.ID)
	if waitErr != nil {
		return receipt, waitErr
	}
	return done, nil
}

func (o *reflectionObserver) job(input learning.Input, signals []learning.Signal, owner *session.Principal) reflectionJob {
	principal := reflectionPrincipal(owner)
	return reflectionJob{
		principal: principal,
		input:     input,
		reflector: o.reflector,
		process: func(ctx context.Context, digest string, outcome learning.Outcome) (reflectionReceipt, error) {
			return processReflectionOutcome(memoryadapter.WithWorkspace(reflectionContext(ctx, owner), input.Trajectory.Workspace), o.repository,
				o.operatorMemory, o.projectMemory, principal, input,
				digest, outcome, signals, o.mode, o.trusted, o.projectWorkspace)
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
	userEvidence := func(ref learning.EvidenceRef) bool {
		return ref.Locator == learning.EvidenceMessage && ref.Ordinal >= 0 && ref.Ordinal < len(input.Trajectory.Messages) && session.IsGenuineUserPrompt(input.Trajectory.Messages[ref.Ordinal])
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
) (reflectionReceipt, error) {
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
			case learning.ProposalRejected, learning.ProposalDeferredUnsupported, learning.ProposalUndone:
				continue
			}
			if record.Candidate.Kind == learning.CandidateProcedure {
				claimed := record
				var err error
				if record.Status == learning.ProposalStaged {
					claimed, err = repository.ClaimPromotion(ctx, group.partition, record.ID, record.Version)
				}
				if err != nil {
					return receipt, err
				}
				_, err = repository.Finalize(ctx, group.partition, record.ID, claimed.Version,
					learning.ProposalDeferredUnsupported, nil, learning.Decision{
						Kind: learning.DecisionDefer, Actor: "standard-policy",
						Reason: "procedure promotion is deferred",
					})
				if err != nil {
					return receipt, err
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
) learning.Observer {
	if cfg.LearningMode == learning.Off {
		return nil
	}
	return buildConfiguredReflectionObserver(cfg, provider, model, operatorMemory, projectMemory, repository, coordinator, admission)
}

func buildExplicitReflectionObserver(
	cfg Config,
	provider port.LLMProvider,
	model string,
	operatorMemory, projectMemory tool.MemoryStore,
	repository learning.ProposalRepository,
	coordinator *reflectionCoordinator,
) *reflectionObserver {
	if cfg.LearningMode == learning.Off {
		cfg.LearningMode = learning.Review
	}
	observer, _ := buildConfiguredReflectionObserver(cfg, provider, model, operatorMemory, projectMemory, repository, coordinator, nil).(*reflectionObserver)
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
) learning.Observer {
	if provider == nil || repository == nil {
		return nil
	}
	if selected, ok := resolveSlotModel(cfg, slotReflection, model); ok && selected != "" {
		model = selected
	}
	modelCfg := cfg
	modelCfg.Model = model
	reflector, err := agent.NewEvidenceReflector(provider, model, buildTokenCounter(modelCfg), agent.ReflectionLimits{})
	if err != nil {
		cfg.diag().Log(context.Background(), port.LevelWarn, "automatic reflection unavailable", "error", err)
		return nil
	}
	return &reflectionObserver{
		coordinator: coordinator, reflector: reflector, repository: repository,
		operatorMemory: operatorMemory, projectMemory: projectMemory,
		mode: cfg.LearningMode, trusted: projectIngestionAdmitted(cfg), projectWorkspace: cfg.Workspace, admission: admission,
	}
}
