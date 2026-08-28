package mcp

import (
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

func TestProjectOAuthErrorPreservesOnlySafeCallbackDiagnostics(t *testing.T) {
	const canary = "nested-token-code-state-path-body-canary\nInjected"

	provider := projectOAuthError(&oauthlogin.AuthorizationErrorResponse{Code: "invalid_scope\nInjected", Description: strings.Repeat("d", 500)})
	var providerDiagnostic *oauthlogin.AuthorizationErrorResponse
	if !errors.As(provider, &providerDiagnostic) || !errors.Is(provider, oauthlogin.ErrAuthorizationFailed) || !errors.Is(provider, ErrOAuthUnavailable) {
		t.Fatalf("provider error = %v", provider)
	}
	if strings.Contains(provider.Error(), "\n") || len(providerDiagnostic.Description) > 200 {
		t.Fatalf("provider diagnostic was not sanitized: %q", provider)
	}

	rejected := projectOAuthError(&oauthlogin.CallbackRejectedError{Reason: canary})
	var rejectedDiagnostic *oauthlogin.CallbackRejectedError
	if !errors.As(rejected, &rejectedDiagnostic) || !errors.Is(rejected, ErrOAuthUnavailable) || strings.Contains(rejected.Error(), canary) {
		t.Fatalf("callback rejection leaked: %v", rejected)
	}

	unknown := projectOAuthError(errors.New(canary))
	if !errors.Is(unknown, ErrOAuthUnavailable) || strings.Contains(unknown.Error(), canary) {
		t.Fatalf("unknown error was not collapsed: %v", unknown)
	}
}
