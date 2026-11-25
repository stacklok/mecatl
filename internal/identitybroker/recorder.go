package identitybroker

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Recorder is the sink the broker's records go through: every open, close,
// registration, delegation report, ID-JAG, and call decision. Record returns
// nil once it accepts rec, and an error when it refuses it; each event handles
// a refusal as audit.md, "Write order and failure", says. Records are
// *SessionOpenRecord, *SessionCloseRecord, *RegistrationRecord,
// *DelegationRecord, *IDJAGRecord, or *DecisionRecord, and the sink must not
// keep rec itself past the call: the broker does not change a record after
// sending it, but it owns it. An implementation is safe for concurrent use.
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

// Decision outcomes: success when the call is allowed, error for a
// CodeInternal or CodeUnavailable failure, and denied for any other refusal.
const (
	OutcomeSuccess DecisionOutcome = "success"
	OutcomeDenied  DecisionOutcome = "denied"
	OutcomeError   DecisionOutcome = "error"
)

// outcomeFor returns the outcome of a refusal with code.
func outcomeFor(code Code) DecisionOutcome {
	if code == CodeInternal || code == CodeUnavailable {
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
	// User is the user of the grant the ID-JAG was exchanged from, and
	// UserRecord the AuditID of the open that stored the grant, which
	// verified the user: AS-1's subject and client, as UserSubjects holds.
	User         Owner
	UserRecord   string
	UserSubjects User
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
	s.addUser(r.UserRecord != "", r.UserSubjects, ProvenanceRecord(r.UserRecord), r.User)
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
}

// Header implements Record.
func (r *DecisionRecord) Header() RecordHeader { return r.header(TypeToolDecision) }

// Subjects implements Record. The agent's identity is verified once its
// credential verified; which instance it is, mecak8s reported.
func (r *DecisionRecord) Subjects() Subjects {
	s := Subjects{}
	s.addUser(r.UserRecord != "", r.User, ProvenanceRecord(r.UserRecord), r.Owner)
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

// recordDecision records rec, a refusal, which is returned whether or not
// its record is accepted.
func (b *Broker) recordDecision(ctx context.Context, rec DecisionRecord) {
	rec.AuditID = newAuditID()
	b.recordAnyway(ctx, &rec)
}

// maxMemoryRecords bounds each kind of MemoryRecorder's records.
const maxMemoryRecords = 10000

// MemoryRecorder is a Recorder holding the most recent records in memory, up
// to maxMemoryRecords of each kind, dropping the oldest. It accepts every
// record. Its methods are safe for concurrent use.
//
// Shortcut: records held in memory, dropping the oldest beyond 10,000 of each kind; removed by #2566 (records written in the AuditEvent profile and delivered) and #2563 (durable state).
type MemoryRecorder struct {
	mu            sync.Mutex
	opens         []SessionOpenRecord
	closes        []SessionCloseRecord
	registrations []RegistrationRecord
	delegations   []DelegationRecord
	idjags        []IDJAGRecord
	decisions     []DecisionRecord
}

// NewMemoryRecorder returns an empty MemoryRecorder.
func NewMemoryRecorder() *MemoryRecorder { return &MemoryRecorder{} }

// Record implements Recorder. It keeps a copy of rec.
func (r *MemoryRecorder) Record(_ context.Context, rec Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch rec := rec.(type) {
	case *SessionOpenRecord:
		c := *rec
		if c.User != nil {
			u := *c.User
			c.User = &u
		}
		if c.Replaced != nil {
			g := *c.Replaced
			c.Replaced = &g
		}
		r.opens = appendBounded(r.opens, c)
	case *SessionCloseRecord:
		r.closes = appendBounded(r.closes, *rec)
	case *RegistrationRecord:
		r.registrations = appendBounded(r.registrations, *rec)
	case *DelegationRecord:
		r.delegations = appendBounded(r.delegations, *rec)
	case *IDJAGRecord:
		r.idjags = appendBounded(r.idjags, *rec)
	case *DecisionRecord:
		r.decisions = appendBounded(r.decisions, *rec)
	}
	return nil
}

// Opens returns a copy of the session open records, oldest first.
func (r *MemoryRecorder) Opens() []SessionOpenRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.opens)
	for i := range out {
		if out[i].User != nil {
			u := *out[i].User
			out[i].User = &u
		}
		if out[i].Replaced != nil {
			g := *out[i].Replaced
			out[i].Replaced = &g
		}
	}
	return out
}

// Closes returns a copy of the session close records, oldest first.
func (r *MemoryRecorder) Closes() []SessionCloseRecord { return cloneLocked(&r.mu, r.closes) }

// Registrations returns a copy of the registration records, oldest first.
func (r *MemoryRecorder) Registrations() []RegistrationRecord {
	return cloneLocked(&r.mu, r.registrations)
}

// Delegations returns a copy of the delegation records, oldest first.
func (r *MemoryRecorder) Delegations() []DelegationRecord { return cloneLocked(&r.mu, r.delegations) }

// IDJAGs returns a copy of the ID-JAG records, oldest first.
func (r *MemoryRecorder) IDJAGs() []IDJAGRecord { return cloneLocked(&r.mu, r.idjags) }

// Decisions returns a copy of the decision records, oldest first.
func (r *MemoryRecorder) Decisions() []DecisionRecord { return cloneLocked(&r.mu, r.decisions) }

// cloneLocked returns a copy of list, taken under mu.
func cloneLocked[T any](mu *sync.Mutex, list []T) []T {
	mu.Lock()
	defer mu.Unlock()
	return slices.Clone(list)
}

// appendBounded appends rec to list, dropping the oldest records past
// maxMemoryRecords.
func appendBounded[T any](list []T, rec T) []T {
	list = append(list, rec)
	if len(list) > maxMemoryRecords {
		list = list[len(list)-maxMemoryRecords:]
	}
	return list
}
