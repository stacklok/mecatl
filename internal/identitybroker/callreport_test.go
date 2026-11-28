package identitybroker_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

const (
	success = identitybroker.OutcomeSuccess
	failure = identitybroker.OutcomeFailure
	unknown = identitybroker.OutcomeUnknown
)

// reportFixture is a per-call fixture that reads its records once the queue
// has sent them.
type reportFixture struct{ perCallFixture }

func newReportFixture(t *testing.T) reportFixture { return reportFixture{newPerCall(t)} }

func (f reportFixture) report(req identitybroker.ReportCallRequest) error {
	return f.b.ReportCall(context.Background(), testWorkload, req)
}

func (f reportFixture) records() ([]identitybroker.DispatchRecord, []identitybroker.OutcomeRecord, []identitybroker.ReportRejectedRecord) {
	identitybroker.WaitRecords(f.b)
	return f.rec.Dispatches(), f.rec.Outcomes(), f.rec.RejectedReports()
}

// authorized authorizes a tools/call of operation op and returns its attempt
// and the decision that issued it.
func (f reportFixture) authorized(t *testing.T, op string) (string, identitybroker.DecisionRecord) {
	t.Helper()
	req := callRequest(f.s.ID, f.cred)
	req.OperationID = op
	auth, err := f.authorize(req)
	if err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	for _, d := range f.rec.Decisions() {
		if d.AttemptID == auth.AttemptID && d.Outcome == success {
			return auth.AttemptID, d
		}
	}
	t.Fatalf("no allowing decision for attempt %s", auth.AttemptID)
	return "", identitybroker.DecisionRecord{}
}

func callReport(session, op, attempt string, dispatch, outcome identitybroker.DecisionOutcome) identitybroker.ReportCallRequest {
	return identitybroker.ReportCallRequest{SessionID: session, OperationID: op, AttemptID: attempt, Dispatch: dispatch, Outcome: outcome}
}

// wantJoined fails t unless r is reported of attempt in op and joined to
// decision with mismatch.
func wantJoined(t *testing.T, r identitybroker.CallReport, op, attempt string, decision identitybroker.DecisionRecord, mismatch identitybroker.ReportMismatch) {
	t.Helper()
	if r.OperationID != op || r.AttemptID != attempt || r.DecisionRecord != decision.AuditID || r.Mismatch != mismatch ||
		r.ReportProvenance != identitybroker.ProvenanceReportedByMecak8s {
		t.Fatalf("report = %+v, want %s/%s joined to %s with mismatch %q, reported by mecak8s", r, op, attempt, decision.AuditID, mismatch)
	}
}

func TestReportCallJoinsTheReportToItsDecision(t *testing.T) {
	f := newReportFixture(t)
	attempt, decision := f.authorized(t, "op-1")
	// The decision names V by its jti.
	if decision.GatewayToken.ID == "" || decision.GatewayToken.Issuer != as2Issuer {
		t.Fatalf("decision names gateway token %+v, want its (iss, jti)", decision.GatewayToken)
	}
	req := callReport(f.s.ID, "op-1", attempt, success, success)
	if err := f.report(req); err != nil {
		t.Fatalf("ReportCall: %v", err)
	}
	dispatches, outcomes, _ := f.records()
	if len(dispatches) != 1 || len(outcomes) != 1 {
		t.Fatalf("recorded %d dispatches and %d outcomes, want 1 each", len(dispatches), len(outcomes))
	}
	d, o := dispatches[0], outcomes[0]
	wantJoined(t, d.CallReport, "op-1", attempt, decision, "")
	wantJoined(t, o.CallReport, "op-1", attempt, decision, "")
	if d.Header().Type != identitybroker.TypeToolDispatch || d.Outcome != success ||
		o.Header().Type != identitybroker.TypeToolOutcome || o.Outcome != success || o.LateResult || o.Follows != "" {
		t.Fatalf("dispatch %+v and outcome %+v, want success for each", d, o)
	}
	if d.AuditID == o.AuditID || d.AuditID == decision.AuditID {
		t.Fatal("a report record shares an AuditID")
	}
	// The acting agent and user are the decision's, taken from its record;
	// the reporting workload is the caller.
	subjects := o.Subjects()
	from := identitybroker.ProvenanceFromRecord(decision.AuditID)
	if got := subjects[identitybroker.SubjectAgentActorID]; got.Value != decision.AgentID || got.Provenance != from {
		t.Fatalf("agent_actor_id = %+v, want %s from the decision's record", got, decision.AgentID)
	}
	if got := subjects[identitybroker.SubjectRunOwnerID]; got.Value != testOwner.Subject || got.Provenance != from {
		t.Fatalf("run_owner_id = %+v, want %s from the decision's record", got, testOwner.Subject)
	}
	if got := subjects[identitybroker.SubjectWorkloadSubject]; got.Value != testWorkload.Name {
		t.Fatalf("workload_sub = %+v, want the reporting workload", got)
	}
	// The same report again records nothing more.
	if err := f.report(req); err != nil {
		t.Fatalf("ReportCall again: %v", err)
	}
	if dispatches, outcomes, _ := f.records(); len(dispatches) != 1 || len(outcomes) != 1 {
		t.Fatalf("a repeated report recorded again: %d dispatches, %d outcomes", len(dispatches), len(outcomes))
	}
}

