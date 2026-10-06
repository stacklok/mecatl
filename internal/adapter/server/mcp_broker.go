package server

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

func (s *Service) brokerConfigured() bool { return s.cfg.SessionBroker != nil }

func (s *Service) callSessionEngine(ctx context.Context, id session.SessionID, owner *session.Principal, acquire ExecutionWorkspaceAcquirer, sel ProviderSelector, specs []mcp.ServerConfig, profile SessionProfile, workspace string, mode session.PermissionMode, sessionTools []tool.Tool) (SessionEngineResult, error) {
	if s.cfg.SessionContextEngine != nil {
		return s.cfg.SessionContextEngine(ctx, id, owner.Clone(), acquire, sel, specs, profile, workspace, mode, append([]tool.Tool(nil), sessionTools...))
	}
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

// Persisted native broker markers must never acquire remote or direct authority.
func rejectRetiredBrokerSession(sess *session.Session) error {
	_, custody := sess.BrokerCredentialCustody()
	_, access := sess.BrokerAccess()
	enrollment, pending := sess.PendingWorkspaceEnrollment()
	ref, refErr := base64.RawURLEncoding.Strict().DecodeString(string(enrollment.ID))
	if sess.ExternalBinding != "" || custody || pending && (!access || refErr != nil || len(ref) != 32) {
		return fmt.Errorf("%w: Unsupported broker session after retirement; create a new session.", ErrFailedPrecondition)
	}
	return nil
}
