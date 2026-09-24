package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// ErrBrokerBindingMismatch marks a session whose persisted binding does not
// match the live broker incarnation. The error it decorates also satisfies
// brokercontract.ErrStateUnavailable, so repair and settlement paths keep
// treating it as lost state — but live authorization controls must hard-fail on
// it rather than silently resolving the authorization as interrupted.
var ErrBrokerBindingMismatch = errors.New("MCP broker binding mismatch")

type brokerRecoveryAttempt struct {
	requestID string
	deadline  time.Time
}

type brokerRecoveryAttemptKey struct {
	sessionID   session.SessionID
	incarnation string
	reference   string
	binding     session.ExternalBinding
}

type localBrokerAttachment struct {
	attachment brokercontract.Attachment
	generation uint64
	outcome    brokercontract.AttachOutcome
	owned      bool
}

// withAttachmentQueryTool appends the attachment-bound CallMcpWithQuery wrapper
// to tools, if the attachment exposes one, mirroring brokerTools' own addition.
// RefreshGrantedAuthorizationCatalogue only returns the declared/authenticated
// catalogue tools, so a caller rebuilding a session engine from its exact tools
// must add this separately, or a parked CallMcpWithQuery call resumes into a
// catalogue that no longer has it registered.
func withAttachmentQueryTool(attachment brokercontract.Attachment, tools []tool.Tool) []tool.Tool {
	if query, ok := attachment.(interface{ CallMcpWithQueryTool() tool.Tool }); ok {
		if candidate := query.CallMcpWithQueryTool(); candidate != nil {
			tools = append(tools, candidate)
		}
	}
	return tools
}

func initialBrokerGeneration(broker brokercontract.Service) uint64 {
	if broker == nil {
		return 0
	}
	return 1
}

func (s *Service) brokerSnapshot() (brokercontract.Service, uint64) {
	s.brokerGenerationMu.RLock()
	defer s.brokerGenerationMu.RUnlock()
	return s.brokerCurrent, s.brokerGeneration
}

func (s *Service) brokerService() brokercontract.Service {
	broker, _ := s.brokerSnapshot()
	return broker
}

func (s *Service) brokerConfigured() bool { return s.brokerService() != nil }

func (s *Service) brokerAttachmentGenerationFor(id session.SessionID) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.brokerAttachmentGeneration[id]
}

func recoveryAttemptKey(sess *session.Session, custody session.BrokerCredentialCustody) brokerRecoveryAttemptKey {
	return brokerRecoveryAttemptKey{
		sessionID:   sess.ID,
		incarnation: string(custody.SessionIncarnation()),
		reference:   custody.RecoveryReference(),
		binding:     sess.ExternalBinding,
	}
}

func (s *Service) discardBrokerRecoveryAttempts(id session.SessionID) {
	s.brokerRecoveryMu.Lock()
	defer s.brokerRecoveryMu.Unlock()
	for key := range s.brokerRecovery {
		if key.sessionID == id {
			delete(s.brokerRecovery, key)
		}
	}
}

// releaseRetiredBrokerClient closes a replaced client only after every cached
// attachment using its generation has been retired. Replacement never closes a
// client below a live attachment.
func (s *Service) releaseRetiredBrokerClient(generation uint64) {
	s.mu.Lock()
	for _, attachedGeneration := range s.brokerAttachmentGeneration {
		if attachedGeneration == generation {
			s.mu.Unlock()
			return
		}
	}
	s.mu.Unlock()

	s.brokerReplacementMu.Lock()
	closeClient := s.brokerRetiredCloses[generation]
	delete(s.brokerRetiredCloses, generation)
	s.brokerReplacementMu.Unlock()
	if closeClient != nil {
		_ = closeClient()
	}
}

func brokerTools(local *localBrokerAttachment) []tool.Tool {
	if local == nil {
		return nil
	}
	tools := withAttachmentQueryTool(local.attachment, local.attachment.Tools())
	return tools
}

func (s *Service) callSessionEngine(ctx context.Context, sel ProviderSelector, specs []mcp.ServerConfig, profile SessionProfile, workspace string, mode session.PermissionMode, sessionTools []tool.Tool) (SessionEngineResult, error) {
	if s.brokerConfigured() {
		if s.cfg.SessionEngineWithTools == nil {
			return SessionEngineResult{}, fmt.Errorf("%w: broker tools require an explicit session catalogue factory", ErrConfig)
		}
		return s.cfg.SessionEngineWithTools(ctx, sel, specs, profile, workspace, mode, append([]tool.Tool(nil), sessionTools...))
	}
	if s.cfg.SessionEngine == nil {
		return SessionEngineResult{}, fmt.Errorf("%w: per-session engine not supported (no session-engine factory configured)", ErrInvalidArgument)
	}
	return s.cfg.SessionEngine(ctx, sel, specs, profile, workspace, mode)
}

