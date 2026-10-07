package tool

import "github.com/stacklok/mecatl/engine/session"

// DurableBrokerInvocation requires a serial, host-owned save-before-dispatch
// uncertainty fence. Permissions and pre-tool hooks still run first.
type DurableBrokerInvocation interface {
	DispatchSerial
	BrokerInvocationRefs() (session.BrokerSessionRef, session.BrokerCatalogueRef)
	BrokerInvocationDisposition(error) session.BrokerAttemptDisposition
}

// BrokerAuthorizationRequired is a verified zero-tool-dispatch response, not an
// execution error. Only trusted broker adapters may construct it.
type BrokerAuthorizationRequired struct {
	Authorization session.ExternalAuthorization
}

func (*BrokerAuthorizationRequired) Error() string {
	return "broker authorization required before tool dispatch"
}