func TestReportCallDispatchFailureHasNoOutcome(t *testing.T) {
	f := newReportFixture(t)
	attempt, decision := f.authorized(t, "op-1")
	req := callReport(f.s.ID, "op-1", attempt, failure, "")
	req.Cause = "authorization_expired"
	if err := f.report(req); err != nil {
		t.Fatalf("ReportCall: %v", err)
	}
	dispatches, outcomes, _ := f.records()
	if len(dispatches) != 1 || len(outcomes) != 0 {
		t.Fatalf("recorded %d dispatches and %d outcomes, want one dispatch", len(dispatches), len(outcomes))
	}
	wantJoined(t, dispatches[0].CallReport, "op-1", attempt, decision, "")
	if dispatches[0].Outcome != failure || dispatches[0].Cause != "authorization_expired" {
		t.Fatalf("dispatch = %+v, want failure with its cause", dispatches[0])
	}
}

// TestReportCallRecordsWhatItCannotJoinAsAMismatch: a report for an attempt
// the broker never issued, or for another operation, is recorded, not
// dropped, and names no decision.
func TestReportCallRecordsWhatItCannotJoinAsAMismatch(t *testing.T) {
	f := newReportFixture(t)
	attempt, _ := f.authorized(t, "op-1")
	other := openGranted(t)
	tests := map[string]struct {
		req  identitybroker.ReportCallRequest
		want identitybroker.ReportMismatch
	}{
		"an attempt never issued": {callReport(f.s.ID, "op-1", "att_never", success, success), identitybroker.MismatchAttemptUnknown},
		"another operation":       {callReport(f.s.ID, "op-other", attempt, success, unknown), identitybroker.MismatchOperation},
		"an unknown session":      {callReport("bs_unknown", "op-1", attempt, failure, ""), identitybroker.MismatchAttemptUnknown},
		"another session":         {callReport(other.s.ID, "op-1", attempt, success, success), identitybroker.MismatchAttemptUnknown},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			before, _, _ := f.records()
			if err := f.report(tc.req); err != nil {
				t.Fatalf("ReportCall = %v, want the mismatch recorded and accepted", err)
			}
			dispatches, outcomes, _ := f.records()
			if len(dispatches) != len(before)+1 {
				t.Fatalf("recorded %d dispatches, want %d", len(dispatches), len(before)+1)
			}
			d := dispatches[len(dispatches)-1]
			if d.Mismatch != tc.want || d.DecisionRecord != "" || d.AttemptID != tc.req.AttemptID || d.OperationID != tc.req.OperationID ||
				d.Outcome != tc.req.Dispatch {
				t.Fatalf("dispatch = %+v, want mismatch %q naming no decision", d, tc.want)
			}
			if got := d.Subjects()[identitybroker.SubjectAgentActorID]; got.Provenance != identitybroker.ProvenanceUnavailable {
				t.Fatalf("agent_actor_id = %+v, want unavailable without a decision", got)
			}
			if tc.req.Dispatch == success && outcomes[len(outcomes)-1].Mismatch != tc.want {
				t.Fatalf("outcome = %+v, want mismatch %q", outcomes[len(outcomes)-1], tc.want)
			}
		})
	}
}

