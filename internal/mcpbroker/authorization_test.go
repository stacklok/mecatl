package mcpbroker_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

// scriptedTransactions answers each primitive from fixed values and counts calls.
type scriptedTransactions struct {
	status    session.AuthorizationStatus
	statusErr error
	outcome   mcpbroker.CancelOutcome
	cancelErr error
	url       string

	statusCalls, cancelCalls, presentCalls int
}

func (s *scriptedTransactions) PresentAuthorization(context.Context, session.ExternalAuthorization) (string, error) {
	s.presentCalls++
	return s.url, nil
}

func (s *scriptedTransactions) AuthorizationStatus(context.Context, session.ExternalAuthorization) (session.AuthorizationStatus, error) {
	s.statusCalls++
	return s.status, s.statusErr
}

func (s *scriptedTransactions) CancelAuthorization(context.Context, session.ExternalAuthorization) (mcpbroker.CancelOutcome, error) {
	s.cancelCalls++
	return s.outcome, s.cancelErr
}

var scriptedAuthorization = session.ExternalAuthorization{ID: "authorization-1", Binding: "binding-1"}

func TestSettleAuthorizationReconcilesTheCancellationOutcome(t *testing.T) {
	statusReadErr := errors.New("authoritative status read failed")
	for _, tc := range []struct {
		name        string
		outcome     mcpbroker.CancelOutcome
		settled     session.AuthorizationStatus
		status      session.AuthorizationStatus
		statusErr   error
		want        session.AuthorizationStatus
		wantErr     error
		wantInvalid string
	}{
		{name: "fresh expiry", outcome: mcpbroker.CancelCancelled, settled: session.AuthorizationExpired, want: session.AuthorizationExpired},
		{name: "fresh explicit cancellation", outcome: mcpbroker.CancelCancelled, settled: session.AuthorizationCancelled, want: session.AuthorizationCancelled},
		{name: "already cancelled", outcome: mcpbroker.CancelAlreadyCancelled, settled: session.AuthorizationExpired, want: session.AuthorizationCancelled},
		{name: "already resolved", outcome: mcpbroker.CancelAlreadyResolved, status: session.AuthorizationGranted, want: session.AuthorizationGranted},
		{name: "already resolved remains pending", outcome: mcpbroker.CancelAlreadyResolved, status: session.AuthorizationPending, wantInvalid: "resolved authorization remained pending"},
		{name: "status read error", outcome: mcpbroker.CancelAlreadyResolved, statusErr: statusReadErr, wantErr: statusReadErr},
		{name: "unknown outcome", outcome: "unknown", wantInvalid: "unknown cancellation outcome"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transactions := &scriptedTransactions{outcome: tc.outcome, status: tc.status, statusErr: tc.statusErr}
			got, err := mcpbroker.SettleAuthorization(t.Context(), transactions, scriptedAuthorization, tc.settled)
			var invalid *mcpbroker.InvalidAuthorizationOutcomeError
			switch {
			case tc.wantInvalid != "":
				if !errors.As(err, &invalid) || err.Error() != tc.wantInvalid {
					t.Fatalf("error = %v, want invalid outcome %q", err, tc.wantInvalid)
				}
			case err != tc.wantErr:
				// A read failure after a successful cancellation stays unwrapped.
				t.Fatalf("error = %#v, want unchanged %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSettleAuthorizationMarksAFailedCancellation(t *testing.T) {
	lost := errors.Join(errors.New("gone"), mcpbroker.ErrStateUnavailable)
	transactions := &scriptedTransactions{cancelErr: lost}
	status, err := mcpbroker.SettleAuthorization(t.Context(), transactions, scriptedAuthorization, session.AuthorizationCancelled)
	var cancelFailed *mcpbroker.CancelFailedError
	if !errors.As(err, &cancelFailed) || cancelFailed.Err != lost || status != "" {
		t.Fatalf("SettleAuthorization() = (%q, %#v), want a CancelFailedError around the cancellation's error", status, err)
	}
	if err.Error() != lost.Error() || !errors.Is(err, mcpbroker.ErrStateUnavailable) {
		t.Fatalf("error = %q, want the cancellation's text and ErrStateUnavailable", err)
	}
	if transactions.statusCalls != 0 {
		t.Fatalf("status reads = %d after a failed cancellation, want none", transactions.statusCalls)
	}
}

func TestPresentPendingAuthorizationPresentsOnlyWhilePending(t *testing.T) {
	statusErr := errors.New("status unavailable")
	for _, tc := range []struct {
		name        string
		status      session.AuthorizationStatus
		statusErr   error
		wantURL     string
		wantErr     error
		wantPresent int
	}{
		{name: "pending", status: session.AuthorizationPending, wantURL: "https://authorize.invalid/live", wantPresent: 1},
		{name: "granted", status: session.AuthorizationGranted, wantErr: mcpbroker.ErrAuthorizationNotPending},
		{name: "cancelled", status: session.AuthorizationCancelled, wantErr: mcpbroker.ErrAuthorizationNotPending},
		{name: "status error", statusErr: statusErr, wantErr: statusErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transactions := &scriptedTransactions{status: tc.status, statusErr: tc.statusErr, url: "https://authorize.invalid/live"}
			url, err := mcpbroker.PresentPendingAuthorization(t.Context(), transactions, scriptedAuthorization)
			if !errors.Is(err, tc.wantErr) || url != tc.wantURL {
				t.Fatalf("PresentPendingAuthorization() = (%q, %v), want (%q, %v)", url, err, tc.wantURL, tc.wantErr)
			}
			if transactions.presentCalls != tc.wantPresent {
				t.Fatalf("presentations = %d, want %d", transactions.presentCalls, tc.wantPresent)
			}
		})
	}
}

func TestExpireAuthorizationCancelsOnlyAPendingAuthorization(t *testing.T) {
	statusErr := errors.New("status unavailable")
	for _, tc := range []struct {
		name       string
		status     session.AuthorizationStatus
		statusErr  error
		outcome    mcpbroker.CancelOutcome
		want       session.AuthorizationStatus
		wantErr    error
		wantCancel int
	}{
		{name: "pending", status: session.AuthorizationPending, outcome: mcpbroker.CancelCancelled, want: session.AuthorizationExpired, wantCancel: 1},
		{name: "granted", status: session.AuthorizationGranted, want: session.AuthorizationGranted},
		{name: "denied", status: session.AuthorizationDenied, want: session.AuthorizationDenied},
		{name: "status error", statusErr: statusErr, wantErr: statusErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transactions := &scriptedTransactions{status: tc.status, statusErr: tc.statusErr, outcome: tc.outcome}
			got, err := mcpbroker.ExpireAuthorization(t.Context(), transactions, scriptedAuthorization)
			if !errors.Is(err, tc.wantErr) || (err == nil && got != tc.want) {
				t.Fatalf("ExpireAuthorization() = (%q, %v), want (%q, %v)", got, err, tc.want, tc.wantErr)
			}
			if transactions.cancelCalls != tc.wantCancel {
				t.Fatalf("cancellations = %d, want %d", transactions.cancelCalls, tc.wantCancel)
			}
		})
	}
}
