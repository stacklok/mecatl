package agent

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func (e *Engine) prepareBrokerAttempt(ctx context.Context, sess *session.Session, call session.ToolCall, broker tool.DurableBrokerInvocation) (context.Context, error) {
	if attempt, ok := tool.BrokerInvocationFromContext(ctx); ok {
		original, err := sess.ContinueBrokerInvocation(call)
		if err != nil || original != attempt {
			return ctx, errors.New("broker continuation does not match original attempt")
		}
		return ctx, nil
	}
	ref, catalogue := broker.BrokerInvocationRefs()
	attempt, err := sess.PrepareBrokerInvocation(ref, catalogue, call, e.now())
	if err != nil {
		return ctx, err
	}
	return tool.WithBrokerInvocation(ctx, attempt), nil
}

func (e *Engine) recordBrokerResults(ctx context.Context, sess *session.Session, results []session.ToolResult) error {
	before, exists := sess.BrokerAccess()
	if err := sess.RecordToolResults(results); err != nil {
		return err
	}
	if err := e.saveRequired(ctx, sess); err != nil {
		if exists {
			// A transcript in memory is not a durable result. Restore uncertainty
			// and discard the local completion receipt before allowing another run.
			return errors.Join(err, sess.RestoreBrokerAccess(before))
		}
		return err
	}
	return nil
}
