package app

import (
	"context"
	"errors"
	"strings"

	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

var (
	// ErrMCPLoginConfig reports that the resolved server is not eligible for a
	// one-shot OAuth login operation.
	ErrMCPLoginConfig = errors.New("mcp login: invalid server configuration")
	// ErrMCPLoginFailed reports a redacted connect, authentication, persistence,
	// or cleanup failure.
	ErrMCPLoginFailed = errors.New("mcp login: failed")
)

// LoginMCP runs one host-authorized OAuth login against an already-resolved MCP
// server configuration. The runtime and credential store are borrowed. A nil
// error means the authenticated MCP initialize and initial tool listing
// completed, a usable credential was durably stored or restored, and the
// temporary server/controller were closed.
func LoginMCP(ctx context.Context, cfg mcp.ServerConfig, runtime *oauthlogin.Runtime) error {
	if err := validateMCPLoginConfig(cfg, runtime); err != nil {
		return err
	}

	err := runtime.Authorize(ctx, cfg.OAuth.Issuer, func(ctx context.Context, redirectURL string, present func(context.Context, string) (oauthlogin.Result, error)) error {
		loginCfg := cfg
		oauth := *cfg.OAuth
		oauth.RedirectURL = redirectURL
		oauth.Presenter = mcp.OAuthLoginPresenter(present)
		loginCfg.OAuth = &oauth

		server, err := mcp.Connect(ctx, loginCfg, nil)
		if err != nil {
			return err
		}
		ready := server.HasOAuthCredential()
		closeErr := server.Close()
		if !ready {
			return errors.New("MCP OAuth credential was not established")
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
	return ErrMCPLoginFailed
}

func validateMCPLoginConfig(cfg mcp.ServerConfig, runtime *oauthlogin.Runtime) error {
	if runtime == nil || cfg.Name == "" || cfg.URL == "" || cfg.OAuth == nil {
		return ErrMCPLoginConfig
	}
	if cfg.OAuth.Presenter != nil || cfg.OAuth.RedirectURL != "" {
		return ErrMCPLoginConfig
	}
	for name := range cfg.Headers {
		if strings.EqualFold(name, "Authorization") {
			return ErrMCPLoginConfig
		}
	}
	return nil
}
