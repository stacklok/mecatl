package mcpbroker

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func tsidEnrollmentRuntime(t *testing.T, tsids map[string]string) (*Runtime, *Attachment, contract.WorkspaceEnrollmentPresentation) {
	t.Helper()
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"broker","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(tokenServer.Close)
	responses := map[string]AuthenticatedCapabilities{}
	backends := make([]string, 0, len(tsids))
	for _, backend := range []string{"alpha", "beta"} {
		tsid, ok := tsids[backend]
		if !ok {
			continue
		}
		backends = append(backends, backend)
		responses[backend] = AuthenticatedCapabilities{Backend: backend, verifiedTSID: tsid, Tools: []ToolDefinition{
			{Backend: backend, Name: "mcp__" + backend + "__list", Description: "list", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true},
		}}
	}
	runtime := newWorkspaceEnrollmentRuntime(t, tokenServer, &orderedCapabilityQueries{responses: responses}, backends...)
	runtime.process.discovery = &authenticatedDiscovery{captureTSID: true}
	attached, _, err := runtime.AttachSession(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	handle := attached.(*Attachment)
	return runtime, handle, beginAndGrantWorkspaceEnrollment(t, runtime, handle)
}

func TestStageTSIDMustAgreeAcrossProtectedProviders(t *testing.T) {
	_, agreeing, ref := tsidEnrollmentRuntime(t, map[string]string{"alpha": "tsid-1", "beta": "tsid-1"})
	connected, err := agreeing.ObserveWorkspaceEnrollment(t.Context(), ref.Ref)
	if err != nil || connected.Status != contract.WorkspaceEnrollmentConnected {
		t.Fatalf("agreeing providers = (%+v, %v)", connected, err)
	}
	agreeing.mu.RLock()
	captured := agreeing.verifiedTSID
	agreeing.mu.RUnlock()
	if captured != "tsid-1" {
		t.Fatalf("captured tsid = %q", captured)
	}

	for name, tsids := range map[string]map[string]string{
		"mismatch": {"alpha": "tsid-1", "beta": "tsid-2"},
		"empty":    {"alpha": "tsid-1", "beta": ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, handle, ref := tsidEnrollmentRuntime(t, tsids)
			if result, err := handle.ObserveWorkspaceEnrollment(t.Context(), ref.Ref); err == nil && result.Status == contract.WorkspaceEnrollmentConnected {
				t.Fatal("discovery connected with disagreeing verified tsids")
			}
			handle.mu.RLock()
			captured := handle.verifiedTSID
			handle.mu.RUnlock()
			if captured != "" {
				t.Fatalf("failed discovery retained tsid %q", captured)
			}
		})
	}
}

func TestHandleStageRefusalsAreContinuityUnavailable(t *testing.T) {
	runtime := testAnonymousRuntime(t)
	attached, _, err := runtime.AttachSession(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	handle := attached.(*Attachment)
	ref := contract.WorkspaceEnrollmentRef{ID: "enroll-1", RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour)}
	deadline := time.Now().Add(time.Minute)
	base := contract.ContinuityGuard{SessionID: "session", SessionIncarnation: session.NewIncarnationID()}

	withDigest := base
	withDigest.ProfileDigest = [32]byte{1}
	withProviders := base
	withProviders.Providers = []string{"alpha"}
	otherSession := base
	otherSession.SessionID = "other-session"

	for name, guard := range map[string]contract.ContinuityGuard{
		"no frozen catalogue": base,
		"host digest":         withDigest,
		"host providers":      withProviders,
		"different session":   otherSession,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := handle.StageCredentialCustody(t.Context(), "stage-1", guard, ref, deadline); !errors.Is(err, contract.ErrContinuityUnavailable) {
				t.Fatalf("Stage = %v, want ErrContinuityUnavailable", err)
			}
		})
	}
}

func TestRetainedHandleCannotStageCustodyAfterLogicalDeletion(t *testing.T) {
	fixture := newFixture(t)
	runtime := testAnonymousRuntime(t)
	runtime.process = &Process{custody: fixture.core}
	attached, _, err := runtime.AttachSession(t.Context(), fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	handle := attached.(*Attachment)
	if _, err := runtime.DeleteSession(t.Context(), fixture.id); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	guard := contract.ContinuityGuard{SessionID: fixture.id, SessionIncarnation: fixture.request.Guard.Incarnation, OwnerPartition: fixture.request.Guard.OwnerPartition, WorkloadPartition: fixture.request.Guard.WorkloadPartition}
	ref := contract.WorkspaceEnrollmentRef{ID: "enroll-1", RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := handle.StageCredentialCustody(t.Context(), "stage-after-delete", guard, ref, fixture.request.AttemptDeadline); !errors.Is(err, contract.ErrContinuityUnavailable) {
		t.Fatalf("Stage after DeleteSession = %v, want continuity unavailable", err)
	}
	keys, err := fixture.client.Keys(t.Context(), "*").Result()
	if err != nil {
		t.Fatalf("inspect custody keys: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("Stage after deletion created custody state: %v", keys)
	}
}

func TestHandleOffersContinuityOnlyWithCustody(t *testing.T) {
	runtime := testAnonymousRuntime(t)
	attached, _, err := runtime.AttachSession(t.Context(), "session")
	if err != nil {
		t.Fatal(err)
	}
	handle := attached.(*Attachment)
	if handle.CredentialContinuity() {
		t.Fatal("handle without a process offered continuity")
	}
	runtime.process = &Process{}
	if handle.CredentialContinuity() {
		t.Fatal("process without custody offered continuity")
	}
	fixture := newFixture(t)
	runtime.process = &Process{custody: fixture.core, providers: []string{"provider"}}
	if !handle.CredentialContinuity() {
		t.Fatal("process with custody did not offer continuity")
	}
	runtime.process.closed = true
	if handle.CredentialContinuity() {
		t.Fatal("closed process offered continuity")
	}
}
