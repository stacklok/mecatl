package identitybroker

import (
	"context"
	"maps"
	"slices"
)

// The MCP client that sends a gateway call reports each attempt's dispatch
// and observed outcome, and the broker records them as separate facts,
// marked as reported by mecak8s and joined to the decision that issued the
// attempt (audit.md, "Decision, dispatch, and observed outcome"). A report
// the broker cannot join is recorded as a mismatch, never dropped. Reports
// are never waited for: no later record references them by AuditID
// (audit.md, "Write order and failure").

// ReportMismatch names why a call report could not be joined to the
// decision that issued its attempt, or was refused.
type ReportMismatch string

const (
	// MismatchAttemptUnknown means the broker did not issue the attempt in the
	// reported session, or the session is unknown to the caller, or the
	// broker has forgotten the attempt.
	MismatchAttemptUnknown ReportMismatch = "attempt_unknown"
	// MismatchOperation means the attempt was issued for another operation.
	MismatchOperation ReportMismatch = "operation_mismatch"
	// MismatchConflictingReport means the attempt was already reported with
	// a different dispatch, outcome, or cause.
	MismatchConflictingReport ReportMismatch = "conflicting_report"
	// MismatchUnexpectedLateResult means a late result for an attempt whose
	// outcome was not reported unknown, or a second, different late result.
	MismatchUnexpectedLateResult ReportMismatch = "unexpected_late_result"
	// MismatchOutcomeNotInSet means the report's dispatch or outcome is
	// outside its fixed set; the report was refused.
	MismatchOutcomeNotInSet ReportMismatch = "outcome_not_in_set"
	// MismatchInvalidReport means the report was refused as invalid for
	// another reason.
	MismatchInvalidReport ReportMismatch = "invalid_report"
)

// CallReport is what the broker records of one call report.
type CallReport struct {
	Workload    Workload
	SessionID   string
	OperationID string
	AttemptID   string
	// DecisionRecord is the AuditID of the decision that issued the attempt
	// for this operation, and DecisionSubjects that decision's subjects;
	// empty when the report could not be joined to it (Mismatch is
	// MismatchAttemptUnknown or MismatchOperation).
	DecisionRecord   string
	DecisionSubjects Subjects
	// Owner is the broker session's owner, as mecak8s reported it, for a
	// report not joined to a decision; zero when the session is unknown.
	Owner Owner
	// Mismatch is empty for a report joined to its decision as the first
	// report of its attempt.
	Mismatch ReportMismatch
	// Cause is the client's fixed code for why the attempt was not sent or
	// was refused; empty when it gave none.
	Cause string
	// ReportProvenance marks the reported facts, the dispatch, outcome, and
	// cause. It is always reported by mecak8s.
	ReportProvenance SubjectProvenance
}

// subjects returns the report's subjects: the reporting workload, verified;
// and the decision's other subjects, as taken from the decision's record,
// or, without one, the session owner as reported and the rest unavailable.
func (r *CallReport) subjects() Subjects {
	s := Subjects{}
	if r.DecisionRecord != "" {
		from := ProvenanceFromRecord(r.DecisionRecord)
		for k, v := range r.DecisionSubjects {
			if v.Provenance != ProvenanceUnavailable {
				v.Provenance = from
			}
			s[k] = v
		}
	} else {
		s.addUser(false, User{}, "", r.Owner)
		s.addAgent(AgentRef{}, "", "", "", "")
		s.addRunOwner(Owner{})
	}
	s.addWorkload(r.Workload)
	return s
}

func (r CallReport) clone() CallReport {
	r.DecisionSubjects = maps.Clone(r.DecisionSubjects)
	return r
}

// DispatchRecord records a reported dispatch: Outcome is success when the
// tools/call was or may have been written, and failure when it never was.
type DispatchRecord struct {
	RecordHeader
	CallReport
}

// Header implements Record.
func (r *DispatchRecord) Header() RecordHeader { return r.header(TypeToolDispatch) }

// Subjects implements Record.
func (r *DispatchRecord) Subjects() Subjects { return r.subjects() }

