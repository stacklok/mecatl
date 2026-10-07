package tool

import (
	"context"
	"github.com/stacklok/mecatl/engine/session"
)

type brokerInvocationKey struct{}

// WithBrokerInvocation carries the host-assigned attempt after durable preparation.
// It is metadata, not permission to execute or evidence of broker admission.
func WithBrokerInvocation(ctx context.Context, attempt session.BrokerAttempt) context.Context {
	return context.WithValue(ctx, brokerInvocationKey{}, attempt)
}

// BrokerInvocationFromContext returns a framed host attempt, never a model argument.
func BrokerInvocationFromContext(ctx context.Context) (session.BrokerAttempt, bool) {
	attempt, ok := ctx.Value(brokerInvocationKey{}).(session.BrokerAttempt)
	return attempt, ok && attempt.Valid()
}
