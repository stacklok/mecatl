package mcpbroker

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestRecoveredAttachExistingOwnerOnlyNeverCreatesOwnerOrLogicalState(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	defer runtime.Close()
	creator, _ := attach(t, runtime, "continuity-session")
	binding := creator.Binding()

	runtime.mu.RLock()
	logical := runtime.sessions["continuity-session"]
	runtime.mu.RUnlock()
	logical.mu.Lock()
	logical.provisional = true
	logical.recoveredProvisional = true
	logical.mu.Unlock()

	before := logical.attachments
	wrongGeneration := contractSessionBinding(runtime.bindingPrefix, 999)
	if _, _, err := runtime.AttachSessionExpectedBinding(t.Context(), "continuity-session", wrongGeneration); !errors.Is(err, contract.ErrContinuityUnavailable) {
		t.Fatalf("same-instance wrong generation error = %v", err)
	}
	logical.mu.RLock()
	afterWrongGeneration := logical.attachments
	stillProvisional := logical.provisional
	logical.mu.RUnlock()
	if afterWrongGeneration != before || !stillProvisional {
		t.Fatalf("wrong generation changed state: attachments=%d provisional=%t", afterWrongGeneration, stillProvisional)
	}

	if _, _, err := runtime.AttachSessionExpectedBinding(t.Context(), "continuity-session", "other-instance.1"); !errors.Is(err, contract.ErrBrokerIncarnationLost) || !errors.Is(err, contract.ErrStateUnavailable) {
		t.Fatalf("cross-instance error = %v", err)
	}
	reattached, outcome, err := runtime.AttachSessionExpectedBinding(t.Context(), "continuity-session", binding)
	if err != nil || outcome != contract.AttachRecoveredProvisional {
		t.Fatalf("exact recovered reattach = (%T, %q, %v)", reattached, outcome, err)
	}
	if _, _, err := runtime.AttachSession(t.Context(), "continuity-session"); !errors.Is(err, contract.ErrContinuityUnavailable) {
		t.Fatalf("legacy attach to recovered provisional error = %v", err)
	}
	logical.mu.RLock()
	if !logical.provisional || !logical.recoveredProvisional {
		t.Fatalf("reattach unexpectedly published recovered state: %+v", logical)
	}
	logical.mu.RUnlock()
	if err := reattached.Commit(t.Context()); err != nil {
		t.Fatalf("recovered Commit: %v", err)
	}
	if _, outcome, err := runtime.AttachSessionExpectedBinding(t.Context(), "continuity-session", binding); err != nil || outcome != contract.AttachReattached {
		t.Fatalf("committed expected reattach = (%q, %v)", outcome, err)
	}
}

func TestRecoveredAttemptExactRetryReattachesSameUnpublishedLogicalSession(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	defer runtime.Close()
	deadline := time.Now().Add(time.Minute)
	assertion := contract.CustodyAssertion{Guard: contract.ContinuityGuard{SessionID: "retry-session", SessionIncarnation: session.NewIncarnationID(), Providers: []string{"provider"}}, RecoveryReference: "reference", AttemptDeadline: deadline}
	assertion.Guard.OwnerPartition[0], assertion.Guard.WorkloadPartition[0], assertion.Guard.ProfileDigest[0] = 1, 2, 3
	process := &Process{Runtime: runtime}
	attempt, creator, err := process.reserveRecoveredAttempt(assertion, "request-1")
	if err != nil || !creator {
		t.Fatalf("reserve = (%#v, %t, %v)", attempt, creator, err)
	}
	source := &recoveredCredentialSource{active: func() bool { return true }, validate: func(context.Context) error { return nil }}
	handle, err := runtime.newRecoveredProvisional(assertion.Guard.SessionID, deadline, source)
	if err != nil {
		t.Fatalf("new recovered provisional: %v", err)
	}
	process.finishRecoveredAttempt(assertion, "request-1", attempt, handle.Binding(), true)
	reattached, err := process.replayRecoveredAttempt(t.Context(), attempt)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if reattached.Attachment.Binding() != handle.Binding() {
		t.Fatalf("replay binding = %q, want %q", reattached.Attachment.Binding(), handle.Binding())
	}
	runtime.mu.RLock()
	logical := runtime.sessions[assertion.Guard.SessionID]
	runtime.mu.RUnlock()
	logical.mu.RLock()
	unpublished := logical.provisional && logical.recoveredProvisional
	logical.mu.RUnlock()
	if !unpublished {
		t.Fatal("replay published recovered provisional state")
	}
	changed := assertion
	changed.RecoveryReference = "other"
	if _, creator, err := process.reserveRecoveredAttempt(changed, "request-1"); err == nil || creator {
		t.Fatalf("changed request reuse = creator=%t err=%v", creator, err)
	}
	_ = handle.Abort(t.Context())
	_, _ = reattached.Attachment.Close(t.Context())
}

func TestRecoveredProvisionalCommitRejectsExpiredAdmissionDeadline(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	defer runtime.Close()
	source := &recoveredCredentialSource{active: func() bool { return true }, validate: func(context.Context) error { return nil }}
	handle, err := runtime.newRecoveredProvisional("expired-recovery", time.Now().Add(time.Minute), source)
	if err != nil {
		t.Fatalf("new recovered provisional: %v", err)
	}
	handle.logical.mu.Lock()
	handle.logical.recoveryDeadline = time.Now().Add(-time.Second)
	handle.logical.mu.Unlock()
	if err := handle.Commit(t.Context()); !errors.Is(err, contract.ErrContinuityUnavailable) {
		t.Fatalf("Commit after deadline = %v, want continuity unavailable", err)
	}
	handle.logical.mu.RLock()
	provisional := handle.logical.provisional
	recovered := handle.logical.recoveredProvisional
	handle.logical.mu.RUnlock()
	if !provisional || !recovered {
		t.Fatalf("expired commit published recovered logical: provisional=%t recovered=%t", provisional, recovered)
	}
	if _, outcome, err := runtime.AttachSessionExpectedBinding(t.Context(), "expired-recovery", handle.Binding()); err != nil || outcome != contract.AttachRecoveredProvisional {
		t.Fatalf("expired provisional reattach = (%q, %v)", outcome, err)
	}
	_ = handle.Abort(t.Context())
}

func contractSessionBinding(prefix string, generation uint64) session.ExternalBinding {
	return session.ExternalBinding(prefix + "." + strconv.FormatUint(generation, 10))
}
