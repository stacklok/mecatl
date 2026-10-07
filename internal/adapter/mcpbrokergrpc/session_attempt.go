package mcpbrokergrpc

import (
	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

func attemptFromWire(a *p.Attempt) c.BrokerAttempt {
	return c.BrokerAttempt{ID: a.GetId()}
}
func attemptToWire(a c.BrokerAttempt) *p.Attempt {
	return &p.Attempt{Id: a.ID}
}
func wireAttemptValid(a *p.Attempt) bool { return cleanSessionWire(a) && attemptFromWire(a).Valid() }
