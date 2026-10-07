package mcpbroker

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestExpectedBindingAttachBypassesOrdinaryObserverPublication(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	defer runtime.Close()
	creator, _ := attach(t, runtime, "expected-binding")
	runtime.mu.RLock()
	logical := runtime.sessions["expected-binding"]
	runtime.mu.RUnlock()
	logical.mu.Lock()
	logical.provisional, logical.recoveredProvisional = true, true
	logical.mu.Unlock()
	attachment, outcome, err := runtime.AttachSessionExpectedBinding(t.Context(), "expected-binding", creator.Binding())
	if err != nil || outcome != contract.AttachRecoveredProvisional {
		t.Fatalf("expected attach = (%T, %q, %v)", attachment, outcome, err)
	}
	logical.mu.RLock()
	provisional := logical.provisional
	logical.mu.RUnlock()
	if !provisional {
		t.Fatal("expected-binding attach published provisional state")
	}
}

func TestRecoveredAttachBindingMismatchAllocatesNoHandle(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	defer runtime.Close()
	_, _ = attach(t, runtime, "mismatch-binding")
	runtime.mu.RLock()
	logical := runtime.sessions["mismatch-binding"]
	runtime.mu.RUnlock()
	logical.mu.RLock()
	before := logical.attachments
	logical.mu.RUnlock()
	wrong := session.ExternalBinding(runtime.bindingPrefix + "." + strconv.Itoa(999))
	if _, _, err := runtime.AttachSessionExpectedBinding(t.Context(), "mismatch-binding", wrong); !errors.Is(err, contract.ErrContinuityUnavailable) {
		t.Fatalf("mismatch error = %v", err)
	}
	logical.mu.RLock()
	after := logical.attachments
	logical.mu.RUnlock()
	if after != before {
		t.Fatalf("binding mismatch allocated handle: %d -> %d", before, after)
	}
}

func TestExpectedBindingAttachDiagnosticsContainOnlyClosedFields(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	defer runtime.Close()
	creator, _ := attach(t, runtime, "expected-diagnostic-session")
	diag := &recordingBrokerDiagnostics{}
	runtime.diag = diag
	if _, _, err := runtime.AttachSessionExpectedBinding(t.Context(), "expected-diagnostic-session", creator.Binding()); err != nil {
		t.Fatal(err)
	}
	logs := diag.String()
	for _, want := range []string{"eventsession_attach", "reasonreattached"} {
		if !strings.Contains(logs, want) {
			t.Errorf("diagnostics missing %q: %s", want, logs)
		}
	}
	for _, forbidden := range []string{"expected-diagnostic-session", string(creator.Binding())} {
		if strings.Contains(logs, forbidden) {
			t.Errorf("diagnostics expose %q: %s", forbidden, logs)
		}
	}
}

func TestExpectedBindingRejectsMalformedBindings(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	defer runtime.Close()
	_, _ = attach(t, runtime, "malformed-binding")
	for _, binding := range []session.ExternalBinding{
		session.ExternalBinding(runtime.bindingPrefix + ".01"),
		session.ExternalBinding(runtime.bindingPrefix + ".0"),
		session.ExternalBinding(runtime.bindingPrefix + ".1.2"),
		".1",
	} {
		if _, _, err := runtime.AttachSessionExpectedBinding(t.Context(), "malformed-binding", binding); !errors.Is(err, contract.ErrContinuityUnavailable) {
			t.Errorf("AttachSessionExpectedBinding(%q) error = %v", binding, err)
		}
	}
}

func TestExpectedBindingCrossInstanceUnknownSessionDoesNotCreateState(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	defer runtime.Close()
	if _, _, err := runtime.AttachSessionExpectedBinding(t.Context(), "unknown-session", "other-instance.1"); !errors.Is(err, contract.ErrBrokerIncarnationLost) {
		t.Fatalf("cross-instance attach error = %v", err)
	}
	runtime.mu.RLock()
	_, exists := runtime.sessions["unknown-session"]
	runtime.mu.RUnlock()
	if exists {
		t.Fatal("cross-instance unknown attach created logical state")
	}
}

func TestOnlyRecoveredHandleCanAbortRecoveredAttempt(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	defer runtime.Close()
	creator, _ := attach(t, runtime, "recovered-owner")
	runtime.mu.RLock()
	logical := runtime.sessions["recovered-owner"]
	runtime.mu.RUnlock()
	logical.mu.Lock()
	logical.provisional, logical.recoveredProvisional = true, true
	logical.mu.Unlock()
	if err := creator.Abort(t.Context()); err != nil {
		t.Fatal(err)
	}
	replacement, outcome, err := runtime.AttachSessionExpectedBinding(t.Context(), "recovered-owner", creator.Binding())
	if err != nil || outcome != contract.AttachRecoveredProvisional {
		t.Fatalf("recovered reattach after original Abort = (%T, %q, %v)", replacement, outcome, err)
	}
}

