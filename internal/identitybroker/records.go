package identitybroker

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"time"
)

// The broker's records follow docs/drafts/agent-identity/audit.md, "AuditEvent
// profile": each is one event with its own AuditID, a mecatl_ type, an
// outcome, the subjects who acted and for whom with how the broker knows each
// (Subjects), and the event's own facts in its fields. Records name
// credentials by reference, never by value.
//
// Shortcut: records are kept in memory behind the Recorder sink, not written in the AuditEvent profile or delivered; removed by #2566.

// Record types, the AuditEvent Type of each record.
const (
	TypeSessionOpen        = "mecatl_session_open"
	TypeSessionClose       = "mecatl_session_close"
	TypeInstanceRegistered = "mecatl_instance_registered"
	TypeDelegation         = "mecatl_delegation"
	TypeIDJAG              = "mecatl_id_jag"
	TypeToolDecision       = "mecatl_tool_decision"
	mecak8sComponent       = "mecak8s"
)

// Record is one of the broker's records: *SessionOpenRecord,
// *SessionCloseRecord, *RegistrationRecord, *DelegationRecord, *IDJAGRecord,
// or *DecisionRecord.
type Record interface {
	// Header returns what every record carries.
	Header() RecordHeader
	// Subjects returns the record's subjects by audit.md's Subjects keys,
	// each with its provenance. Every key the record's event names is
	// present; a subject the broker does not have is marked unavailable.
	Subjects() Subjects
	record()
}

// RecordHeader is what every record carries.
type RecordHeader struct {
	// AuditID identifies the record: minted once, when the broker makes the
	// record, and never shared with another record.
	AuditID string
	// Type is the record's AuditEvent type, one of the Type constants. The
	// broker sets it; Header returns the record's own type whatever it holds.
	Type string
	// At is when the event happened, on the broker's clock.
	At      time.Time
	Outcome DecisionOutcome
	// Code and Reason are a refusal's; empty on success.
	Code   Code
	Reason Reason
}

// The Subjects keys of audit.md's AuditEvent profile.
const (
	SubjectUserIssuer       = "user_iss"
	SubjectUserID           = "user_id"
	SubjectOwnerIssuer      = "owner_iss"
	SubjectOwnerID          = "owner_id"
	SubjectClientID         = "client_id"
	SubjectClientIssuer     = "client_iss"
	SubjectWorkloadIssuer   = "workload_iss"
	SubjectWorkloadSubject  = "workload_sub"
	SubjectAgentSPIFFEID    = "agent_spiffe_id"
	SubjectAgentActorID     = "agent_actor_id"
	SubjectAgentInstanceRef = "agent_instance_ref"
	SubjectRunOwnerIssuer   = "run_owner_iss"
	SubjectRunOwnerID       = "run_owner_id"
)

// SubjectProvenance says how a record knows a subject, as audit.md's
// mecatl.provenance marks it.
type SubjectProvenance string

const (
	// ProvenanceVerified marks a subject the broker verified, or one AS-1
	// accepted in a token the broker presented to it.
	ProvenanceVerified SubjectProvenance = "verified"
	// ProvenanceCredentialVerified marks an instance whose credential the
	// broker verified, though mecak8s reported which instance it is.
	ProvenanceCredentialVerified SubjectProvenance = "credential-verified" // #nosec G101 -- a provenance mark, not a credential.
	// ProvenanceUnavailable marks a subject the broker does not have.
	ProvenanceUnavailable SubjectProvenance = "unavailable"
	// ProvenanceReportedByMecak8s marks a subject mecak8s reported and the
	// broker did not verify.
	ProvenanceReportedByMecak8s SubjectProvenance = "reported:" + mecak8sComponent
)

// ProvenanceRecord marks a subject taken from the retained record auditID.
func ProvenanceRecord(auditID string) SubjectProvenance {
	return SubjectProvenance("record:" + auditID)
}

