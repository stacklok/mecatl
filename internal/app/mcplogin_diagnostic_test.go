package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

func TestDCRLoginPresenterRejectsScopeAndResourceDrift(t *testing.T) {
	const resource = "https://connector.example/gw/mcp"
	presented := 0
	presenter := dcrOAuthLoginPresenter(resource, func(context.Context, string) (oauthlogin.Result, error) {
		presented++
		return oauthlogin.Result{}, nil
	})
	for _, raw := range []string{
		"https://issuer.example/authorize?scope=openid+admin&resource=https%3A%2F%2Fconnector.example%2Fgw%2Fmcp",
		"https://issuer.example/authorize?scope=openid&scope=openid&resource=https%3A%2F%2Fconnector.example%2Fgw%2Fmcp",
		"https://issuer.example/authorize?scope=openid&resource=https%3A%2F%2Fconnector.example%2Fmcp",
		"https://issuer.example/authorize?scope=openid&resource=https%3A%2F%2Fconnector.example%2Fgw%2Fmcp&resource=https%3A%2F%2Fconnector.example%2Fgw%2Fmcp",
	} {
		if _, err := presenter.PresentAuthorization(context.Background(), raw); err == nil {
			t.Fatalf("invalid final authorization URL was accepted: %s", raw)
		}
	}
	if presented != 0 {
		t.Fatalf("invalid final authorization URLs reached host presenter %d times", presented)
	}

	valid := "https://issuer.example/authorize?scope=openid&resource=https%3A%2F%2Fconnector.example%2Fgw%2Fmcp"
	if _, err := presenter.PresentAuthorization(context.Background(), valid); err != nil {
		t.Fatalf("valid final authorization URL: %v", err)
	}
	if presented != 1 {
		t.Fatalf("valid final authorization URL reached host presenter %d times", presented)
	}
}

func TestMCPLoginDiagnosticPreservesCategoryAndSafeDetail(t *testing.T) {
	t.Run("provider fields remain printable and bounded", func(t *testing.T) {
		const printable = "provider-detail-token-canary"
		err := loginDiagnostic(ErrMCPLoginAuthorization, &oauthlogin.AuthorizationErrorResponse{
			Code:        "invalid_scope",
			Description: printable + "\nInjected" + strings.Repeat("x", 500),
		})
		var provider *oauthlogin.AuthorizationErrorResponse
		if !errors.Is(err, ErrMCPLoginAuthorization) || !errors.Is(err, ErrMCPLoginFailed) || !errors.Is(err, oauthlogin.ErrAuthorizationFailed) || !errors.As(err, &provider) {
			t.Fatalf("error lost category or diagnostic: %v", err)
		}
		if !strings.Contains(err.Error(), printable) || strings.Contains(err.Error(), "\n") || len(provider.Description) > 200 {
			t.Fatalf("provider diagnostic was not safely preserved: %q", err)
		}
	})

	t.Run("callback rejection uses closed reason", func(t *testing.T) {
		const canary = "attacker-token-state-path-body-canary\nInjected"
		err := loginDiagnostic(ErrMCPLoginAuthorization, fmt.Errorf("nested %s: %w", canary, &oauthlogin.CallbackRejectedError{Reason: canary}))
		var rejected *oauthlogin.CallbackRejectedError
		if !errors.Is(err, ErrMCPLoginAuthorization) || !errors.Is(err, ErrMCPLoginFailed) || !errors.Is(err, oauthlogin.ErrAuthorizationFailed) || !errors.As(err, &rejected) {
			t.Fatalf("error lost category or diagnostic: %v", err)
		}
		if strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), "token-state-path") || rejected.Reason != "callback did not satisfy validation requirements" {
			t.Fatalf("callback diagnostic leaked attacker detail: %v", err)
		}
	})

	for name, reason := range map[string]oauthlogin.CallbackBindReason{
		"address in use": oauthlogin.CallbackBindAddressInUse,
		"unavailable":    oauthlogin.CallbackBindUnavailable,
	} {
		t.Run("bind "+name, func(t *testing.T) {
			const canary = "nested-network-endpoint-token-state-path-canary"
			err := loginDiagnostic(ErrMCPLoginAuthorization, fmt.Errorf("%s: %w", canary, &oauthlogin.CallbackBindError{Reason: reason}))
			var bind *oauthlogin.CallbackBindError
			if !errors.Is(err, ErrMCPLoginAuthorization) || !errors.Is(err, ErrMCPLoginFailed) || !errors.Is(err, oauthlogin.ErrAuthorizationFailed) || !errors.As(err, &bind) {
				t.Fatalf("error lost category or bind diagnostic: %v", err)
			}
			if bind.Reason != reason || strings.Contains(err.Error(), canary) {
				t.Fatalf("bind diagnostic was not safely projected: %v", err)
			}
		})
	}

	const unknownCanary = "unknown-token-code-state-path-body-canary\nInjected"
	unknown := loginDiagnostic(ErrMCPLoginAuthorization, errors.New(unknownCanary))
	if unknown != ErrMCPLoginAuthorization || strings.Contains(unknown.Error(), unknownCanary) {
		t.Fatalf("unknown diagnostic was not collapsed: %v", unknown)
	}
}
