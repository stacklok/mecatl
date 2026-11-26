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
	// Supersedes is the AuditID of this event's success record, for a
	// refusal recorded after it because the event could not take effect:
	// the sink did not confirm the success, which it may still have kept, or
	// the session closed while the success was recorded. Records are never
	// edited; this one follows the earlier one.
	Supersedes string
}

// The Subjects keys of audit.md's AuditEvent profile.
const (
	SubjectUserIssuer       = "user_iss"
	SubjectUserID           = "user_id"
	SubjectOwnerIssuer      = "owner_iss"
	SubjectOwnerID          = "owner_id"
	SubjectUserClientID     = "user_client_id"
	SubjectUserClientIssuer = "user_client_iss"
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

// ProvenanceFromRecord marks a subject taken from the retained record auditID.
func ProvenanceFromRecord(auditID string) SubjectProvenance {
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
	// The client the user's token was issued to, and the authorization
	// server that issued it.
	s.set(SubjectUserClientID, u.ClientID, p)
	if u.ClientID == "" {
		s.set(SubjectUserClientIssuer, "", p)
	} else {
		s.set(SubjectUserClientIssuer, u.Issuer, p)
	}
}

// addWorkload adds the workload keys. A verified workload is qualified by
// the issuer its authenticator verified; a workload no authenticator
// verified is marked as reported, with no issuer.
func (s Subjects) addWorkload(w Workload) {
	p, issuer := ProvenanceReportedByMecak8s, ""
	if w.Verified {
		p, issuer = ProvenanceVerified, w.Issuer
	}
	s.set(SubjectWorkloadIssuer, issuer, p)
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
		s.addUser(true, *r.User, ProvenanceFromRecord(r.UserRecord), Owner{})
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
	// CloseOpenRefused means the open that created the session was refused,
	// or undone because its record was not accepted, so the session does not
	// stay open. The close refers to the refusal's record, which is queued
	// before it.
	CloseOpenRefused CloseCause = "open_refused"
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
	// SessionRecord is the AuditID of the open that created the session: for
	// CloseOpenRefused, its refusal. That refusal is queued, so the sink may
	// refuse it, or the queue drop it, and the close then names a record the
	// trail does not hold (the #2848 shortcut on recordQueue).
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
	s.addUser(r.UserRecord != "", r.User, ProvenanceFromRecord(r.UserRecord), r.Owner)
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
	// CredentialExpiresAt is when the issued credential expires.
	CredentialExpiresAt time.Time
	// UserRecord is the AuditID of the open that verified the session's
	// user, and User that user; empty when none did.
	UserRecord string
	User       User
	// LineageProvenance marks the instance's place in the tree, which only
	// mecak8s observes: HostSessionID, Incarnation, Definition, Parent,
	// ParentHostSessionID, and the records they lead to. It is always
	// reported by mecak8s (audit.md, "Lineage").
	LineageProvenance SubjectProvenance
}

// Header implements Record.
func (r *RegistrationRecord) Header() RecordHeader { return r.header(TypeInstanceRegistered) }

// Subjects implements Record.
func (r *RegistrationRecord) Subjects() Subjects {
	s := Subjects{}
	s.addUser(r.UserRecord != "", r.User, ProvenanceFromRecord(r.UserRecord), r.Owner)
	s.addWorkload(r.Workload)
	// The broker gives the instance its identity; mecak8s reports which
	// instance (incarnation) it is.
	p := ProvenanceVerified
	if r.PreviousRecord != "" && r.Outcome != OutcomeSuccess {
		p = ProvenanceFromRecord(r.PreviousRecord)
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
	// DelegatingRun and DelegatingAgent delegate; DelegatingAgentID,
	// DelegatingInstance, and DelegatingRecord are the agent's ag_
	// identifier, incarnation, and latest accepted registration, when it is
	// registered to the session.
	DelegatingRun      string
	DelegatingAgent    AgentRef
	DelegatingAgentID  string
	DelegatingInstance string
	DelegatingRecord   string
	// DelegatingRunRecorded reports that, when the report was checked, the
	// broker had not seen the delegating run, so the report records it as a
	// root run of DelegatingAgent. A call of that run racing the report may
	// record the same run first; the run is the same either way.
	DelegatingRunRecorded bool
	// ParentDelegationRecord is the AuditID of the delegation the delegating
	// run itself acts under; empty for a root run.
	ParentDelegationRecord string
	// PreviousRecord is the AuditID of the latest accepted report of the
	// same delegation in an earlier broker session of the root host
	// session, as when mecak8s reports it again after a reopen; empty for a
	// first report.
	PreviousRecord string
	// ReceivingRun, its RunOwner, and ReceivingAgent receive the work;
	// ReceivingAgentID and ReceivingRecord as for the delegating agent.
	ReceivingRun     string
	ReceivingAgent   AgentRef
	ReceivingAgentID string
	ReceivingRecord  string
	RunOwner         Owner
	// UserRecord is the AuditID of the open that verified the session's
	// user, and User that user; empty when none did.
	UserRecord string
	User       User
	// Owner is the broker session's owner, as mecak8s reported it, as in
	// the other records.
	Owner Owner
	// LineageProvenance marks the delegation's facts, its runs, agents, and
	// run owner, which only mecak8s observes. It is always reported by
	// mecak8s (audit.md, "Lineage").
	LineageProvenance SubjectProvenance
}

// Header implements Record.
func (r *DelegationRecord) Header() RecordHeader { return r.header(TypeDelegation) }

// Subjects implements Record. The acting agent is the delegating one, as
// mecak8s reported it.
func (r *DelegationRecord) Subjects() Subjects {
	s := Subjects{}
	s.addUser(r.UserRecord != "", r.User, ProvenanceFromRecord(r.UserRecord), r.Owner)
	s.addWorkload(r.Workload)
	s.addAgent(r.DelegatingAgent, r.DelegatingAgentID, ProvenanceReportedByMecak8s, r.DelegatingInstance, ProvenanceReportedByMecak8s)
	s.addRunOwner(r.RunOwner)
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
	r.Outcome = outcomeFor(r.Code, r.Reason)
}

// supersede returns h as the header of a refusal that follows h's accepted
// success record: a new AuditID, referring to the success.
func (r RecordHeader) supersede() RecordHeader {
	r.Supersedes, r.AuditID = r.AuditID, newAuditID()
	return r
}

// newAuditID returns a new record ID, in the lowercase base32 form of the
// broker's other identifiers.
func newAuditID() string {
	return "rec_" + strings.ToLower(rand.Text())
}

// DeliveryFailure is a record the sink refused or the broker dropped
// unsent: its AuditID and type, never its content, and the error. A record
// still being sent when Close gives up is reported as not confirmed, and its
// send then finishes on its own: the sink may still accept it, or refuse it,
// which reports it a second time with the sink's error.
type DeliveryFailure struct {
	AuditID string
	Type    string
	Err     error
}

// WithDeliveryFailures has the broker report each record its sink refuses to
// report, apart from the records, so a failing sink does not hide its own
// failure (audit.md, "Write order and failure"), and each record it drops
// unsent: past the queue's bound, or at Close. Without it, or with nil,
// refused records are reported nowhere but in the failing operation's
// error, and a refusal or close whose record is refused in none. report
// may be called with the broker's locks held, so it must return promptly
// and must not call the broker.
//
// Shortcut: delivery failures are reported to a callback, which mecak8s logs, not on a separate delivery-failure channel; removed by #2848.
func WithDeliveryFailures(report func(DeliveryFailure)) Option {
	return func(b *Broker) { b.deliveryFailures = report }
}

// recordTimeout bounds each send to the sink, so a slow sink fails a record
// an event waits for with record_not_accepted instead of holding the call,
// and holds a queued close or refusal no longer than this. A sink must honour
// its context's deadline.
const recordTimeout = 10 * time.Second

// record sends rec to the sink, if one is configured, within recordTimeout,
// and returns nil once it is accepted. A refused record is reported as a
// delivery failure, and the caller decides what the refusal means for its
// event.
func (b *Broker) record(ctx context.Context, rec Record) error {
	if b.recorder == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, recordTimeout)
	defer cancel()
	err := b.recorder.Record(ctx, rec)
	if err != nil && b.deliveryFailures != nil {
		h := rec.Header()
		b.deliveryFailures(DeliveryFailure{AuditID: h.AuditID, Type: h.Type, Err: err})
	}
	return err
}

// newHeader returns the header of a new record of type typ.
func (b *Broker) newHeader(typ string) RecordHeader {
	return RecordHeader{AuditID: newAuditID(), Type: typ, At: b.sessions.now()}
}

// invalidRecord is a record of a refused request that failed validation;
// refused is RecordHeader's.
type invalidRecord interface {
	Record
	refused(err error)
}

// refuseInvalid records err as the refusal of a request that failed
// validation and returns err. rec names only the calling workload: the
// request's fields were not validated, so none is recorded (design steps (3)
// and (4) and "Delegation report", "Evidence": every refusal is recorded).
func (b *Broker) refuseInvalid(rec invalidRecord, err error) error {
	rec.refused(err)
	b.recordAnyway(rec)
	return err
}

// dropRecord reports rec, which the broker dropped unsent, as a delivery
// failure with err.
func (b *Broker) dropRecord(rec Record, err error) {
	if b.deliveryFailures != nil {
		h := rec.Header()
		b.deliveryFailures(DeliveryFailure{AuditID: h.AuditID, Type: h.Type, Err: err})
	}
}

// recordAnyway queues rec, a refusal, which takes effect whether or not its
// record is accepted, on the queue closes go through, so neither the caller
// nor a per-key lock it holds waits on the sink. A refused record is only
// reported as a delivery failure.
func (b *Broker) recordAnyway(rec Record) {
	b.sessions.queueRecord(rec)
}

// errNotRecorded is the error of an operation whose record the sink refused.
func errNotRecorded(what string) error {
	return ReasonError(ReasonRecordNotAccepted, "the "+what+" could not be recorded")
}

// errOpenNotRecorded is the error of an open whose record the sink refused.
// An open that converted the request's token spent it, so it fails as AS-1's
// token_used does and the client refreshes the token before opening again
// (credentials-and-lifecycle.md, "Error reasons").
func errOpenNotRecorded(converted bool) error {
	if converted {
		return NewError(CodeUnauthenticated, ReasonRecordNotAccepted, "the session open could not be recorded; refresh the access token and open again")
	}
	return errNotRecorded("session open")
}