func TestReportCallRecordsAConflictingReportAsAMismatch(t *testing.T) {
	f := newReportFixture(t)
	attempt, decision := f.authorized(t, "op-1")
	if err := f.report(callReport(f.s.ID, "op-1", attempt, success, success)); err != nil {
		t.Fatalf("ReportCall: %v", err)
	}
	if err := f.report(callReport(f.s.ID, "op-1", attempt, failure, "")); err != nil {
		t.Fatalf("conflicting ReportCall = %v, want accepted", err)
	}
	dispatches, _, _ := f.records()
	if len(dispatches) != 2 {
		t.Fatalf("recorded %d dispatches, want 2", len(dispatches))
	}
	wantJoined(t, dispatches[1].CallReport, "op-1", attempt, decision, identitybroker.MismatchConflictingReport)
}

// TestReportCallUnknownOutcomeRetryAndLateResult: a retry after an unknown
// outcome is a new attempt, and a late result is a new observed-outcome
// record in the unknown outcome's attempt, following its record.
func TestReportCallUnknownOutcomeRetryAndLateResult(t *testing.T) {
	f := newReportFixture(t)
	attempt, decision := f.authorized(t, "op-1")
	if err := f.report(callReport(f.s.ID, "op-1", attempt, success, unknown)); err != nil {
		t.Fatalf("ReportCall: %v", err)
	}
	retry, retryDecision := f.authorized(t, "op-1")
	if retry == attempt {
		t.Fatal("a retry reuses the attempt")
	}
	if err := f.report(callReport(f.s.ID, "op-1", retry, success, success)); err != nil {
		t.Fatalf("ReportCall of the retry: %v", err)
	}
	late := callReport(f.s.ID, "op-1", attempt, success, success)
	late.LateResult = true
	if err := f.report(late); err != nil {
		t.Fatalf("late ReportCall: %v", err)
	}
	// The same late result again records nothing; a different one, or a
	// late result after a known outcome, is a mismatch.
	if err := f.report(late); err != nil {
		t.Fatalf("late ReportCall again: %v", err)
	}
	second := late
	second.Outcome = failure
	lateAfterKnown := late
	lateAfterKnown.AttemptID = retry
	for _, req := range []identitybroker.ReportCallRequest{second, lateAfterKnown} {
		if err := f.report(req); err != nil {
			t.Fatalf("unexpected late ReportCall = %v, want accepted", err)
		}
	}

	_, outcomes, _ := f.records()
	if len(outcomes) != 5 {
		t.Fatalf("recorded %d outcomes, want 5: %+v", len(outcomes), outcomes)
	}
	first, retried, lateRec := outcomes[0], outcomes[1], outcomes[2]
	wantJoined(t, first.CallReport, "op-1", attempt, decision, "")
	wantJoined(t, retried.CallReport, "op-1", retry, retryDecision, "")
	wantJoined(t, lateRec.CallReport, "op-1", attempt, decision, "")
	if first.Outcome != unknown || !lateRec.LateResult || lateRec.Outcome != success || lateRec.Follows != first.AuditID {
		t.Fatalf("unknown outcome %+v and late result %+v, want the late result following the unknown one", first, lateRec)
	}
	wantJoined(t, outcomes[3].CallReport, "op-1", attempt, decision, identitybroker.MismatchUnexpectedLateResult)
	wantJoined(t, outcomes[4].CallReport, "op-1", retry, retryDecision, identitybroker.MismatchUnexpectedLateResult)
	if outcomes[3].Follows != "" || outcomes[4].Follows != "" {
		t.Fatal("an unexpected late result follows a record")
	}
	// A late result is an outcome alone: no second dispatch.
	if dispatches, _, _ := f.records(); len(dispatches) != 2 {
		t.Fatalf("recorded %d dispatches, want one per attempt", len(dispatches))
	}
}

