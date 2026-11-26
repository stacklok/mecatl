package identitybroker

import (
	"context"
	"errors"
	"slices"
)

// maxRunsPerSession bounds how many run records, and how many delegation
// records, a session keeps; the oldest is forgotten first. A forgotten root
// run is recorded again on its next call or report. A forgotten delegated run
// is rebuilt from its delegation while that is kept; once both are
// forgotten, the run's calls are refused.
//
// Shortcut: the design keeps run and delegation records for the session's life; these in-memory records are capped instead; removed by #2563 (durable records with a retention rule).
const maxRunsPerSession = 4096

// runRecord is a run the broker knows (design step (6), check 4): the agent
// it is bound to, its owner, and the delegation it acts under, empty for a
// root run.
type runRecord struct {
	agent        string // the agent's subject
	owner        Owner
	delegationID string
}

// delegationRecord is a reported delegation: a run of one agent handing work
// to a run of another, both in one session.
type delegationRecord struct {
	delegatingRun, delegatingAgent string
	receivingRun, receivingAgent   string
	owner                          Owner
	// record is the AuditID of the report's record; not part of the
	// delegation's content.
	record string
}

// sameContent reports whether d and o report the same delegation.
func (d delegationRecord) sameContent(o delegationRecord) bool {
	d.record, o.record = "", ""
	return d == o
}

// runLedger holds each session's run and delegation records until the
// session closes. Its methods are guarded by sessionTable.mu.
//
// Shortcut: runs and delegations are held in memory, have no end, and are per replica; removed by #2563 (durable state) and #2805 (runs across replicas).
type runLedger struct {
	bySession map[string]*sessionRuns
	// previous holds, by root host session, each delegation's latest
	// accepted report. It outlives the broker session, so a delegation
	// reported again into a reopened broker session refers to the record of
	// its earlier report. It is forgotten when mecak8s closes the host
	// session, as when it is deleted, or closes its current broker session
	// itself, after which nothing reports the delegations again.
	previous map[string]*delegationRecords
}

// delegationRecords is a root host session's latest accepted report per
// delegation ID, keeping at most maxRunsPerSession, all from broker sessions
// of one workload.
type delegationRecords struct {
	workload Workload
	byID     map[string]acceptedReport
	order    []string
}

// acceptedReport is an accepted report's broker session and record.
type acceptedReport struct {
	sessionID, auditID string
}

type sessionRuns struct {
	runs            map[string]runRecord
	runOrder        []string
	delegations     map[string]delegationRecord
	delegationOrder []string
}

func newRunLedger() *runLedger {
	return &runLedger{bySession: map[string]*sessionRuns{}, previous: map[string]*delegationRecords{}}
}

// previousLocked returns the AuditID of delegation id's latest accepted
// report in an earlier broker session of host than sessionID; empty when
// there is none.
func (m *runLedger) previousLocked(host, sessionID, id string) string {
	if p, ok := m.previous[host]; ok {
		if r := p.byID[id]; r.sessionID != sessionID {
			return r.auditID
		}
	}
	return ""
}

// forgetPreviousLocked forgets host's accepted reports in workload's broker
// sessions.
func (m *runLedger) forgetPreviousLocked(workload Workload, host string) {
	if p, ok := m.previous[host]; ok && p.workload == workload {
		delete(m.previous, host)
	}
}

// notePreviousLocked keeps auditID, in workload's broker session sessionID,
// as delegation id's latest accepted report in host's broker sessions. The
// reports of another workload's earlier broker sessions of host are
// forgotten.
func (m *runLedger) notePreviousLocked(workload Workload, host, sessionID, id, auditID string) {
	p, ok := m.previous[host]
	if !ok || p.workload != workload {
		p = &delegationRecords{workload: workload, byID: map[string]acceptedReport{}}
		m.previous[host] = p
	}
	if _, ok := p.byID[id]; !ok {
		if len(p.order) == maxRunsPerSession {
			delete(p.byID, p.order[0])
			p.order = slices.Delete(p.order, 0, 1)
		}
		p.order = append(p.order, id)
	}
	p.byID[id] = acceptedReport{sessionID: sessionID, auditID: auditID}
}

// forLocked returns sessionID's records, creating them if needed.
func (m *runLedger) forLocked(sessionID string) *sessionRuns {
	s, ok := m.bySession[sessionID]
	if !ok {
		s = &sessionRuns{runs: map[string]runRecord{}, delegations: map[string]delegationRecord{}}
		m.bySession[sessionID] = s
	}
	return s
}