// Subject is a record's subject and how the record knows it.
type Subject struct {
	Value      string
	Provenance SubjectProvenance
}

// Subjects are a record's subjects by Subjects key.
type Subjects map[string]Subject

// set adds key with value and provenance, or as unavailable when value is
// empty, so a missing fact is never left out.
func (s Subjects) set(key, value string, p SubjectProvenance) {
	if value == "" {
		s[key] = Subject{Provenance: ProvenanceUnavailable}
		return
	}
	s[key] = Subject{Value: value, Provenance: p}
}

// User is the user a broker session's refresh token was granted for, as
// AS-1 accepted their access token: the company identity from its sub_id,
// AS-1's own issuer and subject, and the client the token was issued to. The
// broker reads them from the token without verifying it, and keeps them only
// once AS-1 accepts the token. A claim the token lacks is empty.
type User struct {
	Owner    Owner
	Issuer   string
	Subject  string
	ClientID string
}

// addUser adds the user keys: u's with provenance p when known is true, and
// otherwise only owner, the reported owner, marked as reported by mecak8s,
// with the user's own keys unavailable. An identity the broker did not
// verify never appears as user_id.
func (s Subjects) addUser(known bool, u User, p SubjectProvenance, owner Owner) {
	if !known {
		u, p = User{Owner: owner}, ProvenanceReportedByMecak8s
	}
	s.set(SubjectOwnerIssuer, u.Owner.Issuer, p)
	s.set(SubjectOwnerID, u.Owner.Subject, p)
	s.set(SubjectUserIssuer, u.Issuer, p)
	s.set(SubjectUserID, u.Subject, p)
	s.set(SubjectClientID, u.ClientID, p)
	if u.ClientID == "" {
		// client_id is qualified by its authorization server.
		s.set(SubjectClientIssuer, "", p)
	} else {
		s.set(SubjectClientIssuer, u.Issuer, p)
	}
}

// addWorkload adds the workload keys. Workloads have no issuer the broker
// knows; a workload no authenticator verified is marked as reported.
func (s Subjects) addWorkload(w Workload) {
	p := ProvenanceReportedByMecak8s
	if w.Verified {
		p = ProvenanceVerified
	}
	s.set(SubjectWorkloadIssuer, "", p)
	s.set(SubjectWorkloadSubject, w.Name, p)
}

// addAgent adds the agent keys: its SPIFFE ID and ag_ identifier with
// provenance p, and its instance reference, the incarnation, with
// instanceProvenance.
func (s Subjects) addAgent(agent AgentRef, agentID string, p SubjectProvenance, incarnation string, instanceProvenance SubjectProvenance) {
	s.set(SubjectAgentSPIFFEID, agent.Subject, p)
	s.set(SubjectAgentActorID, agentID, p)
	s.set(SubjectAgentInstanceRef, incarnation, instanceProvenance)
}

// addRunOwner adds the run owner keys, always as mecak8s reported them.
func (s Subjects) addRunOwner(owner Owner) {
	s.set(SubjectRunOwnerIssuer, owner.Issuer, ProvenanceReportedByMecak8s)
	s.set(SubjectRunOwnerID, owner.Subject, ProvenanceReportedByMecak8s)
}

// sessionUser returns s's verified user and the AuditID of the open record
// that verified them; an empty ID when none did.
func sessionUser(s *brokerSession) (string, User) {
	if s.user == nil {
		return "", User{}
	}
	return s.user.openRecord, s.user.User
}

// OpenKind is how a successful session open treated the request's access
// token.
type OpenKind string

const (
	// OpenConverted means AS-1 converted the access token into the session's new
	// refresh token.
	OpenConverted OpenKind = "converted"
	// OpenAlreadyConverted means AS-1 may already have converted the token, and
	// the session, holding a live refresh token, was returned as it was.
	OpenAlreadyConverted OpenKind = "already_converted"
	// OpenTokenless means the request carried no access token, or the broker
	// converts none, so the session was only paired with the reported owner.
	OpenTokenless OpenKind = "tokenless"
)