func TestReportCallJoinsAfterTheSessionCloses(t *testing.T) {
	f := newReportFixture(t)
	attempt, decision := f.authorized(t, "op-1")
	if err := f.b.CloseSession(context.Background(), testWorkload, identitybroker.CloseSessionRequest{SessionID: f.s.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	if err := f.report(callReport(f.s.ID, "op-1", attempt, success, unknown)); err != nil {
		t.Fatalf("ReportCall after close: %v", err)
	}
	_, outcomes, _ := f.records()
	if len(outcomes) != 1 {
		t.Fatalf("recorded %d outcomes, want 1", len(outcomes))
	}
	wantJoined(t, outcomes[0].CallReport, "op-1", attempt, decision, "")
}

func TestReportCallListingAttemptsAreNotReported(t *testing.T) {
	f := newReportFixture(t)
	req := callRequest(f.s.ID, f.cred)
	req.Method, req.Tool, req.ArgumentsSHA256 = identitybroker.MethodToolsList, "", ""
	auth, err := f.authorize(req)
	if err != nil {
		t.Fatalf("AuthorizeCall tools/list: %v", err)
	}
	if err := f.report(callReport(f.s.ID, "op-1", auth.AttemptID, success, success)); err != nil {
		t.Fatalf("ReportCall: %v", err)
	}
	if dispatches, _, _ := f.records(); len(dispatches) != 1 || dispatches[0].Mismatch != identitybroker.MismatchAttemptUnknown {
		t.Fatalf("dispatches = %+v, want a mismatch", dispatches)
	}
}

// TestReportCallRejectsOutcomesOutsideTheSets: a dispatch or outcome outside
// its fixed set is refused, and the refusal is recorded as a mismatch
// without the refused value.
func TestReportCallRejectsOutcomesOutsideTheSets(t *testing.T) {
	f := newReportFixture(t)
	attempt, _ := f.authorized(t, "op-1")
	const planted = "planted_secret_value"
	tests := map[string]struct {
		req      identitybroker.ReportCallRequest
		mismatch identitybroker.ReportMismatch
		named    bool
	}{
		"a dispatch outside its set": {callReport(f.s.ID, "op-1", attempt, planted, success), identitybroker.MismatchOutcomeNotInSet, true},
		"a denied dispatch":          {callReport(f.s.ID, "op-1", attempt, identitybroker.OutcomeDenied, ""), identitybroker.MismatchOutcomeNotInSet, true},
		"an outcome outside its set": {callReport(f.s.ID, "op-1", attempt, success, planted), identitybroker.MismatchOutcomeNotInSet, true},
		"a denied outcome":           {callReport(f.s.ID, "op-1", attempt, success, identitybroker.OutcomeDenied), identitybroker.MismatchOutcomeNotInSet, true},
		"a malformed attempt":        {callReport(f.s.ID, "op-1", "att with spaces", success, success), identitybroker.MismatchInvalidReport, false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, before := f.records()
			err := f.report(tc.req)
			wantError(t, err, identitybroker.CodeInvalidArgument, "")
			if strings.Contains(err.Error(), planted) {
				t.Fatalf("error %q echoes the refused value", err)
			}
			_, _, rejected := f.records()
			if len(rejected) != len(before)+1 {
				t.Fatalf("recorded %d rejected reports, want %d", len(rejected), len(before)+1)
			}
			r := rejected[len(rejected)-1]
			if r.Header().Type != identitybroker.TypeToolReportRejected || r.Outcome != identitybroker.OutcomeDenied ||
				r.Code != identitybroker.CodeInvalidArgument || r.Mismatch != tc.mismatch || r.DecisionRecord != "" {
				t.Fatalf("rejected report = %+v, want denied, invalid argument, mismatch %q", r, tc.mismatch)
			}
			if named := r.AttemptID == attempt && r.OperationID == "op-1" && r.SessionID == f.s.ID; named != tc.named {
				t.Fatalf("rejected report names the request's IDs: %v, want %v", named, tc.named)
			}
			if strings.Contains(fmt.Sprintf("%+v", r), planted) {
				t.Fatalf("rejected report %+v records the refused value", r)
			}
		})
	}
	if dispatches, outcomes, _ := f.records(); len(dispatches) != 0 || len(outcomes) != 0 {
		t.Fatalf("a refused report recorded %d dispatches and %d outcomes", len(dispatches), len(outcomes))
	}
}

func TestReportCallRequestValidate(t *testing.T) {
	valid := callReport("bs_1", "op_1", "att_1", success, success)
	tests := map[string]func(*identitybroker.ReportCallRequest){
		"no session":                     func(r *identitybroker.ReportCallRequest) { r.SessionID = "" },
		"no operation":                   func(r *identitybroker.ReportCallRequest) { r.OperationID = "" },
		"no attempt":                     func(r *identitybroker.ReportCallRequest) { r.AttemptID = "" },
		"no dispatch":                    func(r *identitybroker.ReportCallRequest) { r.Dispatch = "" },
		"an error dispatch":              func(r *identitybroker.ReportCallRequest) { r.Dispatch = identitybroker.OutcomeError },
		"an unknown dispatch":            func(r *identitybroker.ReportCallRequest) { r.Dispatch = unknown },
		"a dispatched call no outcome":   func(r *identitybroker.ReportCallRequest) { r.Outcome = "" },
		"an undispatched call outcome":   func(r *identitybroker.ReportCallRequest) { r.Dispatch = failure },
		"an undispatched late result":    func(r *identitybroker.ReportCallRequest) { r.Dispatch, r.Outcome, r.LateResult = failure, "", true },
		"an unknown late result":         func(r *identitybroker.ReportCallRequest) { r.Outcome, r.LateResult = unknown, true },
		"a cause with spaces":            func(r *identitybroker.ReportCallRequest) { r.Cause = "invalid token" },
		"a cause in upper case":          func(r *identitybroker.ReportCallRequest) { r.Cause = "INVALID_TOKEN" },
		"a cause past its bound":         func(r *identitybroker.ReportCallRequest) { r.Cause = strings.Repeat("a", 65) },
		"a cause that could hold values": func(r *identitybroker.ReportCallRequest) { r.Cause = "Bearer.eyJhbGciOi" },
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate(valid) = %v", err)
	}
	for _, ok := range []identitybroker.ReportCallRequest{
		callReport("bs_1", "op_1", "att_1", failure, ""),
		callReport("bs_1", "op_1", "att_1", success, identitybroker.OutcomeError),
		callReport("bs_1", "op_1", "att_1", success, unknown),
		{SessionID: "bs_1", OperationID: "op_1", AttemptID: "att_1", Dispatch: success, Outcome: failure, LateResult: true, Cause: strings.Repeat("a", 64)},
	} {
		if err := ok.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", ok, err)
		}
	}
	for name, edit := range tests {
		r := valid
		edit(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: Validate = nil, want an error", name)
		}
	}
}

func TestReportCallRecordsCarryNoCredential(t *testing.T) {
	f := newReportFixture(t)
	attempt, _ := f.authorized(t, "op-1")
	if err := f.report(callReport(f.s.ID, "op-1", attempt, success, success)); err != nil {
		t.Fatalf("ReportCall: %v", err)
	}
	dispatches, outcomes, _ := f.records()
	exposed := fmt.Sprintf("%+v %+v %v %v", dispatches, outcomes, dispatches[0].Subjects(), outcomes[0].Subjects())
	secrets := []string{f.cred.Value()}
	for _, tok := range f.as2.tokens() {
		secrets = append(secrets, tok.Value())
	}
	for _, tok := range f.issuer.tokens() {
		secrets = append(secrets, tok.Value())
	}
	for _, s := range secrets {
		if strings.Contains(exposed, s) {
			t.Fatalf("report records expose a credential: %s", exposed)
		}
	}
}