// dropLocked forgets sessionID's records.
func (m *runLedger) dropLocked(sessionID string) {
	delete(m.bySession, sessionID)
}

func (s *sessionRuns) addRun(id string, r runRecord) {
	if len(s.runOrder) == maxRunsPerSession {
		delete(s.runs, s.runOrder[0])
		s.runOrder = slices.Delete(s.runOrder, 0, 1)
	}
	s.runOrder = append(s.runOrder, id)
	s.runs[id] = r
}

func (s *sessionRuns) addDelegation(id string, d delegationRecord) {
	if len(s.delegationOrder) == maxRunsPerSession {
		delete(s.delegations, s.delegationOrder[0])
		s.delegationOrder = slices.Delete(s.delegationOrder, 0, 1)
	}
	s.delegationOrder = append(s.delegationOrder, id)
	s.delegations[id] = d
}

// delegationTo returns the kept delegation whose receiving run is run, bound
// to agent, and its ID.
func (s *sessionRuns) delegationTo(run, agent string) (string, delegationRecord, bool) {
	for id, d := range s.delegations {
		if d.receivingRun == run && d.receivingAgent == agent {
			return id, d, true
		}
	}
	return "", delegationRecord{}, false
}

// ReportDelegation implements Core, with the checks of the design's
// "Delegation report" in order. An accepted report records the delegation and
// the receiving run, bound to the receiving agent and its owner, and records
// a delegating root run the broker has not seen yet.
//
// Every report but an identical repeat of an accepted one is recorded as a
// delegation lineage event (audit.md, "Lineage"). Reports to one broker
// session run one at a time, so the check, the record, and the store act on
// the same state. An accepted report is stored only once its record is
// accepted; otherwise it fails with ReasonRecordNotAccepted and stores
// nothing, and mecak8s does not report it again, so the helper does not run.
// A refused report is returned whether or not its record is accepted. A
// report undone after its success record was sent, because the record was
// not accepted or the session closed meanwhile, is recorded again as
// refused, superseding that record.
func (b *Broker) ReportDelegation(ctx context.Context, workload Workload, req ReportDelegationRequest) error {
	if err := checkCall(workload, req); err != nil {
		return b.refuseInvalid(&DelegationRecord{RecordHeader: b.newHeader(TypeDelegation), Workload: workload}, err)
	}
	t := b.sessions
	rec := DelegationRecord{
		RecordHeader: RecordHeader{AuditID: newAuditID(), Type: TypeDelegation, At: t.now()},
		Workload:     workload, SessionID: req.SessionID, DelegationID: req.DelegationID,
		DelegatingRun: req.DelegatingRun, DelegatingAgent: req.DelegatingAgent,
		ReceivingRun: req.ReceivingRun, ReceivingAgent: req.ReceivingAgent, RunOwner: req.Owner,
		LineageProvenance: ProvenanceReportedByMecak8s,
	}
	unlock, err := t.reporting.lock(ctx, req.SessionID, "the delegation report was cancelled")
	if err != nil {
		return b.refuseDelegation(rec, err)
	}
	defer unlock()
	t.mu.Lock()
	_, err = t.checkDelegationLocked(workload, req, &rec)
	t.mu.Unlock()
	if errors.Is(err, errRepeatedDelegation) {
		return nil
	}
	if err != nil {
		return b.refuseDelegation(rec, err)
	}
	rec.Outcome = OutcomeSuccess
	if err := b.record(ctx, &rec); err != nil {
		// The sink may have kept the record without confirming it, so the
		// refusal names it.
		rec.RecordHeader = rec.supersede()
		return b.refuseDelegation(rec, errNotRecorded("delegation report"))
	}
	t.mu.Lock()
	plan, err := t.checkDelegationLocked(workload, req, nil)
	if err == nil {
		plan.storeLocked(req, rec.AuditID)
	}
	t.mu.Unlock()
	if err != nil && !errors.Is(err, errRepeatedDelegation) {
		rec.RecordHeader = rec.supersede()
		return b.refuseDelegation(rec, err)
	}
	return nil
}

// errRepeatedDelegation is checkDelegationLocked's answer to an identical
// repeat of an accepted report, which succeeds with nothing new to record or
// store.
var errRepeatedDelegation = errors.New("delegation already reported with the same content")

