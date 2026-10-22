package identitybroker

import (
	"context"
	"fmt"
	"time"
)

// Service is the contract between Mecatl and the identity broker: every call
// Mecatl makes goes through it, whether the broker runs in process or as its
// own service. Requests and results are plain values owned by this package,
// and every error is an *Error carrying a Code and, where the design defines
// one, a Reason.
//
// Behavior every implementation provides:
//
//   - OpenSession for a host session that already has an open broker session
//     returns that session when the incarnation and owner match. A different
//     owner fails with CodePermissionDenied and ReasonSessionOwned; a different
//     incarnation fails with CodeAlreadyExists.
//   - FindSession finds the open broker session from its root host session.
//   - A closed broker session is never reopened. It refuses registration and
//     calls with its closing reason, such as ReasonSessionClosed with
//     CodeFailedPrecondition, and a later OpenSession for the same host
//     session opens a new broker session with a new ID.
//   - RegisterAgent refuses a request whose owner differs from the broker
//     session's owner with CodePermissionDenied: one session tree has one
//     owner. It also refuses, with CodePermissionDenied, an agent with no
//     parent whose host session is not the broker session's root host
//     session, and an agent whose parent is not registered to the session.
//   - Registering the same incarnation again returns the same agent with a
//     fresh credential, so a lost response or an expiring credential is
//     handled by registering again. Registering it into a new broker session
//     moves it there, keeping its identity.
//   - Agents with different incarnations are different agents.
//   - A broker session ID the broker never issued is refused with
//     CodePermissionDenied.
//   - Invalid requests fail with CodeInvalidArgument.
//
// No request names the calling workload: the broker authenticates it itself,
// and Mecatl gets a Service from NewInProcess. Implementations provide this
// behavior by implementing Core.
type Service interface {
	// OpenSession opens the broker session for a root host session, or returns
	// the one already open for it.
	OpenSession(ctx context.Context, req OpenSessionRequest) (Session, error)
	// FindSession returns the open broker session for a root host session, and
	// whether one is open.
	FindSession(ctx context.Context, req FindSessionRequest) (Session, bool, error)
	// CloseSession closes a broker session with a closing reason. Closing an
	// already closed session succeeds and keeps its first reason.
	CloseSession(ctx context.Context, req CloseSessionRequest) error
	// RegisterAgent registers an agent instance in an open broker session and
	// returns its reference and credential.
	RegisterAgent(ctx context.Context, req RegisterAgentRequest) (Registration, error)
	// AuthorizeCall decides whether the agent presenting the credential may
	// make an MCP call, and returns the gateway token when it may. A denial is
	// an error, so a caller that ignores the result cannot dispatch.
	AuthorizeCall(ctx context.Context, req AuthorizeCallRequest) (Authorization, error)
}

// Owner is a session owner: the user's company identity, such as issuer
// "https://login.example.com" and subject "alice-204".
type Owner struct {
	Issuer  string
	Subject string
}

func (o Owner) validate() error {
	if !isToken(o.Issuer) || !isToken(o.Subject) {
		return invalid("owner issuer and subject must be non-empty printable text without spaces")
	}
	return nil
}

// Session is a broker session.
type Session struct {
	// ID is the broker session ID. The broker allocates it and never reuses it.
	ID            string
	HostSessionID string
	Incarnation   string
	Owner         Owner
}

// OpenSessionRequest opens the broker session for a root host session.
type OpenSessionRequest struct {
	HostSessionID string
	// Incarnation is the host session's incarnation.
	Incarnation string
	Owner       Owner
	// AccessToken is the user's AS-1 access token, when the request has one.
	AccessToken Token
}

// Validate reports whether r is well formed. The error is an *Error with
// CodeInvalidArgument.
func (r OpenSessionRequest) Validate() error {
	if err := requireIDs("host session", r.HostSessionID, "incarnation", r.Incarnation); err != nil {
		return err
	}
	return r.Owner.validate()
}

// FindSessionRequest finds the open broker session for a root host session.
type FindSessionRequest struct {
	RootHostSessionID string
}

// Validate reports whether r is well formed. The error is an *Error with
// CodeInvalidArgument.
func (r FindSessionRequest) Validate() error {
	return requireIDs("root host session", r.RootHostSessionID)
}

// CloseSessionRequest closes a broker session.
type CloseSessionRequest struct {
	SessionID string
	// Reason is the closing reason later calls on the session fail with.
	// ReasonSessionClosed is the only closing reason Mecatl supplies today.
	Reason Reason
}

// Validate reports whether r is well formed. The error is an *Error with
// CodeInvalidArgument.
func (r CloseSessionRequest) Validate() error {
	if err := requireIDs("broker session", r.SessionID); err != nil {
		return err
	}
	if r.Reason != ReasonSessionClosed {
		return invalid(fmt.Sprintf("closing reason %q is not supported", r.Reason))
	}
	return nil
}

// AgentRef refers to a registered agent instance by its canonical subject,
// which the caller checks matches the instance it registered. The zero value
// refers to no agent.
type AgentRef struct {
	Subject string
}

// IsZero reports whether a refers to no agent.
func (a AgentRef) IsZero() bool { return a == AgentRef{} }

