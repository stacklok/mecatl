package mcp

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// TestGatewayAttemptReport maps each attempt outcome to the dispatch and
// observed outcome of identitybroker.ReportCallRequest's fixed sets, and every
// combination it returns is one the broker accepts.
func TestGatewayAttemptReport(t *testing.T) {
	const (
		sent    = identitybroker.DispatchSuccess
		notSent = identitybroker.DispatchFailure
		success = identitybroker.ObservedSuccess
		failure = identitybroker.ObservedFailure
		unknown = identitybroker.ObservedUnknown
	)
	tests := map[string]struct {
		attempt  GatewayAttempt
		dispatch identitybroker.DispatchOutcome
		outcome  identitybroker.ObservedOutcome
		cause    string
	}{
		"never written": {
			attempt:  GatewayAttempt{Outcome: GatewayAttemptNotSent, Rejection: GatewayRejectionExpired},
			dispatch: notSent, cause: "authorization_expired",
		},
		"never written, no known reason": {attempt: GatewayAttempt{Outcome: GatewayAttemptNotSent}, dispatch: notSent},
		"refused before running": {
			attempt:  GatewayAttempt{Outcome: GatewayAttemptRefused, Rejection: GatewayRejectionInvalidToken},
			dispatch: sent, outcome: failure, cause: "invalid_token",
		},
		"completed with a result": {
			attempt:  GatewayAttempt{Outcome: GatewayAttemptCompleted, result: &mcpsdk.CallToolResult{}},
			dispatch: sent, outcome: success,
		},
		"completed with a tool error": {
			attempt:  GatewayAttempt{Outcome: GatewayAttemptCompleted, result: &mcpsdk.CallToolResult{IsError: true}},
			dispatch: sent, outcome: failure,
		},
		"completed with an error that means not processed": {
			attempt:  GatewayAttempt{Outcome: GatewayAttemptCompleted, rpcErr: &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams}},
			dispatch: sent, outcome: failure,
		},
		"unknown": {attempt: GatewayAttempt{Outcome: GatewayAttemptUnknown}, dispatch: sent, outcome: unknown},
		"token reused before the write": {
			attempt:  GatewayAttempt{Outcome: GatewayAttemptUnknown, Rejection: GatewayRejectionTokenReused},
			dispatch: sent, outcome: unknown, cause: "token_reused",
		},
	}
	for name, tc := range tests {
		dispatch, outcome, cause := tc.attempt.Report()
		if dispatch != tc.dispatch || outcome != tc.outcome || cause != tc.cause {
			t.Errorf("%s: Report = %q, %q, %q; want %q, %q, %q", name, dispatch, outcome, cause, tc.dispatch, tc.outcome, tc.cause)
		}
		req := identitybroker.ReportCallRequest{SessionID: "bs_1", OperationID: "op_1", AttemptID: "att_1", Dispatch: dispatch, Outcome: outcome, Cause: cause}
		if err := req.Validate(); err != nil {
			t.Errorf("%s: the broker refuses the report: %v", name, err)
		}
	}
}

// TestGatewayRejectionsAreReportableCauses: every rejection is a cause the
// broker accepts.
func TestGatewayRejectionsAreReportableCauses(t *testing.T) {
	for _, r := range []GatewayRejection{
		GatewayRejectionInvalidToken, GatewayRejectionTokenReused, GatewayRejectionDenied, GatewayRejectionSessionNotFound,
		GatewayRejectionTooLarge, GatewayRejectionURLElicitation, GatewayRejectionChanged, GatewayRejectionExpired,
	} {
		req := identitybroker.ReportCallRequest{SessionID: "bs_1", OperationID: "op_1", AttemptID: "att_1",
			Dispatch: identitybroker.DispatchFailure, Cause: string(r)}
		if err := req.Validate(); err != nil {
			t.Errorf("rejection %q: %v", r, err)
		}
	}
}
