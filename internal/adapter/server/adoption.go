package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// AdoptionReason is a stable machine-readable explanation for legacy-adoption
// eligibility. An empty reason accompanies an eligible source.
type AdoptionReason string

const (
	// AdoptionReasonNotLegacy means the source already has a non-legacy kind.
	AdoptionReasonNotLegacy AdoptionReason = "not_legacy"
	// AdoptionReasonProtectedProvenance means reserved lineage or an ID prefix was found.
	AdoptionReasonProtectedProvenance AdoptionReason = "protected_provenance"
	// AdoptionReasonInvalidTranscript means the authoritative history is unavailable or unpaired.
	AdoptionReasonInvalidTranscript AdoptionReason = "invalid_transcript"
	// AdoptionReasonActive means the source is running or live in this process.
	AdoptionReasonActive AdoptionReason = "active"
	// AdoptionReasonAwaiting means the source has an unresolved approval.
	AdoptionReasonAwaiting AdoptionReason = "awaiting_approval"
	// AdoptionReasonLeased means another process owns the source mutation lease.
	AdoptionReasonLeased AdoptionReason = "leased"
	// AdoptionReasonBindingUnresolved means an explicit environment or model binding failed.
	AdoptionReasonBindingUnresolved AdoptionReason = "binding_unresolved"
)

// AdoptionBindings are the explicit execution and model bindings for the new
// main session. Adoption never interprets an omitted field as a server default.
type AdoptionBindings struct {
	Workspace      string
	EnvironmentRef session.EnvironmentRef
	ProviderID     string
	ModelID        string
	Profile        SessionProfile
}

// AdoptionPreflight is the capability-driven result for one owned legacy source.
type AdoptionPreflight struct {
	Eligible bool
	Reason   AdoptionReason
	Bindings AdoptionBindings
}

func (s *Service) adoptionOwnershipPreflight(ctx context.Context, id session.SessionID) (AdoptionReason, error) {
	if !s.cfg.OwnershipEnforced || session.PrincipalFromContext(ctx) == nil {
		return "", fmt.Errorf("%w", ErrNotFound)
	}
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		if errors.Is(err, port.ErrSessionNotFound) {
			return "", fmt.Errorf("%w", ErrNotFound)
		}
		return AdoptionReasonInvalidTranscript, nil
	}
	if sess == nil || sess.ID != id || s.authorizeSession(ctx, sess) != nil {
		return "", fmt.Errorf("%w", ErrNotFound)
	}
	return "", nil
}

func (s *Service) adoptionSource(ctx context.Context, id session.SessionID) (*session.Session, AdoptionReason, error) {
	if !s.cfg.OwnershipEnforced || session.PrincipalFromContext(ctx) == nil {
		return nil, "", fmt.Errorf("%w", ErrNotFound)
	}
	sess, err := s.cfg.Store.Load(ctx, id)
	if err != nil {
		if errors.Is(err, port.ErrSessionNotFound) {
			return nil, "", fmt.Errorf("%w", ErrNotFound)
		}
		return nil, AdoptionReasonInvalidTranscript, nil
	}
	if sess == nil || sess.ID != id || s.authorizeSession(ctx, sess) != nil {
		return nil, "", fmt.Errorf("%w", ErrNotFound)
	}
	if hasLegacyNonChatPrefix(id) || sess.Relationship != (session.SessionRelationship{}) {
		return sess, AdoptionReasonProtectedProvenance, nil
	}
	if sess.Kind != session.SessionKindUnknown {
		return sess, AdoptionReasonNotLegacy, nil
	}
	if err := session.ValidateSessionMetadata(sess.Kind, sess.Relationship); err != nil {
		return sess, AdoptionReasonProtectedProvenance, nil
	}
	if sess.State == session.StateAwaiting {
		return sess, AdoptionReasonAwaiting, nil
	}
	if sess.State == session.StateRunning || s.IsLive(id) {
		return sess, AdoptionReasonActive, nil
	}
	if sess.State != session.StateIdle && !sess.State.IsTerminal() {
		return sess, AdoptionReasonActive, nil
	}
	if sess.Conversation == nil || session.ValidateToolPairing(sess.Conversation.Messages) != nil {
		return sess, AdoptionReasonInvalidTranscript, nil
	}
	return sess, "", nil
}

