package mcpbroker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestBrokerRefreshDiagnosticsUseClosedValuesAndRedact(t *testing.T) {
	const secret = "refresh-secret-must-not-log"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.FormValue("refresh_token") {
		case "success":
			_, _ = w.Write([]byte(`{"access_token":"fresh-secret","token_type":"Bearer","expires_in":3600}`))
		case "invalid":
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"` + secret + `"}`))
		default:
			_, _ = w.Write([]byte(`{"error":"temporarily_unavailable","error_description":"` + secret + `"}`))
		}
	}))
	defer server.Close()

	diag := &recordingBrokerDiagnostics{}
	harness := newProtectedHarness(t, server)
	harness.runtime.diag = diag.With("component", "mcpbroker")
	attachment, _ := attach(t, harness.runtime, "diagnostic-session")
	config := (&authorizationTransaction{route: harness.runtime.catalogue.routes[0].oauth}).oauthConfig("client-secret")

	for _, test := range []struct {
		name, refresh string
		wantErr       bool
	}{
		{name: "success", refresh: "success"},
		{name: "failure", refresh: "failure", wantErr: true},
		{name: "reauth", refresh: "invalid", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			attachment.logical.mu.Lock()
			attachment.logical.grants["github"] = &oauthGrant{config: config, token: &oauth2.Token{AccessToken: "stale-secret", RefreshToken: test.refresh, TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute)}}
			attachment.logical.mu.Unlock()
			_, err := (&scopedTokenSource{runtime: harness.runtime, logical: attachment.logical, backend: "github", ctx: context.Background()}).Token()
			if (err != nil) != test.wantErr {
				t.Fatalf("Token error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}

	logs := diag.String()
	for _, want := range []string{"componentmcpbroker", "eventtoken_refresh", "credentialroute", "sessiondiagnostic-session", "reasonsucceeded", "reasonfailed", "reasonreauth_required"} {
		if !strings.Contains(logs, want) {
			t.Errorf("diagnostics missing %q: %s", want, logs)
		}
	}
	for _, forbidden := range []string{secret, "fresh-secret", "stale-secret", server.URL, "client-secret"} {
		if strings.Contains(logs, forbidden) {
			t.Errorf("diagnostics expose %q: %s", forbidden, logs)
		}
	}
}

func TestBrokerAuthorizationLifecycleDiagnosticsRedactCallbackSecrets(t *testing.T) {
	const (
		code   = "authorization-code-must-not-log"
		secret = "client-secret-must-not-log"
		access = "access-token-must-not-log"
	)
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + access + `","token_type":"Bearer"}`))
	}))
	defer tokenServer.Close()

	harness := newProtectedHarness(t, tokenServer)
	diag := &recordingBrokerDiagnostics{}
	harness.runtime.diag = diag.With("component", "mcpbroker")
	attachment, _ := attach(t, harness.runtime, "authorization-observability")
	first, state := requestProtected(t, attachment, session.NewToolCall("safe-call", "mcp__github__create", json.RawMessage(`{"safe":true}`)))
	if got := callback(t, harness.runtime, code, state).Code; got != http.StatusOK {
		t.Fatalf("successful callback status = %d", got)
	}

	secondAttachment, _ := attach(t, harness.runtime, "authorization-observability-denied")
	second, deniedState := requestProtected(t, secondAttachment, session.NewToolCall("second-call", "mcp__github__create", json.RawMessage(`{"safe":false}`)))
	denied := httptest.NewRecorder()
	deniedRequest := httptest.NewRequest(http.MethodGet, "/callback?"+url.Values{"error": {"access_denied"}, "error_description": {"provider-detail-must-not-log"}, "state": {deniedState}}.Encode(), nil)
	harness.runtime.CallbackHandler().ServeHTTP(denied, deniedRequest)
	if denied.Code != http.StatusBadRequest {
		t.Fatalf("denied callback status = %d", denied.Code)
	}
	if first.ID == second.ID {
		t.Fatal("authorization identities were reused")
	}

	logs := diag.String()
	for _, want := range []string{
		"eventauthorization", "reasonrequest_started", "eventauthorization_lookup", "reasonfound",
		"reasoncallback_succeeded", "reasoncallback_denied", "oauth_erroraccess_denied",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("diagnostics missing %q: %s", want, logs)
		}
	}
	for _, forbidden := range []string{code, secret, access, state, deniedState, tokenServer.URL, "client-id", "provider-detail-must-not-log"} {
		if strings.Contains(logs, forbidden) {
			t.Errorf("diagnostics expose %q: %s", forbidden, logs)
		}
	}
}

