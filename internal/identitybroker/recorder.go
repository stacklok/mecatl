package identitybroker

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Recorder keeps the broker's records of AuthorizeCall: each ID-JAG it
// obtains and each decision it makes. Records name credentials by reference,
// never by value. An implementation is safe for concurrent use.
type Recorder interface {
	RecordIDJAG(ctx context.Context, rec IDJAGRecord) error
	RecordDecision(ctx context.Context, rec DecisionRecord) error
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

// IDJAGRecord records an ID-JAG the broker obtained for a call.
type IDJAGRecord struct {
	At          time.Time
	SessionID   string
	Agent       AgentRef
	AgentID     string
	RunID       string
	OperationID string
	AttemptID   string
	IDJAG       CredentialRef
	ExpiresAt   time.Time
}

// DecisionRecord records one AuthorizeCall decision.
type DecisionRecord struct {
	At        time.Time
	SessionID string
	Workload  Workload
	// Owner is the broker session's verified owner, or the owner mecak8s
	// reported when the session has none.
	Owner Owner
	// Agent and AgentID are zero when the credential did not verify.
	Agent             AgentRef
	AgentID           string
	RunID             string
	DelegationID      string
	RunOwner          Owner
	OperationID       string
	AttemptID         string
	RejectedAttemptID string
	Method            string
	Server            string
	Tool              string
	ArgumentsSHA256   string
	Outcome           DecisionOutcome
	// Code and Reason are the refusal's; empty on success.
	Code   Code
	Reason Reason
	IDJAG  CredentialRef
	// GatewayToken names the gateway token. GatewayTokenUnreferenced reports
	// an issued token that is not a JWT, so it has no (iss, jti) reference;
	// it does not mean no token was issued.
	GatewayToken             CredentialRef
	GatewayTokenUnreferenced bool
}

// WithRecorder has AuthorizeCall record its decisions with r. Without it, or
// with nil, nothing is recorded.
func WithRecorder(r Recorder) Option {
	return func(b *Broker) { b.recorder = r }
}

// recordDecision records rec, if a recorder is configured. A refusal is
// returned whether or not its record is accepted, so the error is dropped.
func (b *Broker) recordDecision(ctx context.Context, rec DecisionRecord) {
	if b.recorder == nil {
		return
	}
	// Shortcut: a refusal whose record is not accepted is not reported anywhere else; removed by #2541 (the record sink reports delivery failures on a separate channel).
	_ = b.recorder.RecordDecision(ctx, rec)
}

// maxMemoryRecords bounds each of MemoryRecorder's record lists.
const maxMemoryRecords = 10000

// MemoryRecorder is a Recorder holding the most recent records in memory, up
// to maxMemoryRecords of each kind, dropping the oldest. Its methods are safe
// for concurrent use.
//
// Shortcut: records held in memory, dropping the oldest beyond 10,000 of each kind; removed by #2541 (record sink) and #2563 (durable state).
type MemoryRecorder struct {
	mu        sync.Mutex
	idjags    []IDJAGRecord
	decisions []DecisionRecord
}

// NewMemoryRecorder returns an empty MemoryRecorder.
func NewMemoryRecorder() *MemoryRecorder { return &MemoryRecorder{} }

// RecordIDJAG implements Recorder.
func (r *MemoryRecorder) RecordIDJAG(_ context.Context, rec IDJAGRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.idjags = appendBounded(r.idjags, rec)
	return nil
}

// RecordDecision implements Recorder.
func (r *MemoryRecorder) RecordDecision(_ context.Context, rec DecisionRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decisions = appendBounded(r.decisions, rec)
	return nil
}

// IDJAGs returns a copy of the ID-JAG records, oldest first.
func (r *MemoryRecorder) IDJAGs() []IDJAGRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.idjags)
}

// Decisions returns a copy of the decision records, oldest first.
func (r *MemoryRecorder) Decisions() []DecisionRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.decisions)
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