func validateAdoptionBindingShape(bindings AdoptionBindings) error {
	if strings.TrimSpace(bindings.ProviderID) == "" || strings.TrimSpace(bindings.ModelID) == "" {
		return fmt.Errorf("%w: adoption requires explicit provider_id and model_id", ErrInvalidArgument)
	}
	switch bindings.Profile {
	case ProfileDefault:
		if bindings.Workspace == "" {
			return fmt.Errorf("%w: adoption requires an explicit workspace", ErrInvalidArgument)
		}
	case ProfileNoFS:
		if bindings.Workspace != "" {
			return fmt.Errorf("%w: no-fs adoption must not carry a workspace", ErrInvalidArgument)
		}
	default:
		return fmt.Errorf("%w: unknown adoption profile", ErrInvalidArgument)
	}
	if bindings.EnvironmentRef.Kind == "" {
		return fmt.Errorf("%w: adoption requires an explicit environment", ErrInvalidArgument)
	}
	return nil
}

func (s *Service) resolveAdoptionEnvironment(ctx context.Context, bindings AdoptionBindings) error {
	ref := bindings.EnvironmentRef
	switch ref.Kind {
	case session.EnvKindLocal, session.EnvKindMem:
		if bindings.Workspace == "" || ref.ID != bindings.Workspace {
			return fmt.Errorf("%w: environment does not bind the requested workspace", ErrInvalidArgument)
		}
	case session.EnvKindNoFS:
		if bindings.Profile != ProfileNoFS || ref.ID != "" {
			return fmt.Errorf("%w: nofs environment requires the no-fs profile", ErrInvalidArgument)
		}
	default:
		if s.cfg.EnvironmentResolver == nil {
			return fmt.Errorf("%w: environment binding cannot be resolved", ErrFailedPrecondition)
		}
		env, err := s.cfg.EnvironmentResolver(ctx, ref)
		if err != nil || env.Workspace() == nil || env.Ref() != ref {
			return fmt.Errorf("%w: environment binding cannot be resolved", ErrFailedPrecondition)
		}
	}
	return nil
}

func (s *Service) resolveAdoptionBindings(ctx context.Context, bindings AdoptionBindings, mode session.PermissionMode) (SessionEngineResult, error) {
	if s.cfg.SessionEngine == nil {
		return SessionEngineResult{}, fmt.Errorf("%w: adoption requires a session-engine factory", ErrInvalidArgument)
	}
	if err := validateAdoptionBindingShape(bindings); err != nil {
		return SessionEngineResult{}, err
	}
	if err := s.resolveAdoptionEnvironment(ctx, bindings); err != nil {
		return SessionEngineResult{}, err
	}
	res, err := s.cfg.SessionEngine(ctx, ProviderSelector{ProviderID: bindings.ProviderID, ModelID: bindings.ModelID}, nil, bindings.Profile, bindings.Workspace, mode)
	if err != nil || res.Engine == nil || res.Close == nil || res.ProviderID == "" || res.ModelID == "" {
		if err == nil {
			err = errors.New("incomplete session-engine resolution")
		}
		return SessionEngineResult{}, fmt.Errorf("%w: provider/model binding cannot be resolved: %v", ErrFailedPrecondition, err)
	}
	return res, nil
}

