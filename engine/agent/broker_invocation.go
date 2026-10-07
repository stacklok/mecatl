package agent

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// reconcileBrokerAttempt never invokes or recreates a call. Only authenticated,
// exact passive evidence can release an allocated or admitted nonexecuted slot.
func (e *Engine) reconcileBrokerAttempt(ctx context.Context, sess *session.Session, broker tool.DurableBrokerInvocation, attempt session.BrokerAttempt) error {
	before, exists := sess.BrokerAccess()
	if !exists || before.Current == nil || before.Current.Attempt != attempt {
		return errors.New("broker reconciliation does not match current attempt")
	}
	status, err := broker.InspectBrokerAttempt(ctx, attempt)
	if err != nil || status.Attempt != attempt {
		return errors.Join(errors.New("broker attempt inspection failed; invocation remains fenced"), err)
	}
	if status.Phase == "not_admitted" && status.Disposition == "" {
		err = sess.RejectUnadmittedBrokerInvocation(attempt)
	} else {
		if status.Phase == "reserved" || status.Phase == "parked" {
			status, err = broker.AcknowledgeBrokerAttempt(ctx, attempt)
		}
		if err != nil || status.Attempt != attempt || status.Phase != "terminal" || status.Disposition != session.BrokerAttemptNotDispatched {
			return errors.Join(errors.New("broker attempt may have executed; invocation remains fenced"), err)
		}
		err = sess.SettleBrokerInvocation(attempt, session.BrokerAttemptNotDispatched)
	}
	if err != nil {
		return err
	}
	if err := e.saveRequired(ctx, sess); err != nil {
		return errors.Join(err, sess.RestoreBrokerAccess(before))
	}
	return nil
}

func (e *Engine) prepareBrokerAttempt(ctx context.Context, sess *session.Session, call session.ToolCall, broker tool.DurableBrokerInvocation) (context.Context, error) {
	if attempt, ok := tool.BrokerInvocationFromContext(ctx); ok {
		original, err := sess.ContinueBrokerInvocation(call)
		if err != nil || original != attempt {
			return ctx, errors.New("broker continuation does not match original attempt")
		}
		return ctx, nil
	}
	if a, ok := sess.BrokerAccess(); ok && a.Current != nil && a.Current.Phase != "terminal" {
		if err := e.reconcileBrokerAttempt(ctx, sess, broker, a.Current.Attempt); err != nil {
			return ctx, err
		}
	}
	ref, catalogue := broker.BrokerInvocationRefs()
	attempt, err := sess.PrepareBrokerInvocation(ref, catalogue, call, e.now())
	if err == nil {
		err = e.saveRequired(ctx, sess)
	}
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
			// The transcript may be present, but without a verified save it cannot
			// release this occurrence. Restoring clears the local receipt marker.
			return errors.Join(err, sess.RestoreBrokerAccess(before))
		}
		return err
	}
	return nil
}

func (e *Engine) retireBrokerAttempt(ctx context.Context, sess *session.Session, t tool.Tool) error {
	broker, ok := t.(tool.DurableBrokerInvocation)
	if !ok {
		return nil
	}
	a, exists := sess.BrokerAccess()
	if !exists || a.Current == nil || a.Current.Phase != "reserved" {
		return nil
	}
	if attempt, framed := tool.BrokerInvocationFromContext(ctx); framed && attempt != a.Current.Attempt {
		return errors.New("broker cleanup does not match prepared occurrence")
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), authorizationAbortTimeout)
	defer cancel()
	return e.reconcileBrokerAttempt(cleanup, sess, broker, a.Current.Attempt)
}
