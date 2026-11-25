package identitybroker

import (
	"context"
	"fmt"
	"strings"
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
//   - OpenSession with no access token pairs the broker session with the
//     owner the caller reports, and the session holds no refresh token.
//   - FindSession finds the open broker session from its root host session.
//   - A closed broker session is never reopened. It refuses registration and
//     calls with its closing reason, such as ReasonSessionClosed with
//     CodeFailedPrecondition, and a later OpenSession for the same host
//     session opens a new broker session with a new ID.
//   - RegisterAgent refuses a request whose owner differs from the broker
//     session's owner with CodePermissionDenied: one session tree has one
//     owner. It also refuses, with CodePermissionDenied, an agent with no
//     parent whose host session or incarnation is not the broker session's
//     root host session or incarnation, an agent whose parent is not
//     registered to the session or whose reported parent host session is not
//     the parent's recorded host session, and a new agent whose definition
//     reference is not exactly one the broker loaded.
//   - Registering the same incarnation again returns the same agent with a
//     fresh credential, so a lost response or an expiring credential is
//     handled by registering again. Registering it into a new broker session
//     moves it there, keeping its identity. A registration of the same
//     incarnation with a different owner, host session, definition
//     reference, or parent fails with CodeAlreadyExists and changes nothing.
//     An agent registering again is not checked against the definitions
//     loaded now: it keeps the definition it registered with.
//   - A failed registration leaves any earlier registration as it was. A
//     registration that keeps losing a race to other registrations of the
//     same incarnation fails with CodeAborted; registering again succeeds.
//   - Agents with different incarnations are different agents. An agent's
//     subject is its SPIFFE ID, spiffe://<trust domain>/agent/<ag_
//     identifier>, which carries no definition name, incarnation, or
//     ancestry; CheckRegistration checks that form, and a caller holding an
//     earlier registration of the incarnation checks the agent is the same.
//   - A broker session ID the broker never issued is refused with
//     CodePermissionDenied.
//   - Invalid requests fail with CodeInvalidArgument.
//
// An implementation that converts access tokens at session open (design step
// (3); Broker with WithAuthServer) also:
//
//   - Refuses, without calling AS-1, an access token whose sub_id, read
//     without verification, is not the reported owner, with
//     CodePermissionDenied and ReasonOwnerMismatch, or is missing or not of
//     format iss_sub, with CodeUnauthenticated and ReasonSubjectInvalid; and
//     an open of a session whose verified owner is another user, with
//     ReasonSessionOwned. A broker session has two owners: the one the
//     caller reported, which pairs it, and the verified owner, recorded only
//     when AS-1 first accepts a token for it.
//   - Exchanges the token at AS-1 for a refresh token the session holds,
//     never past the session's hard expiry, and reports it in
//     Session.HoldsRefreshToken. A token AS-1 may already have converted,
//     for any session, is never presented to AS-1 again: an open repeating
//     it returns the session as it is when the session holds a live refresh
//     token, and fails with ReasonTokenUsed otherwise. AS-1's token_used and
//     its refusal of the token fail with CodeUnauthenticated and
//     ReasonTokenUsed or ReasonSubjectInvalid, after which the client
//     refreshes its access token and opens again; a user-wide revocation
//     fails with ReasonSignedOut, ReasonUpstreamRevoked, or
//     ReasonOwnerActionRequired and the reason's code, after which the user
//     signs in again; an unavailable AS-1 fails with
//     CodeUnavailable, and any other failure with CodeInternal. A failed
//     exchange leaves the session paired, with any refresh token it held; an
//     answer that does not match the request or cannot be decoded closes it.
//     When the session closes while AS-1 converts the token, the open fails
//     with CodeFailedPrecondition and ReasonSessionClosed and the session
//     keeps no refresh token; the token has been spent, so the client
//     refreshes it before opening again.
//   - Closes a session with ReasonSessionClosed when its refresh token's
//     expiry, the session's hard expiry, passes: from then on every call
//     finds it closed, and the next OpenSession for its host session opens a
//     new broker session.
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
	//
	// The checks run in order, each failing as shown:
	//   - an invalid request: CodeInvalidArgument;
	//   - an unknown session: as for any call;
	//   - a closed session: its closing reason;
	//   - a rejection report for an attempt never issued, or already
	//     reported: CodePermissionDenied, ReasonDenied;
	//   - a session that holds no refresh token: CodeFailedPrecondition,
	//     ReasonNoGrant;
	//   - a credential that does not verify for the session: as
	//     VerifyCredential refuses it;
	//   - a run the broker knows bound to another agent or acting under
	//     another delegation; a new run naming no delegation from an agent
	//     that is not a root instance; a run naming a delegation not reported
	//     for this run and agent; or a run owner that is not the run's owner
	//     and the grant's owner: CodePermissionDenied, ReasonDenied;
	//   - a server no gateway routes: CodePermissionDenied, ReasonDenied;
	//   - a call the acting agent's effective ceiling does not allow (its
	//     definition's ceiling, narrowed by its parent's when it registered):
	//     a tool the ceiling excludes or on a server it does not grant, or a
	//     listing of a server it does not grant: CodePermissionDenied,
	//     ReasonDenied, recorded with the ceiling checked.
	//
	// A call that passes the run checks records a run the broker had not
	// seen: as a root run of the acting agent with the reported owner, or, for
	// a call naming a reported delegation, as that delegation's run.
	//
	// Then the broker obtains a fresh gateway token for this call alone,
	// failing as shown:
	//   - the gateway's authorization server not bound, or an authorization
	//     server or the broker's records unavailable: CodeUnavailable;
	//   - a reported attempt that does not match the call:
	//     CodePermissionDenied, ReasonDenied;
	//   - the session closed during the call: its closing reason;
	//   - AS-1's svid_expired, denied, signed_out, upstream_revoked, or
	//     owner_action_required: that reason; its actor_token_expired:
	//     ReasonSVIDExpired; its approval_required: ReasonDenied;
	//   - a revoked grant, unless the session holds a newer one:
	//     ReasonSessionClosed, closing the session;
	//   - consent the gateway requires: CodeFailedPrecondition,
	//     ReasonConsentRequired;
	//   - any other failure: CodeInternal.
	AuthorizeCall(ctx context.Context, req AuthorizeCallRequest) (Authorization, error)
	// ReportDelegation reports that a run of one agent delegated work to a
	// run of another, so the receiving run may make calls. An accepted report
	// records the delegation and the receiving run, and records the
	// delegating run as a root run when the broker has not seen it.
	//
	// The checks run in order, each failing as shown:
	//   - an invalid request: CodeInvalidArgument;
	//   - an unknown session: as for any call;
	//   - a closed session: its closing reason;
	//   - a delegating run bound to another agent, or itself delegated by a
	//     delegation not reported for it; or, unseen, from an agent that is
	//     not a root instance registered to the session:
	//     CodePermissionDenied, ReasonDenied;
	//   - a receiving agent not registered to the session:
	//     CodePermissionDenied, ReasonDenied;
	//   - an owner that is not the delegating run's owner:
	//     CodePermissionDenied, ReasonDenied;
	//   - a delegation already reported with different content:
	//     CodeAlreadyExists; with the same content the report succeeds;
	//   - a receiving run already recorded: CodePermissionDenied,
	//     ReasonDenied.
	ReportDelegation(ctx context.Context, req ReportDelegationRequest) error
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
	// Owner is the owner the caller reported when it opened the session.
	Owner Owner
	// HoldsRefreshToken reports whether the session held a live refresh token
	// when the call returned: while it does, the caller need not send an
	// access token. The broker itself never presents a token AS-1 may
	// already have converted, for any session, to AS-1 again. The refresh
	// token never leaves the broker.
	HoldsRefreshToken bool
}

