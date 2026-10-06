package tool

import "github.com/stacklok/mecatl/engine/session"

// DurableBrokerInvocation requires a serial, save-before-dispatch invocation
// fence in the owning session. Permissions and pre-tool hooks still run first.
type DurableBrokerInvocation interface {
	DispatchSerial
	BrokerInvocationRefs() (session.BrokerSessionRef, session.BrokerCatalogueRef)
}
