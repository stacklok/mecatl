package server

import (
	"context"
	"fmt"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type localBrokerAttachment struct {
	attachment brokercontract.Attachment
	owned      bool
}

func brokerTools(local *localBrokerAttachment) []tool.Tool {
	if local == nil {
		return nil
	}
	return local.attachment.Tools()
}

func (s *Service) callSessionEngine(ctx context.Context, sel ProviderSelector, specs []mcp.ServerConfig, profile SessionProfile, workspace string, mode session.PermissionMode, sessionTools []tool.Tool) (SessionEngineResult, error) {
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
			return nil, fmt.Errorf("%w: %w: MCP broker binding mismatch for session %q", ErrFailedPrecondition, brokercontract.ErrStateUnavailable, id)
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
	return nil, fmt.Errorf("%w: %w: MCP broker binding mismatch for session %q", ErrFailedPrecondition, brokercontract.ErrStateUnavailable, id)
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

// deleteBrokerSessionLocked permanently destroys logical broker state before
// releasing its local handle. The caller must hold brokerMu for id. Keeping the
// local handle until deletion succeeds leaves the host session retryable when a
// remote broker rejects or times out the mutation.
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