// RegisterAgentRequest registers an agent instance in a broker session.
type RegisterAgentRequest struct {
	SessionID string
	// HostSessionID is the agent's own host session. With no Parent it must
	// equal the broker session's root host session. A child agent's differs,
	// so it is not compared; checking that it belongs to the session's tree
	// will be addressed later.
	HostSessionID string
	// Owner must equal the broker session's owner.
	Owner Owner
	// Incarnation is the agent instance's incarnation, which the harness
	// allocates and persists. It becomes a segment of the agent's SPIFFE ID,
	// so it must match [A-Za-z0-9._-]+ and be at most MaxIncarnationBytes.
	Incarnation string
	// Definition is the definition the instance runs. Name is required. Source
	// and Revision are checked when set but may be empty for now, because
	// Mecatl sessions know their definition only by name; requiring them will
	// be addressed later.
	Definition DefinitionRef
	// Parent is the registered instance that created this one; zero for a
	// session's main agent.
	Parent AgentRef
}

// Validate reports whether r is well formed. The error is an *Error with
// CodeInvalidArgument.
func (r RegisterAgentRequest) Validate() error {
	if err := requireIDs("broker session", r.SessionID, "host session", r.HostSessionID); err != nil {
		return err
	}
	if !isPathSegment(r.Incarnation, MaxIncarnationBytes) {
		return invalid(fmt.Sprintf("incarnation %q must match [A-Za-z0-9._-]+, be at most %d bytes, and not be '.' or '..'", r.Incarnation, MaxIncarnationBytes))
	}
	if err := r.Owner.validate(); err != nil {
		return err
	}
	if err := r.validateDefinition(); err != nil {
		return err
	}
	if !r.Parent.IsZero() && !isToken(r.Parent.Subject) {
		return invalid("parent subject must be non-empty printable text without spaces")
	}
	return nil
}

// validateDefinition applies DefinitionRef's rules to the fields that are set,
// and requires Name.
func (r RegisterAgentRequest) validateDefinition() error {
	d := r.Definition
	if !isSegment(d.Name) {
		return invalid(fmt.Sprintf("definition name %q must match [A-Za-z0-9._-]+, be at most %d bytes, and not be '.' or '..'", d.Name, MaxSegmentBytes))
	}
	if d.Source != "" && !isSegment(d.Source) {
		return invalid(fmt.Sprintf("definition source %q must match [A-Za-z0-9._-]+, be at most %d bytes, and not be '.' or '..'", d.Source, MaxSegmentBytes))
	}
	if d.Revision != "" && !isToken(d.Revision) {
		return invalid(fmt.Sprintf("definition revision %q must be printable text without spaces", d.Revision))
	}
	return nil
}

// Registration is a registered agent instance and its credential. Mecatl's
// trusted harness code holds the credential and presents it on each call;
// it never reaches the engine or the model.
type Registration struct {
	Agent      AgentRef
	Credential Token
	// ExpiresAt is when the credential expires. Register again before then
	// for a fresh one.
	ExpiresAt time.Time
}

// VerifyCredentialRequest asks whether an agent credential is valid for use
// in a broker session.
type VerifyCredentialRequest struct {
	SessionID  string
	Credential Token
}

// Validate reports whether r is well formed. The error is an *Error with
// CodeInvalidArgument.
func (r VerifyCredentialRequest) Validate() error {
	if err := requireIDs("broker session", r.SessionID); err != nil {
		return err
	}
	if r.Credential.IsZero() {
		return invalid("credential is required")
	}
	return nil
}

// VerifiedAgent is the agent a valid credential identifies.
type VerifiedAgent struct {
	Agent AgentRef
	// AgentID is the agent's opaque ag_ identifier, the value AS-1 names the
	// acting agent by. It never contains a SPIFFE path or definition name.
	AgentID string
	// ExpiresAt is when the credential expires.
	ExpiresAt time.Time
}

// AuthorizeCallRequest asks whether an agent may make one MCP call.
type AuthorizeCallRequest struct {
	SessionID string
	// Credential is the acting agent's credential from RegisterAgent. The
	// broker identifies the acting agent from it.
	Credential Token
	// OperationID identifies this call.
	OperationID string
	// Target is the gateway or backend the call is routed to.
	Target string
	Tool   string
	// GatewayRejectedToken reports that the gateway rejected the token
	// returned for this operation, so the broker obtains a fresh one. A caller
	// sets it on at most one retry.
	GatewayRejectedToken bool
}

// Validate reports whether r is well formed. The error is an *Error with
// CodeInvalidArgument.
func (r AuthorizeCallRequest) Validate() error {
	if err := requireIDs("broker session", r.SessionID, "operation", r.OperationID, "target", r.Target, "tool", r.Tool); err != nil {
		return err
	}
	if r.Credential.IsZero() {
		return invalid("credential is required")
	}
	return nil
}

// Authorization is an allowed MCP call.
type Authorization struct {
	// GatewayToken is the token the call is dispatched with.
	GatewayToken Token
}

// requireIDs checks name/value pairs of identifiers.
func requireIDs(pairs ...string) error {
	for i := 0; i+1 < len(pairs); i += 2 {
		if !isToken(pairs[i+1]) {
			return invalid(pairs[i] + " ID must be non-empty printable text without spaces")
		}
	}
	return nil
}

func invalid(detail string) error {
	return NewError(CodeInvalidArgument, "", detail)
}
