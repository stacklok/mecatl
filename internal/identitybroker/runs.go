package identitybroker

import (
	"context"
	"slices"
)

// maxRunsPerSession bounds how many run records, and how many delegation
// records, a session keeps; the oldest is forgotten first. A forgotten root
// run is recorded again on its next call; a forgotten delegated run's later
// calls are refused until its delegation is reported again.
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
}

// runLedger holds each session's run and delegation records until the
// session closes. Its methods are guarded by sessionTable.mu.
//
// Shortcut: runs and delegations are held in memory, have no end, and are per replica; removed by #2563 (durable state) and phase 3 (runs across replicas).
type runLedger struct {
	bySession map[string]*sessionRuns
}

type sessionRuns struct {
	runs            map[string]runRecord
	runOrder        []string
	delegations     map[string]delegationRecord
	delegationOrder []string
}

func newRunLedger() *runLedger {
	return &runLedger{bySession: map[string]*sessionRuns{}}
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

func runDenied(detail string) error {
	return ReasonError(ReasonDenied, detail)
}

// ReportDelegation implements Core, with the checks of the design's
// "Delegation report" in order. An accepted report records the delegation and
// the receiving run, bound to the receiving agent and its owner, and records
// a delegating root run the broker has not seen yet.
//
// Shortcut: accepted and refused reports are not recorded as lineage events; removed by #2541.
func (b *Broker) ReportDelegation(_ context.Context, workload Workload, req ReportDelegationRequest) error {
	if err := checkCall(workload, req); err != nil {
		return err
	}
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	// Checks 1 and 2: the workload, and its open session.
	s, err := t.usableLocked(workload, req.SessionID)
	if err != nil {
		return err
	}
	runs := t.runs.forLocked(s.ID)
	// Check 3: the delegating run belongs to the session and is bound to the
	// delegating agent; one the broker has not seen is recorded as a root
	// run, which needs a root instance registered to the session.
	delegating, known := runs.runs[req.DelegatingRun]
	switch {
	case known && delegating.agent != req.DelegatingAgent.Subject:
		return runDenied("the delegating run is bound to another agent")
	case known && delegating.delegationID != "":
		if d, ok := runs.delegations[delegating.delegationID]; !ok || d.receivingRun != req.DelegatingRun {
			return runDenied("the delegating run's own delegation was not reported for it")
		}
	case !known:
		agent, ok := t.agents[req.DelegatingAgent.Subject]
		if !ok || agent.sessionID != s.ID || !agent.parent.IsZero() {
			return runDenied("a delegating run the broker has not seen must be a root run of a root instance registered to the session")
		}
		delegating = runRecord{agent: req.DelegatingAgent.Subject, owner: req.Owner}
	}
	// Check 4: the receiving agent is registered to the session.
	if agent, ok := t.agents[req.ReceivingAgent.Subject]; !ok || agent.sessionID != s.ID {
		return runDenied("the receiving agent is not registered to the broker session")
	}
	// Check 5: the receiving run keeps the delegating run's owner.
	if req.Owner != delegating.owner {
		return runDenied("the receiving run's owner is not the delegating run's owner")
	}
	// Check 6: a delegation is reported once; a repeat must match.
	d := delegationRecord{
		delegatingRun: req.DelegatingRun, delegatingAgent: req.DelegatingAgent.Subject,
		receivingRun: req.ReceivingRun, receivingAgent: req.ReceivingAgent.Subject, owner: req.Owner,
	}
	if prev, ok := runs.delegations[req.DelegationID]; ok {
		if prev != d {
			return NewError(CodeAlreadyExists, "", "the delegation was reported with different content")
		}
		return nil
	}
	if _, ok := runs.runs[req.ReceivingRun]; ok {
		return runDenied("the receiving run is already recorded")
	}
	if !known {
		runs.addRun(req.DelegatingRun, delegating)
	}
	runs.addDelegation(req.DelegationID, d)
	runs.addRun(req.ReceivingRun, runRecord{agent: req.ReceivingAgent.Subject, owner: req.Owner, delegationID: req.DelegationID})
	return nil
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
			return runDenied("the run is bound to another agent")
		}
		if rec.delegationID != req.DelegationID {
			return runDenied("the run acts under another delegation")
		}
	case req.DelegationID == "":
		if a, ok := t.agents[agent]; !ok || !a.parent.IsZero() {
			return runDenied("a new run naming no delegation needs a root instance")
		}
		rec = runRecord{agent: agent, owner: req.RunOwner}
	default:
		// A delegation report records its receiving run, so this is reached
		// only once that record was forgotten.
		d, ok := runs.delegations[req.DelegationID]
		if !ok || d.receivingRun != req.RunID || d.receivingAgent != agent {
			return runDenied("the run's delegation was not reported for this run and agent")
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
// members, replaces only its body (run-owner.md, item 2).
func checkRunOwner(reported, run, grant Owner) error {
	if reported != run || reported != grant {
		return runDenied("the run's owner is not the owner of the grant in use")
	}
	return nil
}
