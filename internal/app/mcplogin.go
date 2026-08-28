package app

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

var (
	// ErrMCPLoginConfig reports that the resolved server is not eligible for a
	// one-shot OAuth login operation.
	ErrMCPLoginConfig = errors.New("mcp login: invalid server configuration")
	// ErrMCPLoginFailed is the parent category for redacted operational failures.
	ErrMCPLoginFailed = errors.New("mcp login: failed")
	// ErrMCPLoginAuthorization reports an unavailable or rejected authorization flow.
	ErrMCPLoginAuthorization = mcpLoginCategory("mcp login: authorization unavailable or login required")
	// ErrMCPLoginConnect reports failure to connect to or verify the MCP server.
	ErrMCPLoginConnect = mcpLoginCategory("mcp login: MCP connect or verification failed")
	// ErrMCPLoginCredential reports that a durable credential was not established.
	ErrMCPLoginCredential = mcpLoginCategory("mcp login: credential was not established or persisted")
	// ErrMCPLoginCleanup reports failure to close the temporary login resources.
	ErrMCPLoginCleanup = mcpLoginCategory("mcp login: temporary-session cleanup failed")
)

type mcpLoginCategory string

func (e mcpLoginCategory) Error() string      { return string(e) }
func (mcpLoginCategory) Is(target error) bool { return target == ErrMCPLoginFailed }

type mcpLoginDiagnostic struct {
	category   error
	diagnostic error
}

func (e *mcpLoginDiagnostic) Error() string {
	return e.category.Error() + ": " + e.diagnostic.Error()
}

func (e *mcpLoginDiagnostic) Unwrap() []error {
	return []error{e.category, e.diagnostic}
}

func loginDiagnostic(category, diagnostic error) error {
	var provider *oauthlogin.AuthorizationErrorResponse
	var rejected *oauthlogin.CallbackRejectedError
	var bind *oauthlogin.CallbackBindError
	switch {
	case errors.As(diagnostic, &provider):
		diagnostic = provider.Sanitized()
	case errors.As(diagnostic, &rejected):
		diagnostic = rejected.Sanitized()
	case errors.As(diagnostic, &bind):
		diagnostic = &oauthlogin.CallbackBindError{Reason: bind.Reason}
	default:
		return category
	}
	return &mcpLoginDiagnostic{category: category, diagnostic: diagnostic}
}

// LoginMCP runs one host-authorized OAuth login against an already-resolved MCP
// server configuration. The runtime and credential store are borrowed. A nil
// error means the authenticated MCP initialize and initial tool listing
// completed, a usable credential was durably stored or restored, and the
// temporary server/controller were closed.
func LoginMCP(ctx context.Context, cfg mcp.ServerConfig, runtime *oauthlogin.Runtime) error {
	if err := validateMCPLoginConfig(cfg, runtime); err != nil {
		return err
	}

	var operationCategory error
	err := runtime.Authorize(ctx, cfg.OAuth.Issuer, func(ctx context.Context, redirectURL string, present func(context.Context, string) (oauthlogin.Result, error)) error {
		loginCfg := cfg
		oauth := *cfg.OAuth
		oauth.RedirectURL = redirectURL
		oauth.Presenter = mcp.OAuthLoginPresenter(present)
		loginCfg.OAuth = &oauth

		server, err := mcp.Connect(ctx, loginCfg, nil)
		if err != nil {
			if errors.Is(err, mcp.ErrOAuthLoginRequired) || errors.Is(err, mcp.ErrOAuthUnavailable) {
				operationCategory = ErrMCPLoginAuthorization
			} else {
				operationCategory = ErrMCPLoginConnect
			}
			return err
		}
		ready := server.HasOAuthCredential()
		closeErr := server.Close()
		if !ready {
			operationCategory = ErrMCPLoginCredential
			return errors.New("MCP OAuth credential was not established")
		}
		if closeErr != nil {
			operationCategory = ErrMCPLoginCleanup
		}
		return closeErr
	})
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, oauthlogin.ErrBrowserLaunch) || errors.Is(err, oauthlogin.ErrCallbackAttempts) {
		return err
	}
	if operationCategory != nil {
		return loginDiagnostic(operationCategory, err)
	}
	if errors.Is(err, oauthlogin.ErrAuthorizationFailed) {
		return loginDiagnostic(ErrMCPLoginAuthorization, err)
	}
	return ErrMCPLoginCleanup
}

func validateMCPLoginConfig(cfg mcp.ServerConfig, runtime *oauthlogin.Runtime) error {
	if runtime == nil || cfg.Name == "" || cfg.URL == "" || cfg.OAuth == nil {
		return ErrMCPLoginConfig
	}
	if cfg.OAuth.Presenter != nil || cfg.OAuth.RedirectURL != "" {
		return ErrMCPLoginConfig
	}
	if cfg.OAuth.CredentialStore == nil || cfg.OAuth.CredentialReader != nil || mcp.HasCredentialHeaders(cfg.Headers) {
		return ErrMCPLoginConfig
	}
	return nil
}