func (*DispatchRecord) record() {}

func (r *DispatchRecord) clone() DispatchRecord {
	c := *r
	c.CallReport = r.CallReport.clone()
	return c
}

// OutcomeRecord records a reported observed outcome of a dispatched
// attempt: success, failure, error, or unknown when it may have run but no
// completion was observed.
type OutcomeRecord struct {
	RecordHeader
	CallReport
	// LateResult marks a result observed after the attempt's outcome was
	// reported unknown; Follows is the AuditID of that unknown outcome's
	// record (audit.md, "Correlation").
	LateResult bool
	Follows    string
}

// Header implements Record.
func (r *OutcomeRecord) Header() RecordHeader { return r.header(TypeToolOutcome) }

// Subjects implements Record.
func (r *OutcomeRecord) Subjects() Subjects { return r.subjects() }

func (*OutcomeRecord) record() {}

func (r *OutcomeRecord) clone() OutcomeRecord {
	c := *r
	c.CallReport = r.CallReport.clone()
	return c
}

// ReportRejectedRecord records a call report refused as invalid, with
// Outcome denied and the refusal's code. It names the calling workload, the
// mismatch, and the request's session, operation, and attempt only when
// they are well formed; never the refused values themselves.
type ReportRejectedRecord struct {
	RecordHeader
	CallReport
}

// Header implements Record.
func (r *ReportRejectedRecord) Header() RecordHeader { return r.header(TypeToolReportRejected) }

// Subjects implements Record.
func (r *ReportRejectedRecord) Subjects() Subjects { return r.subjects() }

func (*ReportRejectedRecord) record() {}

// reportedAttempt is an attempt the broker issued for a tools/call, and what
// has been reported of it.
type reportedAttempt struct {
	operationID string
	decision    string
	subjects    Subjects
	// reported is the first report joined to the attempt; zero until then.
	reported ReportCallRequest
	// outcomeRecord is the AuditID of the record of the reported outcome.
	outcomeRecord string
	// late is the late result recorded after an unknown outcome; zero until
	// then.
	late ReportCallRequest
}

// reportLedger remembers, per session, the attempts the broker issued for
// tools/calls and what has been reported of each, at most
// maxAttemptsPerSession of them; the oldest is forgotten first, and a report
// for it is recorded as MismatchAttemptUnknown. A session's attempts are kept
// after it closes, so a report that arrives late is still joined. Its
// methods are guarded by sessionTable.mu.
//
// Shortcut: reported attempts held in memory, forgetting a session's oldest beyond 1024; removed by #2563 (durable state).
type reportLedger struct {
	bySession map[string]*sessionReports
}

type sessionReports struct {
	order    []string // attempt IDs, oldest first
	attempts map[string]*reportedAttempt
}

func newReportLedger() *reportLedger {
	return &reportLedger{bySession: map[string]*sessionReports{}}
}

// issueLocked remembers attemptID, issued in sessionID for operationID by
// the decision recorded as decision with subjects.
func (m *reportLedger) issueLocked(sessionID, attemptID, operationID, decision string, subjects Subjects) {
	s, ok := m.bySession[sessionID]
	if !ok {
		s = &sessionReports{attempts: map[string]*reportedAttempt{}}
		m.bySession[sessionID] = s
	}
	if _, ok := s.attempts[attemptID]; ok {
		return
	}
	if len(s.order) == maxAttemptsPerSession {
		delete(s.attempts, s.order[0])
		s.order = slices.Delete(s.order, 0, 1)
	}
	s.order = append(s.order, attemptID)
	s.attempts[attemptID] = &reportedAttempt{operationID: operationID, decision: decision, subjects: subjects}
}

func (m *reportLedger) lookupLocked(sessionID, attemptID string) *reportedAttempt {
	if s, ok := m.bySession[sessionID]; ok {
		return s.attempts[attemptID]
	}
	return nil
}

