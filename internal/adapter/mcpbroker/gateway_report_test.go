package mcpbroker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// wantReports checks the reports f's broker received, every try included.
func wantReports(t *testing.T, f *gatewayFixture, want ...identitybroker.ReportCallRequest) {
	t.Helper()
	got := f.auth.reported()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("reports = %+v\nwant      %+v", got, want)
	}
}

// report is the report of attempt in call-1 of the main agent.
func report(attempt string, dispatch, outcome identitybroker.DecisionOutcome, cause string) identitybroker.ReportCallRequest {
	return identitybroker.ReportCallRequest{
		SessionID: "bs_test", OperationID: operationID(gatewayTestSession, gatewayTestSession, "call-1"),
		AttemptID: attempt, Dispatch: dispatch, Outcome: outcome, Cause: cause,
	}
}

const (
	success = identitybroker.OutcomeSuccess
	failure = identitybroker.OutcomeFailure
	unknown = identitybroker.OutcomeUnknown
)

func TestGatewayCallReportsItsAttempt(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantResult(t, res, err, gatewayTestResult)
	wantReports(t, f, report("att_1", success, success, ""))
}

func TestGatewayCallReportsEveryAttemptOfARetry(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{})
	f.wire.setOnToolsCall(f.g.RejectNextToken)
	res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
	wantResult(t, res, err, gatewayTestResult)
	// The rejected attempt is reported before the retry is authorized.
	wantReports(t, f,
		report("att_1", success, failure, "invalid_token"),
		report("att_2", success, success, ""))
}

func TestGatewayCallReportsOutcomesOfAttemptsThatDidNotComplete(t *testing.T) {
	tests := map[string]struct {
		arrange func(*gatewayFixture)
		row     gatewayOutcome
		want    identitybroker.ReportCallRequest
	}{
		"dropped after the write": {
			arrange: func(f *gatewayFixture) { f.g.DropAfterRequest("github") },
			row:     outcomeUnknown, want: report("att_1", success, unknown, ""),
		},
		"session not found": {
			arrange: func(f *gatewayFixture) { f.wire.setOnToolsCall(f.g.ForgetSessions) },
			row:     outcomeSessionNotFound, want: report("att_1", success, failure, "session_not_found"),
		},
		"answered with an error that means not processed": {
			arrange: func(f *gatewayFixture) { f.g.FailCalls("github", int(jsonrpc.CodeInvalidParams)) },
			want:    report("att_1", success, failure, ""),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newGatewayFixture(t, gatewayFixtureOptions{})
			tc.arrange(f)
			res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
			if tc.row.code != "" {
				wantRow(t, res, err, tc.row)
			} else if err != nil {
				t.Fatalf("Execute = %v", err)
			}
			wantReports(t, f, tc.want)
		})
	}
}

// TestGatewayCallCancelledReportsByWhetherItsToolsCallWasWritten: a call
// cancelled after its tools/call was written reports an unknown outcome,
// and one cancelled before reports a dispatch failure. Both still return the
// cancellation.
func TestGatewayCallCancelledReportsByWhetherItsToolsCallWasWritten(t *testing.T) {
	tests := map[string]struct {
		cancelOn func(t *testing.T, f *gatewayFixture, cancel context.CancelFunc)
		want     identitybroker.ReportCallRequest
		calls    int
	}{
		"after the write": {
			// Cancelled once the gateway has the tools/call, before its
			// answer is read.
			cancelOn: func(t *testing.T, f *gatewayFixture, cancel context.CancelFunc) {
				f.wire.respond = func(req *http.Request, body []byte) *http.Response {
					if !strings.Contains(string(body), `"method":"tools/call"`) {
						return nil
					}
					resp, err := f.wire.base.RoundTrip(req)
					cancel()
					if err != nil {
						t.Errorf("tools/call: %v", err)
						return nil
					}
					return resp
				}
			},
			want:  report("att_1", success, unknown, ""),
			calls: 1,
		},
		"before the write": {
			// Cancelled as the handshake's initialize is sent, so the
			// tools/call is never written.
			cancelOn: func(_ *testing.T, f *gatewayFixture, cancel context.CancelFunc) {
				f.wire.respond = func(_ *http.Request, body []byte) *http.Response {
					if strings.Contains(string(body), `"method":"initialize"`) {
						cancel()
					}
					return nil
				}
			},
			want: report("att_1", failure, "", ""),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newGatewayFixture(t, gatewayFixtureOptions{})
			ctx, cancel := context.WithCancel(mainAgent(t))
			defer cancel()
			tc.cancelOn(t, f, cancel)
			_, err := f.execute(ctx, t, "call-1", gatewayTestArgs)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Execute error = %v, want the cancellation", err)
			}
			wantReports(t, f, tc.want)
			if n := len(f.toolsCalls()); n != tc.calls {
				t.Fatalf("gateway saw %d tools/call, want %d", n, tc.calls)
			}
		})
	}
}

