package sessionruntime

import (
	"errors"
	"reflect"

	engineid "github.com/stacklok/mecatl/engine/id"
	"github.com/stacklok/mecatl/engine/kernel/resourcegroup"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
)

// ErrInvalidConfig reports invalid Session configuration or factory resource bounds.
var ErrInvalidConfig = errors.New("invalid session runtime factory config")

// FactoryConfig supplies typed dependencies for one SessionRuntime factory.
// Intended construction copies it; shared state captured by Session.Now remains
// caller-owned and must support concurrent use across runtimes.
type FactoryConfig struct {
	// Session supplies passive Session policy, writer identity, and optional clock.
	Session session.Config
	// Policy supplies explicit per-history and resident-instance bounds.
	Policy resourcegroup.FactoryPolicy
}

// Validate checks both policies and the required writer identity without calling
// injected code. A nil Session.Now is allowed, as in session.Config.
func (c FactoryConfig) Validate() error {
	if c.Session.Policy.Validate() != nil || c.Session.InstanceID == 0 || c.Policy.Validate() != nil {
		return ErrInvalidConfig
	}
	return nil
}

// Factory is the handwritten SessionRuntime loader and dependency owner registered
// with a resourcegroup.Manager. Zero is unusable for construction. No constructor
// currently returns a usable Factory; metadata and ID decoding alone are operational.
type Factory struct {
	config FactoryConfig
}

var _ resourcegroup.Loader[*SessionRuntime, sessionid.SessionID] = (*Factory)(nil)

// NewFactory proposes constructing a factory with copied, validated dependencies.
// Currently it always returns nil, ErrNotImplemented, without validation,
// dependency calls, or retention, including for invalid configuration.
func NewFactory(FactoryConfig) (*Factory, error) { return nil, ErrNotImplemented }

// ResourceType returns the exact runtime pointer type, including on nil receivers.
func (*Factory) ResourceType() reflect.Type { return reflect.TypeFor[*SessionRuntime]() }

// Kind returns the Session-owned resource kind, including on nil receivers.
func (*Factory) Kind() engineid.Kind { return "SES" }

// Policy returns a value copy of configured bounds, or zero on nil/zero receivers.
func (f *Factory) Policy() resourcegroup.FactoryPolicy {
	if f == nil {
		return resourcegroup.FactoryPolicy{}
	}
	return f.config.Policy
}

// DecodeID reconstructs an exact Session identity without identity generation,
// membership lookup, or authority checks. Invalid or wrong-kind input returns zero
// and error.
func (*Factory) DecodeID(v engineid.ID) (sessionid.SessionID, error) {
	return sessionid.ParseSessionID(v.String())
}

// Load proposes reconstructing a SessionRuntime within its controlled Scope.
// Currently it returns zero, ErrNotImplemented on every receiver and input,
// without dependency calls, construction, or retention.
func (*Factory) Load(*resourcegroup.Scope, sessionid.SessionID) (resourcegroup.Loaded[*SessionRuntime], error) {
	return resourcegroup.Loaded[*SessionRuntime]{}, ErrNotImplemented
}