func (s *Service) openBrokerAttachment(ctx context.Context, id session.SessionID, expectedBinding session.ExternalBinding, bindingRequired bool) (*localBrokerAttachment, error) {
	s.brokerGenerationMu.RLock()
	defer s.brokerGenerationMu.RUnlock()
	broker := s.brokerCurrent
	if broker == nil {
		return nil, nil
	}
	if bindingRequired && expectedBinding == "" {
		return nil, fmt.Errorf("%w: session %q has no persisted MCP broker binding", ErrFailedPrecondition, id)
	}
	s.mu.Lock()
	existing := s.brokerAttachments[id]
	existingGeneration := s.brokerAttachmentGeneration[id]
	s.mu.Unlock()
	if existing != nil {
		if expectedBinding != "" && existing.Binding() != expectedBinding {
			return nil, fmt.Errorf("%w: %w: %w for session %q", ErrFailedPrecondition, brokercontract.ErrStateUnavailable, ErrBrokerBindingMismatch, id)
		}
		return &localBrokerAttachment{attachment: existing, generation: existingGeneration, outcome: brokercontract.AttachReattached}, nil
	}
	var attachment brokercontract.Attachment
	var outcome brokercontract.AttachOutcome
	var err error
	if expectedBinding != "" {
		if attacher, ok := broker.(brokercontract.ExpectedBindingAttacher); ok {
			attachment, outcome, err = attacher.AttachSessionExpectedBinding(ctx, id, expectedBinding)
		} else {
			attachment, outcome, err = broker.AttachSession(ctx, id)
		}
		if err != nil && errors.Is(err, brokercontract.ErrContinuityUnavailable) {
			// Same broker instance, but the persisted generation is gone. Never
			// create state here: report the mismatch so callers take their
			// explicit rebind (legacy) or fail-closed (custody) path.
			return nil, fmt.Errorf("%w: %w: %w for session %q", ErrFailedPrecondition, brokercontract.ErrStateUnavailable, ErrBrokerBindingMismatch, id)
		}
		if err != nil && errors.Is(err, brokercontract.ErrBrokerIncarnationLost) {
			// A different broker instance is also a binding mismatch: live controls
			// must see a hard precondition failure, not a soft interruption
			// (brokerStateLost). Instance loss stays wrapped so recovery and
			// rebind callers still recognise it.
			return nil, fmt.Errorf("%w: %w: %w for session %q", ErrFailedPrecondition, ErrBrokerBindingMismatch, err, id)
		}
	} else {
		attachment, outcome, err = broker.AttachSession(ctx, id)
	}
	if err != nil {
		return nil, fmt.Errorf("attach MCP broker session %q: %w", id, err)
	}
	_, generation := s.brokerSnapshot()
	local := &localBrokerAttachment{attachment: attachment, generation: generation, outcome: outcome, owned: true}
	if expectedBinding == "" || attachment.Binding() == expectedBinding {
		return local, nil
	}
	s.rollbackBrokerAttachment(context.Background(), local)
	return nil, fmt.Errorf("%w: %w: %w for session %q", ErrFailedPrecondition, brokercontract.ErrStateUnavailable, ErrBrokerBindingMismatch, id)
}

func (s *Service) retireBrokerAttachment(id session.SessionID) {
	s.mu.Lock()
	attachment := s.brokerAttachments[id]
	delete(s.brokerAttachments, id)
	delete(s.brokerAttachmentGeneration, id)
	s.mu.Unlock()
	if attachment == nil {
		return
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), engineCloseTimeout)
	_, err := attachment.Close(closeCtx)
	cancel()
	if err != nil && !errors.Is(err, brokercontract.ErrStateUnavailable) {
		s.cfg.Diagnostics.Log(context.Background(), port.LevelWarn, "MCP broker attachment retirement failed")
	}
}