// PreflightSessionAdoption reports whether an authenticated caller-owned legacy
// source can be adopted with the supplied explicit bindings. It is read-only;
// the short trial lease is released before return and every condition is checked
// again by AdoptSession under the mutation lease.
func (s *Service) PreflightSessionAdoption(ctx context.Context, id session.SessionID, bindings AdoptionBindings) (AdoptionPreflight, error) {
	result := AdoptionPreflight{Bindings: bindings}
	reason, err := s.adoptionOwnershipPreflight(ctx, id)
	if err != nil {
		return result, err
	}
	if reason != "" {
		result.Reason = reason
		return result, nil
	}
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	_, reason, err = s.adoptionSource(ctx, id)
	if err != nil {
		return result, err
	}
	if reason != "" {
		result.Reason = reason
		return result, nil
	}
	release, err := s.acquireMutationLease(ctx, id)
	if err != nil {
		if errors.Is(err, ErrSessionLeasedElsewhere) {
			result.Reason = AdoptionReasonLeased
			return result, nil
		}
		return result, err
	}
	sess, reason, err := s.adoptionSource(ctx, id)
	release()
	if err != nil {
		return result, err
	}
	if reason != "" {
		result.Reason = reason
		return result, nil
	}
	resolved, err := s.resolveAdoptionBindings(ctx, bindings, sess.Mode)
	if err != nil {
		result.Reason = AdoptionReasonBindingUnresolved
		return result, nil
	}
	_ = resolved.Close()
	result.Bindings.ProviderID = resolved.ProviderID
	result.Bindings.ModelID = resolved.ModelID
	result.Eligible = true
	return result, nil
}

func adoptionTargetID(principal *session.Principal, source session.SessionID, key string) session.SessionID {
	h := sha256.New()
	for _, value := range []string{principal.Issuer, principal.Subject, string(source), key} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(value))
	}
	return session.SessionID(base64.RawURLEncoding.EncodeToString(h.Sum(nil)))
}