// ReplacedGrant refers to a refresh token an open replaced, never by value:
// by the open record that stored it and that open's source token jti.
type ReplacedGrant struct {
	OpenRecord string
	SourceJTI  string
}

// SessionOpenRecord records an OpenSession call: a successful open, or a
// refused one with its reason (credentials-and-lifecycle.md step (3),
// "Evidence").
type SessionOpenRecord struct {
	RecordHeader
	Workload      Workload
	HostSessionID string
	Incarnation   string
	// ReportedOwner is the owner mecak8s reported.
	ReportedOwner Owner
	// SessionID is the broker session, when the open reached one.
	SessionID string
	// NewSession reports that this open created the broker session.
	NewSession bool
	// SessionRecord is the AuditID of the open that created the session;
	// empty when this open did.
	SessionRecord string
	// Kind is how a successful open treated the access token; empty for a
	// refusal.
	Kind OpenKind
	// TokenPresented reports that the request carried an access token.
	TokenPresented bool
	// SourceJTI is the access token's jti, read without verification; empty
	// when the token had none or could not be read.
	SourceJTI string
	// User is the session's user: the one AS-1 accepted, for OpenConverted,
	// or else the one an earlier open verified, whose AuditID is UserRecord.
	// Nil when no open verified one.
	User       *User
	UserRecord string
	// HardExpiry is the session's hard expiry AS-1 granted, for
	// OpenConverted.
	HardExpiry time.Time
	// Replaced is the refresh token a converting open replaced; nil when the
	// session held none.
	Replaced *ReplacedGrant
}

// Header implements Record.
func (r *SessionOpenRecord) Header() RecordHeader { return r.header(TypeSessionOpen) }

// Subjects implements Record.
func (r *SessionOpenRecord) Subjects() Subjects {
	s := Subjects{}
	switch {
	case r.User != nil && r.UserRecord == "":
		s.addUser(true, *r.User, ProvenanceVerified, Owner{})
	case r.User != nil:
		s.addUser(true, *r.User, ProvenanceRecord(r.UserRecord), Owner{})
	default:
		s.addUser(false, User{}, "", r.ReportedOwner)
	}
	s.addWorkload(r.Workload)
	return s
}

func (*SessionOpenRecord) record() {}

// CloseCause is why a broker session closed.
type CloseCause string

const (
	// CloseRequested means mecak8s closed the session.
	CloseRequested CloseCause = "requested"
	// CloseHardExpiry means a lookup found the session past its hard expiry.
	CloseHardExpiry CloseCause = "hard_expiry"
	// CloseMismatchedAnswer means AS-1's answer at session open did not match the
	// request or could not be decoded.
	CloseMismatchedAnswer CloseCause = "mismatched_answer"
	// CloseGrantLost means AS-1 reported the session's refresh token revoked and
	// the session held no newer one.
	CloseGrantLost CloseCause = "grant_lost"
	// CloseOpenNotRecorded means the open that created the session could not be
	// recorded, so the session does not stay open.
	CloseOpenNotRecorded CloseCause = "open_not_recorded"
)

// SessionCloseRecord records a broker session closing, once per session,
// from every close path (credentials-and-lifecycle.md step (3), "Session
// end").
type SessionCloseRecord struct {
	RecordHeader
	SessionID     string
	HostSessionID string
	// Workload is the workload the session belongs to.
	Workload Workload
	Owner    Owner
	// ClosingReason is the reason later calls on the session fail with.
	ClosingReason Reason
	Cause         CloseCause
	// SessionRecord is the AuditID of the open that created the session.
	SessionRecord string
	// UserRecord is the AuditID of the open that verified the session's
	// user, and User that user; empty when none did.
	UserRecord string
	User       User
}

