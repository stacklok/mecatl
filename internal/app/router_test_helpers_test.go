package app

import (
	"context"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

func callModelRouter(ctx context.Context, router *agent.SubagentModelRouter, task string) (category, model string, usage session.Usage, reason string, ok bool) {
	result := router.Route(ctx, task)
	return result.Category, result.Model, result.Usage, result.Reason, result.OK
}
