package toolhivellm

// tokensource.go is the ONE file in this package (and, with
// internal/adapter/mcp/source/toolhive.go, one of two in the whole tree) allowed
// to import github.com/stacklok/toolhive. It builds an in-process OIDC token
// source — the SAME `llm.NewTokenSource` that `thv llm token` and the ToolHive
// proxy use — so the `toolhive` LLM provider can talk DIRECTLY to the real
// `gateway_url` (no local proxy hop), with the access token obtained and
// refreshed in-process, never through a subprocess (issue #265).
//
// # Security invariants (v1-mandatory)
//
//   - The token NEVER enters a log, an error string, or an environment variable.
//     Errors are sanitised via llm.SanitizeTokenError (which strips any bearer
//     material an OIDC IdP may echo back in a RetrieveError body) before they
//     cross any boundary. The OS keyring (pkg/secrets) holds the refresh token;
//     only its REFERENCE (CachedRefreshTokenRef) is persisted to config, never
//     the token value.
//   - This file is the sole import of pkg/llm / pkg/secrets / pkg/auth/secrets /
//     pkg/config in the tree. The package's detector (detect.go) stays pure
//     stdlib+yaml and never decodes the OIDC subtree — the OIDC-presence check
//     lives HERE, over toolhive's own config read, so detect.go's
//     "tls_skip_verify/oidc never decoded" invariant (pinned by
//     TestDetectConfig_TLSSkipVerifyNeverDecoded) is untouched.
//
// # Layering
//
// ToolHive imports confined to THIS file: pkg/llm, pkg/secrets,
// pkg/auth/secrets, pkg/config. The rest of the package (detect*.go) stays
// stdlib + go.yaml.in/yaml/v3. Composition (internal/app) consumes the exported
// funcs; it never names a toolhive symbol.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/stacklok/toolhive/pkg/auth/secrets"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/llm"
	pkgsecrets "github.com/stacklok/toolhive/pkg/secrets"

	"github.com/stacklok/mecatl/engine/port"
)

// TokenSourceFunc is the composition-facing closure a direct-mode provider
// calls on every request to mint a fresh bearer token. The returned string is
// the access token; the error is ALREADY sanitised (no bearer material) and is
// safe to surface to a human or a log. It is the ONE seam the registry's
// bearer RoundTripper holds; tests inject a fake to exercise the transport
// without a real OS keyring.
type TokenSourceFunc func(ctx context.Context) (string, error)

// ErrTokenRequiredHint is the actionable remediation surfaced when the
// non-interactive token source returns llm.ErrTokenRequired (no cached
// credential and the browser flow is disabled). It names BOTH remediations so
// a headless operator sees the exact next step, mirroring errToolhiveNoModels.
//
// the next operator action, not a hardcoded credential — it carries no secret.
//
//nolint:gosec // G101 false positive: this is a remediation HINT string naming
const ErrTokenRequiredHint = "no cached ToolHive LLM gateway credential — run `thv llm setup` (or `mecatui login`) to log in, or use `--toolhive-llm-mode proxy`"

// loadLLMConfig reads the ToolHive LLM config block. configPath == "" resolves
// to toolhive's own default config path (NewDefaultProvider, the singleton the
// `thv` CLI itself uses); a non-empty configPath loads from that exact file so
// a test fixture never touches the real home directory. It is the shared read
// for both the OIDC-presence check and the token-source construction.
func loadLLMConfig(configPath string) (llm.Config, error) {
	if configPath == "" {
		return config.NewDefaultProvider().GetConfig().LLM, nil
	}
	cfg, err := config.LoadOrCreateConfigWithPath(configPath)
	if err != nil {
		return llm.Config{}, fmt.Errorf("load toolhive config %q: %w", configPath, err)
	}
	return cfg.LLM, nil
}

// tokenRefUpdater returns the config-persistence callback for a rotated
// refresh-token reference: it writes ONLY the secret key + expiry (never the
// token value) back to the config file. configPath == "" persists via the
// default path (UpdateConfig); a non-empty path persists to THAT file
// (UpdateConfigAtPath) so a test fixture never mutates the operator's real
// config. A persist failure is logged via diag when non-nil, or to stderr as a
// fallback (the CLI-only RunInteractiveLogin path). It is a best-effort write,
// never a hard failure (a rotated refresh token is still valid for the current
// process; the next non-interactive call re-derives it). It must NEVER surface
// the token value.
func tokenRefUpdater(configPath string, diag port.Diagnostics) llm.TokenRefUpdater {
	return func(key string, expiry time.Time) {
		update := func(c *config.Config) error {
			c.LLM.OIDC.CachedRefreshTokenRef = key
			c.LLM.OIDC.CachedTokenExpiry = expiry
			return nil
		}
		var err error
		if configPath == "" {
			err = config.UpdateConfig(update)
		} else {
			err = config.UpdateConfigAtPath(configPath, update)
		}
		if err != nil {
			// Best-effort: the rotation is still valid in-memory for this
			// process; persisting only saves the NEXT process from re-login.
			if diag != nil {
				diag.Log(context.Background(), port.LevelWarn, "toolhivellm: failed to persist LLM token reference", "error", err)
			} else {
				fmt.Fprintf(os.Stderr, "toolhivellm: warning: failed to persist LLM token reference: %v\n", err)
			}
		}
	}
}

