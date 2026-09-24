package server

import (
	"context"
	"errors"
	"fmt"

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

type localBrokerAttachment struct {
	attachment brokercontract.Attachment
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

func brokerTools(local *localBrokerAttachment) []tool.Tool {
	if local == nil {
		return nil
	}
	tools := withAttachmentQueryTool(local.attachment, local.attachment.Tools())
	return tools
}

func (s *Service) callSessionEngine(ctx context.Context, id session.SessionID, owner *session.Principal, sel ProviderSelector, specs []mcp.ServerConfig, profile SessionProfile, workspace string, mode session.PermissionMode, sessionTools []tool.Tool) (SessionEngineResult, error) {
	if s.cfg.SessionContextEngine != nil {
		return s.cfg.SessionContextEngine(ctx, id, owner.Clone(), sel, specs, profile, workspace, mode, append([]tool.Tool(nil), sessionTools...))
	}
	if s.cfg.MCPBroker != nil {
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
	if s.cfg.MCPBroker == nil {
		return nil, nil
	}
	if bindingRequired && expectedBinding == "" {
		return nil, fmt.Errorf("%w: session %q has no persisted MCP broker binding", ErrFailedPrecondition, id)
	}
	s.mu.Lock()
	existing := s.brokerAttachments[id]
	s.mu.Unlock()
	if existing != nil {
		if expectedBinding != "" && existing.Binding() != expectedBinding {
			return nil, fmt.Errorf("%w: %w: %w for session %q", ErrFailedPrecondition, brokercontract.ErrStateUnavailable, ErrBrokerBindingMismatch, id)
		}
		return &localBrokerAttachment{attachment: existing}, nil
	}
	attachment, _, err := s.cfg.MCPBroker.AttachSession(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("attach MCP broker session %q: %w", id, err)
	}
	local := &localBrokerAttachment{attachment: attachment, owned: true}
	if expectedBinding == "" || attachment.Binding() == expectedBinding {
		return local, nil
	}
	s.rollbackBrokerAttachment(context.Background(), local)
	return nil, fmt.Errorf("%w: %w: %w for session %q", ErrFailedPrecondition, brokercontract.ErrStateUnavailable, ErrBrokerBindingMismatch, id)
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
func (s *Service) rebindBrokerAttachment(ctx context.Context, sess *session.Session) (*localBrokerAttachment, error) {
	local, err := s.openBrokerAttachment(ctx, sess.ID, "", false)
	if err != nil {
		return nil, err
	}
	committed := false
	defer s.finalizeBrokerAttachment(local, &committed)
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
func (s *Service) deleteBrokerSessionLocked(ctx context.Context, id session.SessionID) error {
	if s.cfg.MCPBroker == nil {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), engineCloseTimeout)
	defer cancel()
	if _, err := s.cfg.MCPBroker.DeleteSession(cleanupCtx, id); err != nil {
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