// OpenSessionRequest opens the broker session for a root host session.
type OpenSessionRequest struct {
	HostSessionID string
	// Incarnation is the host session's incarnation, which its main agent
	// registers with, so it must be a Mecatl incarnation (see
	// RegisterAgentRequest.Incarnation).
	Incarnation string
	Owner       Owner
	// AccessToken is the user's AS-1 access token, when the request has one.
	// A broker that converts tokens checks that its sub_id is Owner and
	// exchanges it at AS-1 for the session's refresh token. AS-1 converts each
	// token once, so the caller passes a freshly refreshed token, and need not
	// pass one while the session holds a live refresh token; a token AS-1 may
	// already have converted, for any session, is not exchanged again. A
	// token not converted before replaces the session's refresh token. Other
	// brokers ignore it.
	AccessToken Token
}

// Validate reports whether r is well formed. The error is an *Error with
// CodeInvalidArgument.
func (r OpenSessionRequest) Validate() error {
	if err := requireIDs("host session", r.HostSessionID); err != nil {
		return err
	}
	if !isIncarnation(r.Incarnation) {
		return invalidIncarnation(r.Incarnation)
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
// its SPIFFE ID, which the caller checks with CheckRegistration. The zero
// value refers to no agent.
type AgentRef struct {
	Subject string
}

// IsZero reports whether a refers to no agent.
func (a AgentRef) IsZero() bool { return a == AgentRef{} }

// RegisterAgentRequest registers an agent instance in a broker session.
type RegisterAgentRequest struct {
	SessionID string
	// HostSessionID is the agent's own host session. With no Parent it must
	// equal the broker session's root host session. A child agent's is its own
	// child session; ParentHostSessionID ties it to its parent.
	HostSessionID string
	// Owner must equal the broker session's owner.
	Owner Owner
	// Incarnation is the agent instance's incarnation, which the harness
	// allocates and persists: its host session's incarnation. With no Parent
	// it must equal the broker session's incarnation. The broker keys agents
	// by it. It must be a Mecatl incarnation: inc_ and 26 lowercase base32
	// characters, or legacy_ and 64 hex characters.
	Incarnation string
	// Definition is the definition the instance runs. Name is required, and a
	// new instance's reference must be exactly one the broker loaded, so it
	// registers only with Source and Revision set. Source and Revision may
	// still be empty in a request, which the broker then refuses as not
	// loaded, until sessions record their exact reference (#2548).
	Definition DefinitionRef
	// Parent is the registered instance that created this one; zero for a
	// session's main agent.
	Parent AgentRef
	// ParentHostSessionID is the host session the child's session was created
	// under, as the harness records it, or the root host session for a member
	// of a team created over the API, whose session records none and whose
	// parent is the root's main agent. It is required with Parent and must be
	// empty without one. The broker refuses a child whose ParentHostSessionID
	// is not its parent's recorded host session, so each child is tied to its
	// parent and, through it, to the session's tree.
	ParentHostSessionID string
	// ParentStandIn reports that Parent is the root's main agent standing in
	// for the API request that created a team member, not the agent that
	// created it. The broker only records it; it requires a Parent.
	ParentStandIn bool
}

// Validate reports whether r is well formed. The error is an *Error with
// CodeInvalidArgument.
func (r RegisterAgentRequest) Validate() error {
	if err := requireIDs("broker session", r.SessionID, "host session", r.HostSessionID); err != nil {
		return err
	}
	if !isIncarnation(r.Incarnation) {
		return invalidIncarnation(r.Incarnation)
	}
	if err := r.Owner.validate(); err != nil {
		return err
	}
	if err := r.validateDefinition(); err != nil {
		return err
	}
	if r.Parent.IsZero() {
		if r.ParentHostSessionID != "" || r.ParentStandIn {
			return invalid("parent host session and stand-in must be empty without a parent")
		}
		return nil
	}
	if !isToken(r.Parent.Subject) {
		return invalid("parent subject must be non-empty printable text without spaces")
	}
	return requireIDs("parent host session", r.ParentHostSessionID)
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

// VerifyCredentialForOwnerRequest asks whether an agent credential is valid
// for use under a grant of Owner, whichever of the owner's broker sessions
// the agent is registered to. AS-1's actor-token validator sends it, with
// Owner read from the grant's sub_id.
type VerifyCredentialForOwnerRequest struct {
	Owner      Owner
	Credential Token
}

// VerifiedAgent is the agent a valid credential identifies.
type VerifiedAgent struct {
	Agent AgentRef
	// AgentID is the agent's opaque ag_ identifier, the last segment of
	// ActorID. It never contains a SPIFFE path or definition name.
	AgentID string
	// ActorID is the agent's name in an ID-JAG's act.sub:
	// spiffe://<trust domain>/agent/<AgentID>, in the broker's agent trust
	// domain.
	ActorID string
	// ExpiresAt is when the credential expires.
	ExpiresAt time.Time
}

// The MCP methods AuthorizeCallRequest.Method names.
const (
	MethodToolsCall = "tools/call"
	MethodToolsList = "tools/list"
)

// AuthorizeCallRequest asks whether an agent may make one MCP call.
type AuthorizeCallRequest struct {
	SessionID string
	// Credential is the acting agent's credential from RegisterAgent. The
	// broker identifies the acting agent from it.
	Credential Token
	// OperationID identifies this call.
	OperationID string
	// RunID is the run the call belongs to.
	RunID string
	// DelegationID is the delegation the run acts under; empty for a root
	// run.
	DelegationID string
	// RunOwner is the owner the run acts for.
	RunOwner Owner
	// Method is MethodToolsCall or MethodToolsList.
	Method string
	// Server is the gateway backend the call is routed to, such as "github".
	Server string
	// Tool is the exact tool name the call sends as params.name, such as
	// "github.pull_request_read": the server's name, a dot, and the tool's
	// name on the server. Set for tools/call only.
	Tool string
	// ArgumentsSHA256 is the argument hash, 64 lowercase hex characters, as
	// credentials-and-lifecycle.md step (8), "The argument hash", defines it:
	// the SHA-256 of the RFC 8785 (JCS) serialization of params.arguments as
	// sent, or of {} when there are none, not of the raw request bytes. Set
	// for tools/call only.
	ArgumentsSHA256 string
	// RejectedAttemptID, when set, reports that the gateway rejected the token
	// of that attempt, so the broker obtains a fresh one. A caller reports an
	// attempt at most once, with the attempt's own call fields.
	RejectedAttemptID string
}

// Validate reports whether r is well formed. The error is an *Error with
// CodeInvalidArgument.
func (r AuthorizeCallRequest) Validate() error {
	if err := requireIDs("broker session", r.SessionID, "operation", r.OperationID, "run", r.RunID, "server", r.Server); err != nil {
		return err
	}
	if r.Credential.IsZero() {
		return invalid("credential is required")
	}
	if err := r.RunOwner.validate(); err != nil {
		return err
	}
	if r.DelegationID != "" && !isToken(r.DelegationID) {
		return invalid("delegation ID must be printable text without spaces")
	}
	if r.RejectedAttemptID != "" && !isToken(r.RejectedAttemptID) {
		return invalid("rejected attempt ID must be printable text without spaces")
	}
	switch r.Method {
	case MethodToolsCall:
		if !isToken(r.Tool) {
			return invalid("a tools/call must name its tool in printable text without spaces")
		}
		if name, ok := strings.CutPrefix(r.Tool, r.Server+"."); !ok || name == "" {
			return invalid("a tools/call must name its tool as its server's name, a dot, and the tool's name on the server")
		}
		if !isSHA256Hex(r.ArgumentsSHA256) {
			return invalid("a tools/call must carry its arguments' SHA-256 as 64 lowercase hex characters")
		}
	case MethodToolsList:
		if r.Tool != "" || r.ArgumentsSHA256 != "" {
			return invalid("a tools/list carries no tool and no arguments hash")
		}
	default:
		return invalid(fmt.Sprintf("method %q is not supported", r.Method))
	}
	return nil
}

// ReportDelegationRequest reports a delegation from a run of one agent to a
// new run of another, both in one broker session.
type ReportDelegationRequest struct {
	SessionID string
	// DelegationID identifies the delegation within the session. Calls of the
	// receiving run name it in AuthorizeCallRequest.DelegationID.
	DelegationID string
	// DelegatingRun is the run that delegates, and DelegatingAgent the agent
	// it is bound to.
	DelegatingRun   string
	DelegatingAgent AgentRef
	// ReceivingRun is the new run that receives the work, and ReceivingAgent
	// the agent it is bound to.
	ReceivingRun   string
	ReceivingAgent AgentRef
	// Owner is the receiving run's owner. It must equal the delegating run's.
	Owner Owner
	// StandIn reports that DelegatingRun is a run of the root's main agent
	// standing in for the API request that started a team, so the receiving
	// agent is a team member whose stand-in parent is DelegatingAgent. It is
	// part of the delegation's content: a repeat must carry the same value.
	StandIn bool
}

// Validate reports whether r is well formed. The error is an *Error with
// CodeInvalidArgument.
func (r ReportDelegationRequest) Validate() error {
	if err := requireIDs("broker session", r.SessionID, "delegation", r.DelegationID,
		"delegating run", r.DelegatingRun, "receiving run", r.ReceivingRun); err != nil {
		return err
	}
	if !isToken(r.DelegatingAgent.Subject) || !isToken(r.ReceivingAgent.Subject) {
		return invalid("delegating and receiving agents must be non-empty printable text without spaces")
	}
	if r.DelegatingRun == r.ReceivingRun {
		return invalid("the receiving run must differ from the delegating run")
	}
	return r.Owner.validate()
}

// isSHA256Hex reports whether s is 64 lowercase hex characters.
func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Authorization is an allowed MCP call.
type Authorization struct {
	// GatewayToken is the token the call is dispatched with, obtained for
	// this call alone.
	GatewayToken Token
	// ExpiresAt is the broker's use-before deadline for GatewayToken: the
	// earliest of the ID-JAG's exp, AS-2's expires_in, and the token's own
	// exp. The authorization server may keep the token valid longer.
	ExpiresAt time.Time
	// AttemptID identifies this authorization, so a caller whose token the
	// gateway rejects reports it in AuthorizeCallRequest.RejectedAttemptID.
	AttemptID string
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