func adoptionRequestDigest(principal *session.Principal, source session.SessionID, key string, bindings AdoptionBindings) string {
	payload, _ := json.Marshal(struct {
		Issuer, Subject, Source, Key string
		Bindings                     AdoptionBindings
	}{principal.Issuer, principal.Subject, string(source), key, bindings})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (s *Service) existingAdoption(ctx context.Context, id, source session.SessionID, owner *session.Principal, digest string) (*session.Session, error) {
	existing, err := s.cfg.Store.Load(ctx, id)
	if errors.Is(err, port.ErrSessionNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: load adoption target", ErrInternal)
	}
	if existing == nil || !sameCreateOwner(existing.Owner, owner) {
		return nil, fmt.Errorf("%w", ErrNotFound)
	}
	metadata := existing.Adoption
	if existing.Kind != session.SessionKindMain || metadata == nil || metadata.AdoptionSourceID != source || metadata.AdoptionRequestDigest != digest {
		return nil, fmt.Errorf("%w: idempotency key was reused with a different adoption request", ErrInvalidArgument)
	}
	return existing, nil
}

func (s *Service) newAdoptionTarget(source *session.Session, targetID session.SessionID, owner *session.Principal, digest string, bindings AdoptionBindings, resolved SessionEngineResult) (*session.Session, error) {
	history := session.ForkSnapshot(source.Conversation)
	sourceProvider := source.ProviderID
	if sourceProvider == "" {
		sourceProvider = s.cfg.DefaultResolvedModel.ProviderID
	}
	if sourceProvider != resolved.ProviderID {
		history = session.StripProviderState(history)
		if usesResponsesReplayIDs(resolved.ProviderID) {
			history = synthesizeOpenAIItemIDs(history)
		}
	}
	target := session.New(targetID, source.Mode, bindings.Workspace, source.Limits, s.cfg.Now())
	if err := target.SeedHistory(history); err != nil {
		return nil, fmt.Errorf("%w: invalid authoritative transcript", ErrFailedPrecondition)
	}
	authority, _ := source.BoundAuthority()
	if err := setSessionLabels(target, ProviderSelector{ProviderID: bindings.ProviderID, ModelID: bindings.ModelID}, bindings.Profile, owner, authority); err != nil {
		return nil, err
	}
	target.EnvironmentRef = bindings.EnvironmentRef
	target.Title = source.Title
	target.TitleProvenance = source.TitleProvenance
	target.Adoption = &session.AdoptionMetadata{
		AdoptionSourceID:      source.ID,
		AdoptionRequestDigest: digest,
	}
	return target, nil
}

func adoptionSourceID(s *session.Session) session.SessionID {
	if s.Adoption == nil {
		return ""
	}
	return s.Adoption.AdoptionSourceID
}

func (s *Service) publishAdoption(ctx context.Context, target *session.Session, sourceID session.SessionID, owner *session.Principal, digest string) (*session.Session, bool, error) {
	if err := s.persistNewSession(ctx, target); err != nil {
		s.mu.Lock()
		delete(s.sessionEngines, target.ID)
		delete(s.sessionEnvironments, target.ID)
		s.mu.Unlock()
		if errors.Is(err, port.ErrSessionAlreadyExists) {
			existing, collisionErr := s.existingAdoption(ctx, target.ID, sourceID, owner, digest)
			if existing == nil && collisionErr == nil {
				return nil, false, fmt.Errorf("%w: adoption target disappeared after create collision", ErrInternal)
			}
			return existing, false, collisionErr
		}
		return nil, false, fmt.Errorf("server: persist adopted session: %w", err)
	}
	return target, true, nil
}

// AdoptSession atomically publishes a new explicit-main copy of one eligible
// legacy source. The source is never transitioned or saved. The target ID and
// persisted request digest make retries durable and caller/source-bound.
func (s *Service) AdoptSession(ctx context.Context, sourceID session.SessionID, idempotencyKey string, bindings AdoptionBindings) (*session.Session, error) {
	if strings.TrimSpace(idempotencyKey) == "" || len(idempotencyKey) > 256 {
		return nil, fmt.Errorf("%w: idempotency_key is required and must be at most 256 bytes", ErrInvalidArgument)
	}
	reason, err := s.adoptionOwnershipPreflight(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		return nil, fmt.Errorf("%w: adoption ineligible: %s", ErrFailedPrecondition, reason)
	}
	unlockSource := s.runEntryMu.lock(sourceID)
	defer unlockSource()
	_, reason, err = s.adoptionSource(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		return nil, fmt.Errorf("%w: adoption ineligible: %s", ErrFailedPrecondition, reason)
	}
	release, err := s.acquireMutationLease(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	defer release()
	// Revalidate after the lease acquisition; another replica may have changed the
	// authoritative snapshot between the advisory preflight and this mutation.
	source, reason, err := s.adoptionSource(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		return nil, fmt.Errorf("%w: adoption ineligible: %s", ErrFailedPrecondition, reason)
	}

	owner := session.PrincipalFromContext(ctx)
	targetID := adoptionTargetID(owner, sourceID, idempotencyKey)
	digest := adoptionRequestDigest(owner, sourceID, idempotencyKey, bindings)
	unlockTarget := s.runEntryMu.lock(targetID)
	defer unlockTarget()
	if existing, err := s.existingAdoption(ctx, targetID, sourceID, owner, digest); err != nil || existing != nil {
		return existing, err
	}

	resolved, err := s.resolveAdoptionBindings(ctx, bindings, source.Mode)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFailedPrecondition, AdoptionReasonBindingUnresolved)
	}
	closeFn := resolved.Close
	cleanup := true
	defer func() {
		if cleanup {
			_ = closeFn()
		}
	}()

	target, err := s.newAdoptionTarget(source, targetID, owner, digest, bindings, resolved)
	if err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if len(s.sessionEngines) >= s.cfg.MaxSessionEngines {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: %d", ErrTooManySessionEngines, s.cfg.MaxSessionEngines)
	}
	s.sessionEngines[targetID] = &sessionEngine{engine: resolved.Engine, caps: resolved.Capabilities, providerID: resolved.ProviderID, modelID: resolved.ModelID, reasoningEffort: resolved.ReasoningEffort, builtForMode: resolved.BuiltForMode, close: closeFn}
	if bindings.Profile == ProfileNoFS {
		s.sessionEnvironments[targetID] = tool.MustEnvironment(bindings.EnvironmentRef, nofs.New(), nil)
	}
	s.mu.Unlock()
	published, ownsPublication, err := s.publishAdoption(ctx, target, sourceID, owner, digest)
	cleanup = !ownsPublication
	return published, err
}
