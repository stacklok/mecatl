package tool

import (
	"context"

	"github.com/stacklok/mecatl/engine/session"
)

// DurableBrokerInvocation requires a serial, save-before-dispatch invocation
// fence in the owning session. Permissions and pre-tool hooks still run first.
type DurableBrokerInvocation interface {
	DispatchSerial
	BrokerInvocationRefs() (session.BrokerSessionRef, session.BrokerCatalogueRef)
	BrokerInvocationDisposition(error) session.BrokerAttemptDisposition
	BrokerAttemptControl
}

// BrokerAttemptStatus is verified passive evidence, not execution authority.
// Implementations must validate the session and exact attempt before returning it.
type BrokerAttemptStatus struct {
	Attempt     session.BrokerAttempt
	Phase       string
	Disposition session.BrokerAttemptDisposition
}

// BrokerAttemptControl reconciles an allocation without resubmitting its call.
type BrokerAttemptControl interface {
	InspectBrokerAttempt(context.Context, session.BrokerAttempt) (BrokerAttemptStatus, error)
	AcknowledgeBrokerAttempt(context.Context, session.BrokerAttempt) (BrokerAttemptStatus, error)
}
