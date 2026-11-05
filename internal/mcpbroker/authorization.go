package mcpbroker

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// ErrAuthorizationNotPending means the exact authorization exists but is no
// longer pending, so it has nothing left to present.
var ErrAuthorizationNotPending = errors.New("mcp broker authorization not pending")

// InvalidAuthorizationOutcomeError means a cancellation produced an outcome the
// contract does not allow, so the authorization's status is not trustworthy.
// Reason is the whole message.
type InvalidAuthorizationOutcomeError struct {
	Reason string
}

func (e *InvalidAuthorizationOutcomeError) Error() string { return e.Reason }

// CancelFailedError means the exact cancellation itself failed, so the
// authorization was not settled by it. It reads and unwraps as the
// cancellation's error; a failure after the cancellation succeeded is returned
// without it.
type CancelFailedError struct {
	Err error
}

func (e *CancelFailedError) Error() string { return e.Err.Error() }

func (e *CancelFailedError) Unwrap() error { return e.Err }

// GrantedAuthorization is the broker-authored replacement for a parked call
// whose exact authorization was granted.
type GrantedAuthorization struct {
	// Tools is the independently owned broker tool set, including any
	// CallMcpWithQuery wrapper, that the host rebuilds the parked session from.
	Tools []tool.Tool
	// ArgumentsErr is non-nil when the parked call's arguments do not conform to
	// the authenticated schema in Tools. The authorization itself still succeeded;
	// the host must settle the call without running it.
	ArgumentsErr error
}

// AuthorizationTransactions is the exact-transaction primitive set, satisfied by
// every Attachment, from which the functions below compose the host's settling
// operations.
type AuthorizationTransactions interface {
	PresentAuthorization(context.Context, session.ExternalAuthorization) (string, error)
	AuthorizationStatus(context.Context, session.ExternalAuthorization) (session.AuthorizationStatus, error)
	CancelAuthorization(context.Context, session.ExternalAuthorization) (CancelOutcome, error)
}

var _ AuthorizationTransactions = Attachment(nil)

// PresentPendingAuthorization reads the exact authorization's status and
// presents it only while pending; otherwise it returns ErrAuthorizationNotPending.
func PresentPendingAuthorization(ctx context.Context, transactions AuthorizationTransactions, authorization session.ExternalAuthorization) (string, error) {
	status, err := transactions.AuthorizationStatus(ctx, authorization)
	if err != nil {
		return "", err
	}
	if status != session.AuthorizationPending {
		return "", ErrAuthorizationNotPending
	}
	return transactions.PresentAuthorization(ctx, authorization)
}

// SettleAuthorization cancels the exact authorization and reports the status it
// settled at: settled when this call cancelled it, AuthorizationCancelled when
// it was already cancelled, and the broker's own terminal status when it had
// already resolved. A failed cancellation is returned as *CancelFailedError; a
// failure to read an already-resolved status is returned unwrapped. A
// disallowed outcome is an *InvalidAuthorizationOutcomeError.
func SettleAuthorization(ctx context.Context, transactions AuthorizationTransactions, authorization session.ExternalAuthorization, settled session.AuthorizationStatus) (session.AuthorizationStatus, error) {
	outcome, err := transactions.CancelAuthorization(ctx, authorization)
	if err != nil {
		return "", &CancelFailedError{Err: err}
	}
	switch outcome {
	case CancelCancelled:
		return settled, nil
	case CancelAlreadyCancelled:
		return session.AuthorizationCancelled, nil
	case CancelAlreadyResolved:
		status, err := transactions.AuthorizationStatus(ctx, authorization)
		if err != nil {
			return "", err
		}
		if status == session.AuthorizationPending {
			return "", &InvalidAuthorizationOutcomeError{Reason: "resolved authorization remained pending"}
		}
		return status, nil
	default:
		return "", &InvalidAuthorizationOutcomeError{Reason: "unknown cancellation outcome"}
	}
}

// ExpireAuthorization settles the exact authorization as expired when it is
// still pending, and otherwise reports its current status unchanged. A host
// that must act between the status read and the settlement (for example, open
// a session before anything is settled) calls AuthorizationStatus and
// SettleAuthorization separately instead.
func ExpireAuthorization(ctx context.Context, transactions AuthorizationTransactions, authorization session.ExternalAuthorization) (session.AuthorizationStatus, error) {
	status, err := transactions.AuthorizationStatus(ctx, authorization)
	if err != nil || status != session.AuthorizationPending {
		return status, err
	}
	return SettleAuthorization(ctx, transactions, authorization, session.AuthorizationExpired)
}