// ReportCall implements Core, as Service.ReportCall documents. Every report
// is recorded, unless it repeats one already recorded; none is waited for.
func (b *Broker) ReportCall(_ context.Context, workload Workload, req ReportCallRequest) error {
	if err := checkCall(workload, req); err != nil {
		return b.refuseReport(workload, req, err)
	}
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	report := CallReport{
		Workload: workload, SessionID: req.SessionID, OperationID: req.OperationID, AttemptID: req.AttemptID,
		Cause: req.Cause, ReportProvenance: ProvenanceReportedByMecak8s,
	}
	// A closed session still takes reports: its calls may end after it closed.
	s, _ := t.callSessionLocked(workload, req.SessionID)
	var a *reportedAttempt
	if s != nil {
		report.Owner = s.Owner
		a = t.reports.lookupLocked(s.ID, req.AttemptID)
	}
	switch {
	case a == nil:
		report.Mismatch = MismatchAttemptUnknown
	case a.operationID != req.OperationID:
		report.Mismatch = MismatchOperation
	default:
		report.DecisionRecord, report.DecisionSubjects = a.decision, a.subjects
		var repeat bool
		if report.Mismatch, repeat = a.match(req); repeat {
			return nil
		}
	}
	t.queueRecordLocked(b.reportRecords(report, req, a)...)
	return nil
}

// match returns the mismatch of req against what was reported of a, and
// whether req repeats a report already recorded, which records nothing.
func (a *reportedAttempt) match(req ReportCallRequest) (ReportMismatch, bool) {
	switch {
	case !req.LateResult && a.reported == (ReportCallRequest{}):
		return "", false
	case !req.LateResult:
		return MismatchConflictingReport, a.reported == req
	case a.late != (ReportCallRequest{}):
		return MismatchUnexpectedLateResult, a.late == req
	case a.reported.Outcome != OutcomeUnknown:
		return MismatchUnexpectedLateResult, false
	}
	return "", false
}

// reportRecords returns the records of req, reported of attempt a (nil when
// unknown), and remembers a report joined without a mismatch: a dispatch
// record, then, for a dispatched attempt, its outcome record. A late result
// is an outcome record alone, following the unknown outcome's record.
func (b *Broker) reportRecords(report CallReport, req ReportCallRequest, a *reportedAttempt) []Record {
	now := b.sessions.now()
	outcome := &OutcomeRecord{
		RecordHeader: RecordHeader{AuditID: newAuditID(), Type: TypeToolOutcome, At: now, Outcome: req.Outcome},
		CallReport:   report, LateResult: req.LateResult,
	}
	if req.LateResult {
		if a != nil && report.Mismatch == "" {
			outcome.Follows = a.outcomeRecord
			a.late = req
		}
		return []Record{outcome}
	}
	if a != nil && report.Mismatch == "" {
		a.reported = req
		if req.Dispatch == OutcomeSuccess {
			a.outcomeRecord = outcome.AuditID
		}
	}
	dispatch := &DispatchRecord{
		RecordHeader: RecordHeader{AuditID: newAuditID(), Type: TypeToolDispatch, At: now, Outcome: req.Dispatch},
		CallReport:   report,
	}
	if req.Dispatch != OutcomeSuccess {
		return []Record{dispatch}
	}
	return []Record{dispatch, outcome}
}

// refuseReport queues the record of req, refused with err as invalid, and
// returns err. It names req's session, operation, and attempt only when they
// are well formed, and never its dispatch, outcome, or cause.
func (b *Broker) refuseReport(workload Workload, req ReportCallRequest, err error) error {
	rec := &ReportRejectedRecord{
		RecordHeader: b.newHeader(TypeToolReportRejected),
		CallReport:   CallReport{Workload: workload, Mismatch: MismatchInvalidReport, ReportProvenance: ProvenanceReportedByMecak8s},
	}
	if requireIDs("broker session", req.SessionID, "operation", req.OperationID, "attempt", req.AttemptID) == nil {
		rec.SessionID, rec.OperationID, rec.AttemptID = req.SessionID, req.OperationID, req.AttemptID
	}
	if !req.inSets() {
		rec.Mismatch = MismatchOutcomeNotInSet
	}
	rec.refused(err)
	b.recordAnyway(rec)
	return err
}