func TestBrokerRouteUnavailableDiagnosticsRedactCalls(t *testing.T) {
	diag := &recordingBrokerDiagnostics{}
	runtime := testAnonymousRuntime(t)
	runtime.diag = diag.With("component", "mcpbroker")
	attachment := testAttachment(t, runtime)
	attachment.catalogue = newAttachmentCatalogue(nil, nil, nil)

	candidate := &sessionTool{attachment: attachment, route: route{backend: "configured", spec: tool.ToolSpec{Name: "mcp__configured__safe"}}}
	if result, err := candidate.Execute(t.Context(), session.NewToolCall("call", "mcp__configured__safe", json.RawMessage(`{"secret":"raw-args-must-not-log"}`)), tool.Environment{}); err != nil || !result.IsError {
		t.Fatalf("native Execute = (%+v, %v)", result, err)
	}
	query := &attachmentQueryTool{attachment: attachment}
	_, _ = query.Execute(t.Context(), session.NewToolCall("query", "CallMcpWithQuery", json.RawMessage(`{"server":"missing","tool":"secret-tool","args":{"secret":"raw-args-must-not-log"},"jq_filter":"."}`)), tool.Environment{})

	logs := diag.String()
	for _, want := range []string{"eventroute_unavailable", "surfacenative", "surfacequery"} {
		if !strings.Contains(logs, want) {
			t.Errorf("diagnostics missing %q: %s", want, logs)
		}
	}
	for _, forbidden := range []string{"raw-args-must-not-log", "secret-tool", "mcp__configured__safe"} {
		if strings.Contains(logs, forbidden) {
			t.Errorf("diagnostics expose %q: %s", forbidden, logs)
		}
	}
}

func TestAuthenticatedCatalogueFreezeDiagnosticsAreEnrichedAndRedacted(t *testing.T) {
	secret := "untrusted-tool-and-schema-must-not-log"
	diag := &recordingBrokerDiagnostics{}
	runtime := testAnonymousRuntime(t)
	attachment := testAttachment(t, runtime)
	process := testCatalogueProcess(runtime, &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{
		"configured-secret": {Backend: "configured-secret", Tools: []ToolDefinition{{Backend: "configured-secret", Name: "mcp__configured-secret__" + secret, Description: secret, Schema: json.RawMessage(`{"secret":"` + secret + `"}`)}}},
	}}, "configured-secret")
	process.diag = diag.With("component", "mcpbroker")
	if _, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), process, staticTokenSource("broker-token"), nil); err != nil {
		t.Fatal(err)
	}

	logs := diag.String()
	for _, want := range []string{"eventauthenticated_catalogue_backend", "reasondiscovered", "backend_index0", "eventauthenticated_catalogue_freeze", "reasonsucceeded", "routes2"} {
		if !strings.Contains(logs, want) {
			t.Errorf("diagnostics missing %q: %s", want, logs)
		}
	}
	for _, forbidden := range []string{secret, "configured-secret", "broker-token"} {
		if strings.Contains(logs, forbidden) {
			t.Errorf("diagnostics expose %q: %s", forbidden, logs)
		}
	}

	failedRuntime := testAnonymousRuntime(t)
	failedAttachment := testAttachment(t, failedRuntime)
	failedProcess := testCatalogueProcess(failedRuntime, &orderedCapabilityQueries{fail: "configured-secret"}, "configured-secret")
	failedProcess.diag = diag.With("component", "mcpbroker")
	if _, err := failedAttachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), failedProcess, staticTokenSource("broker-token"), nil); err == nil {
		t.Fatal("FreezeAuthenticatedCatalogue unexpectedly succeeded")
	}
	if logs = diag.String(); !strings.Contains(logs, "reasondiscovery_failed") {
		t.Errorf("freeze failure diagnostic missing: %s", logs)
	}
}

func TestWorkspaceEnrollmentDiagnosticsCoverBundleFlow(t *testing.T) {
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"opaque","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	diag := &recordingBrokerDiagnostics{}
	runtime := newWorkspaceEnrollmentRuntime(t, tokenServer, &orderedCapabilityQueries{}, "github")
	runtime.diag = diag.With("component", "mcpbroker")
	attached, _, err := runtime.AttachSession(t.Context(), "workspace-enrollment-observability")
	if err != nil {
		t.Fatal(err)
	}
	enroller := attached.(contract.WorkspaceEnrollmentAttachment)
	presentation, err := enroller.BeginWorkspaceEnrollment(t.Context())
	if err != nil {
		t.Fatalf("BeginWorkspaceEnrollment: %v", err)
	}
	if _, err := enroller.ObserveWorkspaceEnrollment(t.Context(), presentation.Ref); err != nil {
		t.Fatalf("ObserveWorkspaceEnrollment: %v", err)
	}
	if _, err := enroller.CancelWorkspaceEnrollment(t.Context(), presentation.Ref); err != nil {
		t.Fatalf("CancelWorkspaceEnrollment: %v", err)
	}
	if _, _, err := runtime.AttachSession(t.Context(), "workspace-enrollment-observability"); err != nil {
		t.Fatalf("reattach: %v", err)
	}

	logs := diag.String()
	for _, want := range []string{
		"eventsession_attach", "reasoncreated", "reasonreattached", "eventworkspace_enrollment", "operationbegin", "operationobserve", "operationcancel",
		"reasonrequest_started", "reasonrequest_observed", "reasonrequest_cancelled",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("diagnostics missing %q: %s", want, logs)
		}
	}
}
