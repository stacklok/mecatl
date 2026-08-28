package main

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestAuthRecoveryCandidateAdoptsOnlyVerifiedTerminalState(t *testing.T) {
	for _, state := range []string{"completed", "cancelled", "failed"} {
		resume := &client.ResumeSelection{Snapshot: client.SessionSnapshot{State: state}}
		if !shouldAdoptAuthRecoveryCandidate(resume, nil) {
			t.Fatalf("%s should be adopted", state)
		}
	}
	for _, state := range []string{"running", "awaiting", "idle", "", "future"} {
		resume := &client.ResumeSelection{Snapshot: client.SessionSnapshot{State: state}}
		if shouldAdoptAuthRecoveryCandidate(resume, nil) {
			t.Fatalf("%s must start fresh", state)
		}
	}
	if shouldAdoptAuthRecoveryCandidate(nil, nil) {
		t.Fatal("missing candidate must start fresh")
	}
	for _, err := range []error{
		errors.New("infrastructure ambiguous"),
		&client.AuthError{Reason: client.AuthSessionExpired},
	} {
		resume := &client.ResumeSelection{Snapshot: client.SessionSnapshot{State: "completed"}}
		if shouldAdoptAuthRecoveryCandidate(resume, err) {
			t.Fatalf("verification failure %v must start fresh", err)
		}
	}
}

func TestSafeAuthRecoveryStateOnlyAdoptsTerminalBoundaries(t *testing.T) {
	for _, state := range []string{"completed", "cancelled", "failed"} {
		if !safeAuthRecoveryState(state) {
			t.Fatalf("%s should be safe", state)
		}
	}
	for _, state := range []string{"running", "awaiting", "idle", ""} {
		if safeAuthRecoveryState(state) {
			t.Fatalf("%s must not be adopted after auth recovery", state)
		}
	}
}