// delegationPlan is what an accepted report stores.
type delegationPlan struct {
	ledger     *runLedger
	workload   Workload
	host       string
	sessionID  string
	runs       *sessionRuns
	delegating runRecord
	known      bool
	d          delegationRecord
}

// storeLocked stores p's delegation, whose record is auditID, its receiving
// run, and its delegating run when the broker had not seen it. The caller
// holds t.mu.
func (p delegationPlan) storeLocked(req ReportDelegationRequest, auditID string) {
	if !p.known {
		p.runs.addRun(req.DelegatingRun, p.delegating)
	}
	p.d.record = auditID
	p.runs.addDelegation(req.DelegationID, p.d)
	p.ledger.notePreviousLocked(p.workload, p.host, p.sessionID, req.DelegationID, auditID)
	p.runs.addRun(req.ReceivingRun, runRecord{agent: req.ReceivingAgent.Subject, owner: req.Owner, delegationID: req.DelegationID})
}

// refuseDelegation records err as the refusal of rec's report and returns
// err.
func (b *Broker) refuseDelegation(rec DelegationRecord, err error) error {
	rec.refused(err)
	rec.DelegatingRunRecorded = false
	b.recordAnyway(&rec)
	return err
}

// checkDelegationLocked runs the report's checks and returns what an
// accepted report stores, or errRepeatedDelegation for an identical repeat.
// It fills rec, when not nil, with what the broker knows of the session, the
// agents, and the runs. The caller holds t.mu.
func (t *sessionTable) checkDelegationLocked(workload Workload, req ReportDelegationRequest, rec *DelegationRecord) (delegationPlan, error) {
	if rec != nil {
		t.describeDelegationLocked(workload, req, rec)
	}
	// Checks 1 and 2: the workload, and its open session.
	s, err := t.usableLocked(workload, req.SessionID)
	if err != nil {
		return delegationPlan{}, err
	}
	runs := t.runs.forLocked(s.ID)
	// Check 3.
	delegating, known, err := t.delegatingRunLocked(s, runs, req)
	if err != nil {
		return delegationPlan{}, err
	}
	if rec != nil {
		rec.DelegatingRunRecorded = !known && delegating.delegationID == ""
		if delegating.delegationID != "" {
			rec.ParentDelegationRecord = runs.delegations[delegating.delegationID].record
		}
	}
	// Check 4: the receiving agent is registered to the session.
	if agent, ok := t.agents[req.ReceivingAgent.Subject]; !ok || agent.sessionID != s.ID {
		return delegationPlan{}, deniedError("the receiving agent is not registered to the broker session")
	}
	// Check 5: the receiving run keeps the delegating run's owner.
	if req.Owner != delegating.owner {
		return delegationPlan{}, deniedError("the receiving run's owner is not the delegating run's owner")
	}
	// Check 6: a delegation is reported once; a repeat must match.
	d := delegationRecord{
		delegatingRun: req.DelegatingRun, delegatingAgent: req.DelegatingAgent.Subject,
		receivingRun: req.ReceivingRun, receivingAgent: req.ReceivingAgent.Subject, owner: req.Owner,
	}
	if prev, ok := runs.delegations[req.DelegationID]; ok {
		if !prev.sameContent(d) {
			return delegationPlan{}, NewError(CodeAlreadyExists, "", "the delegation was reported with different content")
		}
		return delegationPlan{}, errRepeatedDelegation
	}
	if _, ok := runs.runs[req.ReceivingRun]; ok {
		return delegationPlan{}, deniedError("the receiving run is already recorded")
	}
	return delegationPlan{ledger: t.runs, workload: workload, host: s.HostSessionID, sessionID: s.ID, runs: runs, delegating: delegating, known: known, d: d}, nil
}

// describeDelegationLocked fills rec with what the broker knows of req's
// session and agents, whether or not the report passes. The caller holds
// t.mu.
func (t *sessionTable) describeDelegationLocked(workload Workload, req ReportDelegationRequest, rec *DelegationRecord) {
	s, ok := t.byID[req.SessionID]
	if !ok || s.workload != workload {
		return
	}
	rec.Owner = s.Owner
	rec.UserRecord, rec.User = sessionUser(s)
	rec.PreviousRecord = t.runs.previousLocked(s.HostSessionID, s.ID, req.DelegationID)
	if a, ok := t.agents[req.DelegatingAgent.Subject]; ok && a.sessionID == s.ID {
		rec.DelegatingAgentID, rec.DelegatingInstance, rec.DelegatingRecord = a.agentID, a.incarnation, a.registrationRecord
	}
	if a, ok := t.agents[req.ReceivingAgent.Subject]; ok && a.sessionID == s.ID {
		rec.ReceivingAgentID, rec.ReceivingRecord = a.agentID, a.registrationRecord
	}
}

