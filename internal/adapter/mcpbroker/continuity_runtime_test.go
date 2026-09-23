package mcpbroker

import (
	"errors"
	"strconv"
	"testing"

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

func contractSessionBinding(prefix string, generation uint64) session.ExternalBinding {
	return session.ExternalBinding(prefix + "." + strconv.FormatUint(generation, 10))
}