// rebindBrokerAttachment adopts the live broker incarnation for a session whose
// persisted binding names an incarnation that no longer exists. A Runtime's
// binding prefix is random per process and its generation counter is in memory,
// so after a restart NO persisted binding can ever match again: without an
// adoption seam such a session is stranded for the rest of its life. Callers
// must hold brokerMu for the session and must own a seam where nothing durable
// was built on the lost incarnation — a stale pre-prompt enrollment correlation
// is dropped here because its broker-side transaction died with the incarnation
// that issued it. Live authorization control paths deliberately do NOT rebind:
// they must hard-fail on a mismatch rather than resolve against fresh state.
func (s *Service) rebindBrokerAttachment(ctx context.Context, sess *session.Session, failedGeneration uint64, replaceGeneration bool) (*localBrokerAttachment, error) {
	s.brokerReplacementMu.Lock()
	defer s.brokerReplacementMu.Unlock()
	if s.brokerClosed {
		return nil, fmt.Errorf("%w: MCP broker is closed", ErrUnavailable)
	}
	// A cached attachment belongs to the lost client generation. Remove it before
	// publishing a replacement, or openBrokerAttachment would return the stale
	// handle without touching the fresh broker.
	s.retireBrokerAttachment(sess.ID)
	if replaceGeneration && s.cfg.MCPBrokerFactory != nil {
		_, currentGeneration := s.brokerSnapshot()
		if currentGeneration == failedGeneration {
			fresh, closeFresh, err := s.cfg.MCPBrokerFactory(ctx)
			if err != nil {
				return nil, fmt.Errorf("%w: replace MCP broker client: %v", ErrInternal, err)
			}
			if fresh == nil || closeFresh == nil {
				if closeFresh != nil {
					_ = closeFresh()
				}
				return nil, fmt.Errorf("%w: replacement MCP broker factory returned incomplete service", ErrInternal)
			}
			s.brokerGenerationMu.Lock()
			oldClose := s.brokerFactoryClose
			if oldClose != nil {
				_ = oldClose()
			}
			// brokerCurrent is published only after the old owner has closed, making
			// the handoff atomic to all attachment readers.
			s.brokerCurrent = fresh
			s.brokerFactoryClose = closeFresh
			s.brokerGeneration++
			s.brokerGenerationMu.Unlock()
		}
	}
	local, err := s.openBrokerAttachment(ctx, sess.ID, "", false)
	if err != nil {
		return nil, err
	}
	committed := false
	persisted := false
	defer func() {
		s.finalizeBrokerAttachment(local, &committed)
		if committed && !persisted {
			s.retireBrokerAttachment(sess.ID)
		}
	}()
	commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), engineCloseTimeout)
	commitErr := s.commitBrokerAttachment(commitCtx, sess.ID, local)
	cancelCommit()
	if commitErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrInternal, commitErr)
	}
	committed = true
	if pending, ok := sess.PendingWorkspaceEnrollment(); ok {
		if abortErr := sess.AbortWorkspaceEnrollment(pending.ID); abortErr != nil {
			return nil, fmt.Errorf("%w: clear lost workspace enrollment", ErrFailedPrecondition)
		}
	}
	sess.ExternalBinding = local.attachment.Binding()
	if err := s.saveSession(ctx, sess); err != nil {
		return nil, fmt.Errorf("%w: persist rebound MCP broker attachment", ErrInternal)
	}
	persisted = true
	return local, nil
}

func (s *Service) commitBrokerAttachment(ctx context.Context, id session.SessionID, local *localBrokerAttachment) error {
	if local == nil || !local.owned {
		return nil
	}
	if err := local.attachment.Commit(ctx); err != nil {
		return fmt.Errorf("commit MCP broker attachment: %w", err)
	}
	s.mu.Lock()
	s.brokerAttachments[id] = local.attachment
	s.brokerAttachmentGeneration[id] = local.generation
	s.mu.Unlock()
	local.owned = false
	return nil
}

func (s *Service) rollbackBrokerAttachment(ctx context.Context, local *localBrokerAttachment) {
	if local == nil || !local.owned {
		return
	}
	abortCtx, cancelAbort := context.WithTimeout(context.WithoutCancel(ctx), engineCloseTimeout)
	abortErr := local.attachment.Abort(abortCtx)
	cancelAbort()
	if abortErr != nil {
		s.cfg.Diagnostics.Log(context.Background(), port.LevelWarn, "MCP broker attachment rollback failed")
	}
	local.owned = false
}

// deleteBrokerSessionLocked permanently destroys logical broker state and
// releases its local handle. The caller must hold brokerMu for id. Called only
// AFTER the durable session record is already gone (I-8): retryability comes
// from broker deletion being idempotent (ToolHive's DeleteSession treats an
// already-deleted/never-existed session as success), not from keeping this
// handle around pending a durable delete that has already committed.
func (s *Service) deleteBrokerSessionLocked(ctx context.Context, id session.SessionID, binding session.ExternalBinding) error {
	s.brokerGenerationMu.RLock()
	defer s.brokerGenerationMu.RUnlock()
	broker := s.brokerCurrent
	if broker == nil {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), engineCloseTimeout)
	defer cancel()
	if binding != "" {
		deleter, ok := broker.(brokercontract.BindingSessionDeleter)
		if !ok {
			return fmt.Errorf("%w: broker does not support exact-binding deletion", ErrFailedPrecondition)
		}
		if _, err := deleter.DeleteSessionIfBinding(cleanupCtx, id, binding); err != nil {
			return fmt.Errorf("%w: delete MCP broker logical session: %v", ErrInternal, err)
		}
	} else if _, err := broker.DeleteSession(cleanupCtx, id); err != nil {
		return fmt.Errorf("%w: delete MCP broker logical session: %v", ErrInternal, err)
	}
	s.closeSessionLocal(id)
	return nil
}

func (s *Service) finalizeBrokerAttachment(local *localBrokerAttachment, committed *bool) {
	if local != nil && !*committed {
		s.rollbackBrokerAttachment(context.Background(), local)
	}
}
