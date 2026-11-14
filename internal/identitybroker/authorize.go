package identitybroker

import (
	"context"
	"errors"
	"time"
)

// AuthorizeCall implements Core, in the order Service.AuthorizeCall
// documents. Every refusal after the session lookup is recorded.
func (b *Broker) AuthorizeCall(ctx context.Context, workload Workload, req AuthorizeCallRequest) (Authorization, error) {
	if err := checkCall(workload, req); err != nil {
		return Authorization{}, err
	}
	t := b.sessions
	t.mu.Lock()
	s, err := t.callSessionLocked(workload, req.SessionID)
	if s == nil {
		t.mu.Unlock()
		return Authorization{}, err
	}
	rec := decisionRecord(t.now(), workload, s, req)
	// The rejection report is checked before the grant, so a session without
	// one still refuses a report it never issued.
	if err == nil && req.RejectedAttemptID != "" && !t.attempts.knownLocked(s.ID, req.RejectedAttemptID) {
		err = ReasonError(ReasonDenied, "the rejected attempt is unknown or already retried")
	}
	if err == nil {
		_, err = grantForCallLocked(s)
	}
	t.mu.Unlock()
	if err != nil {
		return Authorization{}, b.refuse(ctx, rec, err)
	}

	// Check 3: the credential identifies an agent registered to this open
	// session.
	agent, err := b.VerifyCredential(ctx, VerifyCredentialRequest{SessionID: s.ID, Credential: req.Credential})
	if err != nil {
		return Authorization{}, b.refuse(ctx, rec, err)
	}
	rec.Agent, rec.AgentID = agent.Agent, agent.AgentID

	// Shortcut: step (6) checks 4 and 5 (run and owner) and 7 (ceiling) accept their inputs; removed by #2557 (checks 4 and 5) and the authority work (docs/drafts/agent-identity/README.md, "Order of work") for check 7.

	// Check 6: the gateway routes the server.
	// Shortcut: check 6 matches the call's server, not the tool's route; removed by #2538 (gateway entries carry their routed tools, or the design moves the tool check to the vMCP).
	if !b.gateway.routes(req.Server) {
		return Authorization{}, b.refuse(ctx, rec, ReasonError(ReasonDenied, "no gateway routes the server"))
	}

	// A call that passes every check is still refused: the next change adds
	// design steps (7) and (8), the per-call ID-JAG exchange at AS-1 and its
	// redemption at AS-2 for the gateway token.
	return Authorization{}, b.refuse(ctx, rec, ReasonError(ReasonDenied, "call authorization is not available yet"))
}

// grantForCallLocked returns the grant a call in s uses: the refresh token s
// holds. It fails with ReasonNoGrant when s holds none. It is the one place
// that decides which grant a call uses; scheduled runs (#2569) and the
// chained exchange change only this function. The caller holds t.mu.
func grantForCallLocked(s *brokerSession) (*heldGrant, error) {
	if s.grant == nil {
		return nil, ReasonError(ReasonNoGrant, "the broker session holds no refresh token")
	}
	return s.grant, nil
}

// decisionRecord returns the record of a decision on req in s, before any
// outcome. Its owner is the session's verified owner, or the reported one
// when the session has none. The caller holds t.mu.
func decisionRecord(now time.Time, workload Workload, s *brokerSession, req AuthorizeCallRequest) DecisionRecord {
	owner := s.Owner
	if s.verifiedOwner != nil {
		owner = *s.verifiedOwner
	}
	return DecisionRecord{
		At: now, SessionID: s.ID, Workload: workload, Owner: owner,
		RunID: req.RunID, DelegationID: req.DelegationID, RunOwner: req.RunOwner,
		OperationID: req.OperationID, RejectedAttemptID: req.RejectedAttemptID,
		Method: req.Method, Server: req.Server, Tool: req.Tool, ArgumentsSHA256: req.ArgumentsSHA256,
	}
}

// refuse records err as the refusal of rec's call and returns err. The
// refusal is recorded even when ctx is cancelled, as a cancelled call is
// still a decision.
func (b *Broker) refuse(ctx context.Context, rec DecisionRecord, err error) error {
	rec.Code = CodeInternal
	var e *Error
	if errors.As(err, &e) {
		rec.Code, rec.Reason = e.Code, e.Reason
	}
	rec.Outcome = outcomeFor(rec.Code)
	b.recordDecision(context.WithoutCancel(ctx), rec)
	return err
}