// delegatingRunLocked is "Delegation report" check 3: the delegating run
// belongs to s and is bound to the delegating agent. One the broker has not
// seen is a delegated run whose record was forgotten, rebuilt from its kept
// delegation as AuthorizeCall rebuilds it, or else a root run, which needs a
// root instance registered to s and s's owner. It returns the delegating
// run's record and whether runs holds it. The caller holds t.mu.
func (t *sessionTable) delegatingRunLocked(s Session, runs *sessionRuns, req ReportDelegationRequest) (runRecord, bool, error) {
	delegating, known := runs.runs[req.DelegatingRun]
	switch {
	case known && delegating.agent != req.DelegatingAgent.Subject:
		return runRecord{}, true, deniedError("the delegating run is bound to another agent")
	case known && delegating.delegationID != "":
		if d, ok := runs.delegations[delegating.delegationID]; !ok || d.receivingRun != req.DelegatingRun {
			return runRecord{}, true, deniedError("the delegating run's own delegation was not reported for it")
		}
	case known:
	default:
		if id, d, ok := runs.delegationTo(req.DelegatingRun, req.DelegatingAgent.Subject); ok {
			return runRecord{agent: d.receivingAgent, owner: d.owner, delegationID: id}, false, nil
		}
		agent, ok := t.agents[req.DelegatingAgent.Subject]
		if !ok || agent.sessionID != s.ID || !agent.parent.IsZero() {
			return runRecord{}, false, deniedError("a delegating run the broker has not seen must be a root run of a root instance registered to the session")
		}
		// The owner a root run is recorded with is checked here, not only at
		// its first call, so one inconsistent report cannot fix the wrong
		// owner on it.
		if req.Owner != s.Owner {
			return runRecord{}, false, deniedError("a delegating root run's owner must be the broker session's owner")
		}
		delegating = runRecord{agent: req.DelegatingAgent.Subject, owner: req.Owner}
	}
	return delegating, known, nil
}

// checkRunLocked applies design step (6) checks 4 and 5 to a call in s by the
// agent with subject agent: the run belongs to s and is bound to that agent,
// and its reported owner is the run's and the grant's owner. A run the broker
// has not seen is recorded only once both checks pass, so a refused first
// call leaves nothing behind. The caller holds t.mu.
func (t *sessionTable) checkRunLocked(s *brokerSession, agent string, req AuthorizeCallRequest) error {
	grant, err := grantForCallLocked(s)
	if err != nil {
		return err
	}
	runs := t.runs.forLocked(s.ID)
	rec, known := runs.runs[req.RunID]
	switch {
	case known:
		if rec.agent != agent {
			return deniedError("the run is bound to another agent")
		}
		if rec.delegationID != req.DelegationID {
			return deniedError("the run acts under another delegation")
		}
	case req.DelegationID == "":
		if a, ok := t.agents[agent]; !ok || !a.parent.IsZero() {
			return deniedError("a new run naming no delegation needs a root instance")
		}
		rec = runRecord{agent: agent, owner: req.RunOwner}
	default:
		// A delegation report records its receiving run, so this is reached
		// only once that record was forgotten.
		d, ok := runs.delegations[req.DelegationID]
		if !ok || d.receivingRun != req.RunID || d.receivingAgent != agent {
			return deniedError("the run's delegation was not reported for this run and agent")
		}
		rec = runRecord{agent: agent, owner: d.owner, delegationID: req.DelegationID}
	}
	if err := checkRunOwner(req.RunOwner, rec.owner, grant.owner); err != nil {
		return err
	}
	if !known {
		runs.addRun(req.RunID, rec)
	}
	return nil
}

// checkRunOwner is design step (6) check 5: the reported run owner is the
// run's owner and the owner recorded with the grant in use, the company
// identity. It only refuses: the run owner never selects the grant. It is
// one function so a later membership check, for sessions with several
// members, replaces only its body (docs/drafts/agent-identity/run-owner.md,
// item 2).
func checkRunOwner(reported, run, grant Owner) error {
	if reported != run || reported != grant {
		return deniedError("the run's owner is not the owner of the grant in use")
	}
	return nil
}