func TestGatewayCallReportFailureNeverFailsTheCall(t *testing.T) {
	unavailable := identitybroker.NewError(identitybroker.CodeUnavailable, "", "broker down")
	tests := map[string]struct {
		script []error
		tries  int
		logged string
	}{
		"retried until accepted":   {script: []error{unavailable, context.DeadlineExceeded, nil}, tries: 3},
		"retried until tries end":  {script: []error{unavailable, unavailable, unavailable}, tries: 3, logged: "broker_codeUNAVAILABLE"},
		"a refusal is not retried": {script: []error{identitybroker.NewError(identitybroker.CodeInvalidArgument, "", "bad")}, tries: 1, logged: "broker_codeINVALID_ARGUMENT"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newGatewayFixture(t, gatewayFixtureOptions{})
			f.auth.reportScript = tc.script
			res, err := f.execute(mainAgent(t), t, "call-1", gatewayTestArgs)
			wantResult(t, res, err, gatewayTestResult)
			want := make([]identitybroker.ReportCallRequest, tc.tries)
			for i := range want {
				want[i] = report("att_1", success, success, "")
			}
			wantReports(t, f, want...)
			diag := f.diag.String()
			if tc.logged == "" {
				if strings.Contains(diag, "report failed") {
					t.Fatalf("diagnostics = %q, want no failed report", diag)
				}
				return
			}
			if !strings.Contains(diag, "gateway call report failed") || !strings.Contains(diag, tc.logged) ||
				!strings.Contains(diag, fmt.Sprintf("tries%d", tc.tries)) {
				t.Fatalf("diagnostics = %q, want the failed report with %s after %d tries", diag, tc.logged, tc.tries)
			}
		})
	}
}

// TestGatewayCallReportCarriesNoCredentialArgumentsOrResult: a report names
// the attempt by reference and fixed values only.
func TestGatewayCallReportCarriesNoCredentialArgumentsOrResult(t *testing.T) {
	f := newGatewayFixture(t, gatewayFixtureOptions{agentToken: "agent-secret-credential", tokenPrefix: "gateway-secret-token-"})
	f.auth.reportScript = []error{identitybroker.NewError(identitybroker.CodeInvalidArgument, "", "bad")}
	const args = `{"secret_argument":"argument-value"}`
	if _, err := f.execute(mainAgent(t), t, "call-1", args); err != nil {
		t.Fatal(err)
	}
	reports := f.auth.reported()
	if len(reports) == 0 {
		t.Fatal("no report")
	}
	exposed := fmt.Sprintf("%+v %#v", reports, reports) + f.diag.String()
	for _, secret := range []string{"agent-secret-credential", "gateway-secret-token-", "secret_argument", "argument-value", gatewayTestResult} {
		if strings.Contains(exposed, secret) {
			t.Fatalf("report or its diagnostic exposes %q: %s", secret, exposed)
		}
	}
}