// Header implements Record.
func (r *SessionCloseRecord) Header() RecordHeader { return r.header(TypeSessionClose) }

// Subjects implements Record.
func (r *SessionCloseRecord) Subjects() Subjects {
	s := Subjects{}
	s.addUser(r.UserRecord != "", r.User, ProvenanceRecord(r.UserRecord), r.Owner)
	s.addWorkload(r.Workload)
	return s
}

func (*SessionCloseRecord) record() {}

// RegistrationRecord records a RegisterAgent call as a registration lineage
// event, or a refused registration with its reason (audit.md, "Lineage";
// credentials-and-lifecycle.md step (4), "Evidence"). Everything about the
// instance's place in the tree is reported by mecak8s; the agent's identity is
// the broker's own.
type RegistrationRecord struct {
	RecordHeader
	Workload  Workload
	SessionID string
	// SessionRecord is the AuditID of the open that created the session.
	SessionRecord string
	HostSessionID string
	Owner         Owner
	Incarnation   string
	Definition    DefinitionRef
	// Agent and AgentID are the instance's SPIFFE ID and ag_ identifier;
	// zero for a refusal before the broker knew the instance.
	Agent   AgentRef
	AgentID string
	// New reports that the instance registered for the first time; Moved
	// that it moved from another broker session.
	New, Moved bool
	// PreviousRecord is the AuditID of the instance's previous accepted
	// registration; empty for a new instance.
	PreviousRecord string
	// Parent is the instance's creation parent; zero for a main agent.
	Parent              AgentRef
	ParentHostSessionID string
	// ParentRecord is the AuditID of the parent's latest accepted
	// registration.
	ParentRecord string
	// ParentStandIn reports, as mecak8s did, that Parent is the root's main
	// agent standing in for the API request that created a team member, not
	// the instance that created it.
	ParentStandIn bool
	// CredentialExpiresAt is when the issued credential expires.
	CredentialExpiresAt time.Time
	// UserRecord is the AuditID of the open that verified the session's
	// user, and User that user; empty when none did.
	UserRecord string
	User       User
}

// Header implements Record.
func (r *RegistrationRecord) Header() RecordHeader { return r.header(TypeInstanceRegistered) }

// Subjects implements Record.
func (r *RegistrationRecord) Subjects() Subjects {
	s := Subjects{}
	s.addUser(r.UserRecord != "", r.User, ProvenanceRecord(r.UserRecord), r.Owner)
	s.addWorkload(r.Workload)
	// The broker gives the instance its identity; mecak8s reports which
	// instance (incarnation) it is.
	p := ProvenanceVerified
	if r.PreviousRecord != "" && r.Outcome != OutcomeSuccess {
		p = ProvenanceRecord(r.PreviousRecord)
	}
	s.addAgent(r.Agent, r.AgentID, p, r.Incarnation, ProvenanceReportedByMecak8s)
	return s
}

func (*RegistrationRecord) record() {}

// DelegationRecord records a ReportDelegation call as a delegation lineage
// event, or a refused report with its reason (audit.md, "Lineage";
// credentials-and-lifecycle.md, "Delegation report"). Every fact in it is
// reported by mecak8s; an accepted report was checked for consistency. An
// identical repeat of an accepted report makes no second record.
type DelegationRecord struct {
	RecordHeader
	Workload     Workload
	SessionID    string
	DelegationID string
	// DelegatingRun and DelegatingAgent delegate; DelegatingAgentID and
	// DelegatingRecord are the agent's ag_ identifier and latest accepted
	// registration, when it is registered to the session.
	DelegatingRun     string
	DelegatingAgent   AgentRef
	DelegatingAgentID string
	DelegatingRecord  string
	// DelegatingRunRecorded reports that this report recorded the delegating
	// run, which the broker had not seen, as a root run of DelegatingAgent.
	DelegatingRunRecorded bool
	// ParentDelegationRecord is the AuditID of the delegation the delegating
	// run itself acts under; empty for a root run.
	ParentDelegationRecord string
	// StandIn reports, as mecak8s did, that DelegatingRun is a run of the
	// root's main agent standing in for the API request that started a
	// team, not a delegation the agent made.
	StandIn bool
	// ReceivingRun, its Owner, and ReceivingAgent receive the work;
	// ReceivingAgentID and ReceivingRecord as for the delegating agent.
	ReceivingRun     string
	ReceivingAgent   AgentRef
	ReceivingAgentID string
	ReceivingRecord  string
	Owner            Owner
	// UserRecord is the AuditID of the open that verified the session's
	// user, and User that user; empty when none did.
	UserRecord string
	User       User
	// SessionOwner is the broker session's owner, as mecak8s reported it.
	SessionOwner Owner
}

