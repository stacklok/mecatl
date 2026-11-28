package identitybroker

import (
	"context"
	"time"
)

// Recorder is the sink the broker's records go through: every open, close,
// registration, delegation report, ID-JAG, and call decision. Record returns
// nil once it accepts rec, and an error when it refuses it. A refusal is
// handled per event: a successful open, registration, or delegation report
// fails with ReasonRecordNotAccepted and changes nothing; an ID-JAG is not
// redeemed and a success decision returns no gateway token, both failing
// with ReasonRecordNotAccepted; refusals and closes take effect anyway. An
// event undone after its record was sent is recorded again as refused,
// superseding that record, since the sink may have kept it without
// confirming it. Every refused
// record is reported through WithDeliveryFailures. Records are
// *SessionOpenRecord, *SessionCloseRecord, *RegistrationRecord,
// *DelegationRecord, *IDJAGRecord, or *DecisionRecord. The broker owns rec
// and may reuse it after the call, so the sink copies what it keeps. An
// implementation is safe for concurrent use.
type Recorder interface {
	Record(ctx context.Context, rec Record) error
}

// CredentialRef names a credential by its issuer and ID (iss, jti), never by
// its value.
type CredentialRef struct {
	Issuer string
	ID     string
}

// DecisionOutcome is how an AuthorizeCall ended.
type DecisionOutcome string

// Outcomes: success when the event took effect, error for a CodeInternal
// or CodeUnavailable failure or a record not accepted, and denied for any
// other refusal (audit.md, "AuditEvent profile"). A refusal that supersedes
// an earlier record therefore has error when that record was not accepted,
// and denied when a close undid its event.
const (
	OutcomeSuccess DecisionOutcome = "success"
	OutcomeDenied  DecisionOutcome = "denied"
	OutcomeError   DecisionOutcome = "error"
)

// outcomeFor returns the outcome of a refusal with code and reason. A record
// not accepted is an error even when a converted open reports it with
// CodeUnauthenticated.
func outcomeFor(code Code, reason Reason) DecisionOutcome {
	if code == CodeInternal || code == CodeUnavailable || reason == ReasonRecordNotAccepted {
		return OutcomeError
	}
	return OutcomeDenied
}

// IDJAGRecord records an ID-JAG the broker obtained for a call, before it is
// redeemed.
type IDJAGRecord struct {
	RecordHeader
	SessionID string
	Workload  Workload
	Agent     AgentRef
	AgentID   string
	RunID     string
	// DelegationID is the delegation the run acts under; empty for a root
	// run.
	DelegationID string
	// RunOwner is the owner the run acts for, as reported.
	RunOwner Owner
	// Owner is the company identity of the grant the ID-JAG was exchanged
	// from, User its verified user (AS-1's subject and client), and
	// UserRecord the AuditID of the open that stored the grant and verified
	// the user, as in the other records.
	Owner      Owner
	UserRecord string
	User       User
	// AgentInstance is the acting agent's incarnation, as mecak8s reported
	// it at registration.
	AgentInstance string
	// RegistrationRecord is the AuditID of the acting agent's latest
	// accepted registration; DelegationRecord that of the delegation the run
	// acts under, empty for a root run.
	RegistrationRecord string
	DelegationRecord   string
	OperationID        string
	AttemptID          string
	IDJAG              CredentialRef
	ExpiresAt          time.Time
}

// Header implements Record.
func (r *IDJAGRecord) Header() RecordHeader { return r.header(TypeIDJAG) }

// Subjects implements Record. The user is the grant's, from the open that
// stored it.
func (r *IDJAGRecord) Subjects() Subjects {
	s := Subjects{}
	s.addUser(r.UserRecord != "", r.User, ProvenanceFromRecord(r.UserRecord), r.Owner)
	s.addWorkload(r.Workload)
	s.addAgent(r.Agent, r.AgentID, ProvenanceVerified, r.AgentInstance, ProvenanceCredentialVerified)
	s.addRunOwner(r.RunOwner)
	return s
}

func (*IDJAGRecord) record() {}

// DecisionRecord records one AuthorizeCall decision.
type DecisionRecord struct {
	RecordHeader
	SessionID string
	Workload  Workload
	// Owner is the broker session's verified owner, or the owner mecak8s
	// reported when the session has none; OwnerVerified says which.
	Owner Owner
	// OwnerVerified is true when Owner is the owner the broker verified at
	// session open, and false when it is only what mecak8s reported
	// (audit.md, "verified" or "reported:<component>").
	OwnerVerified bool
	// UserRecord is the AuditID of the open that verified the session's
	// user, and User that user; empty when none did. The broker sets both
	// with OwnerVerified, and Subjects marks the owner by UserRecord: taken
	// from that open's record.
	UserRecord string
	User       User
	// Agent and AgentID are zero when the credential did not verify.
	Agent   AgentRef
	AgentID string
	// AgentInstance is the acting agent's incarnation, as mecak8s reported
	// it at registration, and RegistrationRecord the AuditID of its latest
	// accepted registration; empty before the run checks.
	AgentInstance      string
	RegistrationRecord string
	RunID              string
	DelegationID       string
	// DelegationRecord is the AuditID of the delegation record of
	// DelegationID; empty for a root run or a refusal before the run checks
	// passed.
	DelegationRecord  string
	RunOwner          Owner
	OperationID       string
	AttemptID         string
	RejectedAttemptID string
	Method            string
	Server            string
	Tool              string
	ArgumentsSHA256   string
	// Ceiling is the acting agent's effective ceiling the call was checked
	// against (check 7), in Ceiling.String's form; empty when the call was
	// refused before that check.
	Ceiling string
	IDJAG   CredentialRef
	// GatewayToken names the gateway token. GatewayTokenUnreferenced reports
	// an issued token that is not a JWT, so it has no (iss, jti) reference;
	// it does not mean no token was issued.
	GatewayToken             CredentialRef
	GatewayTokenUnreferenced bool
	// CallEntryCheck is the result of the output check on the gateway
	// token's response (step (8a)); empty when no token was issued.
	CallEntryCheck CallEntryCheck
}

// Header implements Record.
func (r *DecisionRecord) Header() RecordHeader { return r.header(TypeToolDecision) }

// Subjects implements Record. The agent's identity is verified once its
// credential verified; which instance it is, mecak8s reported.
func (r *DecisionRecord) Subjects() Subjects {
	s := Subjects{}
	s.addUser(r.UserRecord != "", r.User, ProvenanceFromRecord(r.UserRecord), r.Owner)
	s.addWorkload(r.Workload)
	instance := ProvenanceCredentialVerified
	if r.AgentInstance == "" {
		instance = ProvenanceUnavailable
	}
	s.addAgent(r.Agent, r.AgentID, ProvenanceVerified, r.AgentInstance, instance)
	s.addRunOwner(r.RunOwner)
	return s
}

func (*DecisionRecord) record() {}

// WithRecorder has the broker send every record to r. Without it, or with
// nil, nothing is recorded; a gateway requires one.
func WithRecorder(r Recorder) Option {
	return func(b *Broker) { b.recorder = r }
}

// recordDecision queues rec, a refusal, which is returned whether or not its
// record is accepted, so no call waits on the sink to be denied (audit.md,
// "Write order and failure").
func (b *Broker) recordDecision(rec DecisionRecord) {
	rec.AuditID = newAuditID()
	b.recordAnyway(&rec)
}