// OIDCConfigured reports whether the ToolHive LLM config at configPath has the
// minimum OIDC trio (gateway_url + issuer + client_id) required for direct
// mode — the same llm.Config.IsConfigured() the `thv` CLI gates on. It is the
// composition gate resolveToolhiveIntent calls to discriminate direct vs proxy
// under mode=auto. A config read failure fails CLOSED (returns false): a
// missing/malformed config falls back to proxy mode (today's behaviour), never
// silently routes to a gateway with no credential.
func OIDCConfigured(configPath string) bool {
	llmCfg, err := loadLLMConfig(configPath)
	if err != nil {
		return false
	}
	return llmCfg.IsConfigured()
}

// buildTokenSource is the shared pipeline for both the non-interactive direct
// path and the interactive mecatui login: system secrets provider →
// ScopeLLM-scoped provider → config-persisting updater → llm.NewTokenSource.
// It mirrors buildLLMTokenSource in toolhive's own cmd/thv/app/llm.go so there
// is ONE token-source construction path, not two. interactive controls whether
// a genuine cache miss may launch the browser OIDC flow (false for the
// headless direct-mode provider, true for `mecatui login`); skipBrowser
// prints the auth URL instead of opening a browser (headless/SSH/CI), and has
// no effect unless interactive is also true.
func buildTokenSource(llmCfg llm.Config, configPath string, interactive, skipBrowser bool, diag port.Diagnostics) (*llm.TokenSource, error) {
	secretsProvider, err := secrets.GetSystemSecretsProvider()
	if err != nil {
		return nil, fmt.Errorf("toolhive secrets provider unavailable: %w", err)
	}
	scoped := pkgsecrets.NewScopedProvider(secretsProvider, pkgsecrets.ScopeLLM)
	return llm.NewTokenSource(&llmCfg, scoped, interactive, skipBrowser, tokenRefUpdater(configPath, diag)), nil
}

// DirectTokenSource builds the NON-INTERACTIVE token source a direct-mode
// `toolhive` provider calls on every request. A genuine cache miss (no cached
// or refreshable refresh token) returns llm.ErrTokenRequired — the caller
// surfaces it with ErrTokenRequiredHint; it NEVER silently launches a browser
// from a headless daemon. The returned TokenSourceFunc sanitises every error
// via llm.SanitizeTokenError so no bearer material an OIDC IdP echoes back in a
// RetrieveError body ever reaches a log or an error string. Returns an error
// (never a panicking nil func) when the config cannot be read or the secrets
// provider is unavailable, so Build fails fast with an actionable cause rather
// than constructing a provider that errors on every request.
func DirectTokenSource(configPath string, diag port.Diagnostics) (TokenSourceFunc, error) {
	llmCfg, err := loadLLMConfig(configPath)
	if err != nil {
		return nil, err
	}
	ts, err := buildTokenSource(llmCfg, configPath, false /* interactive */, false /* skipBrowser */, diag)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) (string, error) {
		tok, err := ts.Token(ctx)
		if err != nil {
			return "", sanitizeTokenError(err)
		}
		return tok, nil
	}, nil
}

// sanitizeTokenError maps a raw token-source error to a log-safe error.
// ErrTokenRequired → ErrTokenRequiredHint (naming BOTH remediations);
// everything else → llm.SanitizeTokenError(err) (strips bearer material).
// Exported for testing (the direct-mapping unit test, F5 AC #7).
func sanitizeTokenError(err error) error {
	if errors.Is(err, llm.ErrTokenRequired) {
		return errors.New(ErrTokenRequiredHint)
	}
	return errors.New(llm.SanitizeTokenError(err))
}

// RunInteractiveLogin runs the interactive OIDC browser flow in-process (the
// SAME buildTokenSource pipeline with interactive=true) and prints the fresh
// access token to stdout — the `mecatui login` subcommand. It is CLI-ONLY: it
// does NOT start a session or connect to a server. skipBrowser prints the
// authorization URL instead of opening a browser (headless/SSH/CI). The
// tokenRefUpdater persists the rotated refresh-token reference so a
// subsequent non-interactive DirectTokenSource call finds the credential
// without re-login.
func RunInteractiveLogin(ctx context.Context, configPath string, skipBrowser bool, diag port.Diagnostics) error {
	llmCfg, err := loadLLMConfig(configPath)
	if err != nil {
		return err
	}
	if !llmCfg.IsConfigured() {
		return errors.New("ToolHive LLM gateway is not configured — run `thv llm config set` first (gateway_url, oidc.issuer, oidc.client_id)")
	}
	ts, err := buildTokenSource(llmCfg, configPath, true /* interactive */, skipBrowser, diag)
	if err != nil {
		return err
	}
	// The token itself is the deliverable of `login`; printing it to stdout is
	// the point (mirrors `thv llm token`). It is never logged.
	tok, err := ts.Token(ctx)
	if err != nil {
		return errors.New(llm.SanitizeTokenError(err))
	}
	fmt.Println(tok)
	return nil
}
