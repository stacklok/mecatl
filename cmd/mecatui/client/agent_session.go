package client

import (
	"context"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// CreateSessionWithAgent allocates a session bound to a named AgentDef (ADR
// 0353) instead of the default explorer session: its catalog is built
// exclusively from the resolved definition's own tools, provider/model,
// limits, and permission mode. A dedicated method, mirroring ClearSession's
// one-method-per-distinct-session-shaping-call idiom, rather than an added
// ModelSelection field — the binding is a request-shape concern
// (CreateSessionRequest.agent_definition_name), not a model selector.
// An unknown agentDefName fails with ErrInvalidArgument (IsInvalidArgument).
func (c *Client) CreateSessionWithAgent(ctx context.Context, mode mecatlv1.PermissionMode, sel ModelSelection, agentDefName string) (string, Capabilities, ResolvedModel, error) {
	return c.createSession(ctx, &mecatlv1.CreateSessionRequest{
		Mode:                mode,
		ProviderId:          sel.ProviderID,
		ModelId:             sel.ModelID,
		ReasoningEffort:     sel.ReasoningEffort,
		AgentDefinitionName: agentDefName,
	})
}