func TestRecoveredHandleCommitAfterAttemptReplacedFails(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	defer runtime.Close()
	creator, _ := attach(t, runtime, "recovered-deleted")
	runtime.mu.RLock()
	logical := runtime.sessions["recovered-deleted"]
	runtime.mu.RUnlock()
	logical.mu.Lock()
	logical.provisional, logical.recoveredProvisional = true, true
	logical.mu.Unlock()
	recovered, err := runtime.newAttachedHandle(logical, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.DeleteSessionIfBinding(t.Context(), "recovered-deleted", creator.Binding()); err != nil {
		t.Fatal(err)
	}
	if _, outcome := attach(t, runtime, "recovered-deleted"); outcome != contract.AttachCreated {
		t.Fatalf("replace deleted attempt outcome = %q", outcome)
	}
	if err := recovered.Commit(t.Context()); !errors.Is(err, contract.ErrStateUnavailable) {
		t.Fatalf("Commit after replaced attempt error = %v", err)
	}
}

func TestRecoveredAttachConcurrentExactHandlesShareAttempt(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	defer runtime.Close()
	creator, _ := attach(t, runtime, "shared-attempt")
	runtime.mu.RLock()
	logical := runtime.sessions["shared-attempt"]
	runtime.mu.RUnlock()
	logical.mu.Lock()
	logical.provisional, logical.recoveredProvisional = true, true
	logical.mu.Unlock()
	type result struct {
		handle  contract.Attachment
		outcome contract.AttachOutcome
		err     error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			handle, outcome, err := runtime.AttachSessionExpectedBinding(t.Context(), "shared-attempt", creator.Binding())
			results <- result{handle: handle, outcome: outcome, err: err}
		}()
	}
	first, second := <-results, <-results
	for _, got := range []result{first, second} {
		if got.err != nil || got.outcome != contract.AttachRecoveredProvisional {
			t.Fatalf("concurrent exact attach = (%T, %q, %v)", got.handle, got.outcome, got.err)
		}
		if got.handle.Binding() != creator.Binding() {
			t.Fatalf("binding = %q, want %q", got.handle.Binding(), creator.Binding())
		}
	}
	logical.mu.RLock()
	attachments := logical.attachments
	logical.mu.RUnlock()
	if attachments != 3 {
		t.Fatalf("attachments = %d, want 3", attachments)
	}
	for _, handle := range []contract.Attachment{first.handle, second.handle} {
		if outcome, err := handle.Close(t.Context()); err != nil || outcome != contract.CloseClosed {
			t.Fatalf("Close = (%q, %v)", outcome, err)
		}
	}
}

func TestExpectedBindingAbortDoesNotDeleteRecoveredAttempt(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	defer runtime.Close()
	creator, _ := attach(t, runtime, "exact-abort")
	runtime.mu.RLock()
	logical := runtime.sessions["exact-abort"]
	runtime.mu.RUnlock()
	logical.mu.Lock()
	logical.provisional, logical.recoveredProvisional = true, true
	logical.mu.Unlock()
	first, _, err := runtime.AttachSessionExpectedBinding(t.Context(), "exact-abort", creator.Binding())
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := runtime.AttachSessionExpectedBinding(t.Context(), "exact-abort", creator.Binding())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Abort(t.Context()); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if err := second.Commit(t.Context()); err != nil {
		t.Fatalf("Commit after sibling Abort: %v", err)
	}
}

func TestExpectedBindingCommitAfterSiblingCommitSucceeds(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	defer runtime.Close()
	creator, _ := attach(t, runtime, "exact-commit")
	runtime.mu.RLock()
	logical := runtime.sessions["exact-commit"]
	runtime.mu.RUnlock()
	logical.mu.Lock()
	logical.provisional, logical.recoveredProvisional = true, true
	logical.mu.Unlock()
	first, _, err := runtime.AttachSessionExpectedBinding(t.Context(), "exact-commit", creator.Binding())
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := runtime.AttachSessionExpectedBinding(t.Context(), "exact-commit", creator.Binding())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Commit(t.Context()); err != nil {
		t.Fatalf("first Commit: %v", err)
	}
	if err := second.Commit(t.Context()); err != nil {
		t.Fatalf("second Commit after sibling Commit: %v", err)
	}
}