// Header implements Record.
func (r *DelegationRecord) Header() RecordHeader { return r.header(TypeDelegation) }

// Subjects implements Record. The acting agent is the delegating one, as
// mecak8s reported it.
func (r *DelegationRecord) Subjects() Subjects {
	s := Subjects{}
	s.addUser(r.UserRecord != "", r.User, ProvenanceRecord(r.UserRecord), r.SessionOwner)
	s.addWorkload(r.Workload)
	s.addAgent(r.DelegatingAgent, r.DelegatingAgentID, ProvenanceReportedByMecak8s, "", ProvenanceUnavailable)
	s.addRunOwner(r.Owner)
	return s
}

func (*DelegationRecord) record() {}

// header returns r's header with type typ.
func (r RecordHeader) header(typ string) RecordHeader {
	r.Type = typ
	return r
}

// refused sets r's refusal from err: its code and reason, and the outcome
// they make.
func (r *RecordHeader) refused(err error) {
	r.Code, r.Reason = CodeInternal, ""
	var e *Error
	if errors.As(err, &e) {
		r.Code, r.Reason = e.Code, e.Reason
	}
	r.Outcome = outcomeFor(r.Code)
}

// newAuditID returns a new record ID, in the lowercase base32 form of the
// broker's other identifiers.
func newAuditID() string {
	return "rec_" + strings.ToLower(rand.Text())
}

// DeliveryFailure is a record the sink refused: its AuditID and type, never
// its content, and the sink's error.
type DeliveryFailure struct {
	AuditID string
	Type    string
	Err     error
}

// WithDeliveryFailures has the broker report each record its sink refuses to
// report, apart from the records, so a failing sink does not hide its own
// failure (audit.md, "Write order and failure"). Without it, or with nil,
// refused records are reported nowhere but in the failing operation's
// error, and a refusal or close whose record is refused in none.
func WithDeliveryFailures(report func(DeliveryFailure)) Option {
	return func(b *Broker) { b.deliveryFailures = report }
}

// record sends rec to the sink, if one is configured, and returns nil once
// it is accepted. A refused record is reported as a delivery failure, and
// the caller decides what the refusal means for its event.
func (b *Broker) record(ctx context.Context, rec Record) error {
	if b.recorder == nil {
		return nil
	}
	err := b.recorder.Record(ctx, rec)
	if err != nil && b.deliveryFailures != nil {
		h := rec.Header()
		b.deliveryFailures(DeliveryFailure{AuditID: h.AuditID, Type: h.Type, Err: err})
	}
	return err
}

// recordAnyway records rec, a refusal or a close, which takes effect whether
// or not its record is accepted; a refused record is only reported as a
// delivery failure. It is recorded even when ctx is cancelled, as the event
// happened anyway.
func (b *Broker) recordAnyway(ctx context.Context, rec Record) {
	_ = b.record(context.WithoutCancel(ctx), rec)
}

// errNotRecorded is the error of an operation whose record the sink refused.
func errNotRecorded(what string) error {
	return NewError(CodeUnavailable, "", "the "+what+" could not be recorded")
}
