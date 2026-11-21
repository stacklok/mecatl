package identitybroker

import (
	"context"
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
}

// runLedger holds each session's run and delegation records until the
// session closes. Its methods are guarded by sessionTable.mu.
//
// Shortcut: runs and delegations are held in memory, have no end, and are per replica; removed by #2563 (durable state) and #2805 (runs across replicas).
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
	// Check 3.
	delegating, known, err := t.delegatingRunLocked(s, runs, req)
	if err != nil {
		return err
	}
	// Check 4: the receiving agent is registered to the session.
	if agent, ok := t.agents[req.ReceivingAgent.Subject]; !ok || agent.sessionID != s.ID {
		return deniedError("the receiving agent is not registered to the broker session")
	}
	// Check 5: the receiving run keeps the delegating run's owner.
	if req.Owner != delegating.owner {
		return deniedError("the receiving run's owner is not the delegating run's owner")
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
		return deniedError("the receiving run is already recorded")
	}
	if !known {
		runs.addRun(req.DelegatingRun, delegating)
	}
	runs.addDelegation(req.DelegationID, d)
	runs.addRun(req.ReceivingRun, runRecord{agent: req.ReceivingAgent.Subject, owner: req.Owner, delegationID: req.DelegationID})
	return nil
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
