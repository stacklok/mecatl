package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/adapter/oidcclient"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/cliconfig"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

type providerCredentialConfig struct {
	definitions permconfig.ProviderDefinitions
	authPath    string
}

func currentProviderCredentialConfig() (providerCredentialConfig, error) {
	resolver := permconfig.NewWithEnv(permconfig.Options{Conventional: true}, xdgconfig.OSEnv)
	definitions, _, err := resolver.OperatorProviders()
	if err != nil {
		return providerCredentialConfig{}, err
	}
	path := authfile.DefaultPath(xdgconfig.OSEnv)
	if store := resolver.OperatorCredentialStore(); store != nil && store.APIKey != nil {
		path = store.APIKey.File
	}
	return providerCredentialConfig{definitions: definitions, authPath: path}, nil
}

var (
	errProviderCredentialCancelled = errors.New("provider credential prompt cancelled")
	errProviderSaveDeclined        = errors.New("provider API key save declined")
)

func (c providerCommands) runCredential(ctx context.Context, res invocationResolution, stdout, stderr io.Writer) (err error) {
	defer func() {
		if errors.Is(err, errProviderSaveDeclined) {
			err = nil
		}
	}()
	if len(res.remaining) == 1 && isHelpMetaFlag(res.remaining[0]) {
		return providerHelpResult(stderr, res.providerAction)
	}
	if res.providerName == toolHiveEndpointID {
		return c.runToolHiveCredential(ctx, res, stderr)
	}
	cfg, err := c.backend.loadCredentials()
	if err != nil {
		return fmt.Errorf("providers %s: load configured providers: %w", res.providerAction, err)
	}
	// A subscription provider's credential is a plan entitlement obtained by
	// signing in. Codex has no API key at all, so it always takes this path;
	// Anthropic keeps its API-key path and opts in with --subscription.
	if isSubscriptionProvider(res.providerName) {
		opts, subscription, err := parseSubscriptionFlags(res)
		if err != nil {
			return err
		}
		if subscription {
			if res.providerAction == providerActionLogout {
				return runSubscriptionLogout(ctx, res.providerName, stdout)
			}
			// A sign-in that an existing credential will shadow is inert.
			// Resolve that now so the operator is told at login rather than
			// discovering their requests still bill the other identity.
			shadowedBy := ""
			if inspection, inspectErr := c.backend.inspect(); inspectErr == nil {
				shadowedBy = cliconfig.SubscriptionShadowedBy(inspection.credentials, res.providerName)
			}
			return runSubscriptionLogin(ctx, res.providerName, opts, shadowedBy, stdout, stderr)
		}
	}
	if isBuiltinAPIKeyProvider(res.providerName) {
		if len(res.remaining) != 0 {
			return errors.New("providers login: --no-browser is available only for auth.method oidc")
		}
		return c.runAPIKey(ctx, res, cfg.authPath, stdout, stderr)
	}
	definition, ok := cfg.definitions[res.providerName]
	if !ok {
		return fmt.Errorf("provider %q is not a configured custom provider; login and logout are unavailable", res.providerName)
	}
	switch definition.Auth.Method {
	case providerAuthAPIKey:
		if len(res.remaining) != 0 {
			return errors.New("providers login: --no-browser is available only for auth.method oidc")
		}
		return c.runAPIKey(ctx, res, cfg.authPath, stdout, stderr)
	case providerAuthOIDC:
		return c.runOIDC(ctx, res, definition, stdout, stderr)
	default:
		return fmt.Errorf("provider %q uses auth.method %q; login and logout require locally managed credentials", res.providerName, definition.Auth.Method)
	}
}

func isBuiltinAPIKeyProvider(provider string) bool {
	_, ok := stockAPIKeyProviderFor(provider)
	return ok
}

func (c providerCommands) runAPIKey(ctx context.Context, res invocationResolution, authPath string, stdout, stderr io.Writer) error {
	var key *string
	if res.providerAction == providerActionLogin {
		// A provider that also supports signing in must say so before the key
		// prompt blocks on input: an operator who wants the subscription
		// otherwise sees only an API-key prompt and has no way to discover
		// the flag from here.
		if isSubscriptionProvider(res.providerName) {
			_, _ = fmt.Fprintf(stderr,
				"Reading an API key for %s. To sign in with a %s subscription instead, cancel and run:\n  mecatui providers login %s --subscription\n",
				res.providerName, subscriptionProviders[res.providerName], res.providerName)
		}
		if err := writeProviderKeyGuidance(stderr, res.providerName); err != nil {
			return err
		}
		inspection, err := c.inspectForEnrollment()
		if err != nil {
			return err
		}
		if strings.HasSuffix(inspection.sources[res.providerName], " (environment)") {
			if _, err := fmt.Fprintln(stderr, "Warning: the effective environment credential will still win over the saved file key; saving does not change that environment."); err != nil {
				return err
			}
		}
		entered, err := c.terminal.readAPIKey(ctx, res.providerName)
		if err != nil {
			return c.providerKeyReadError(err, stderr)
		}
		if err := validateProviderAPIKey(entered); err != nil {
			return err
		}
		save, err := c.confirmProviderAction(ctx, "Save this API key to the configured owner-only plaintext file? [y/N]")
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return c.credentialCancellation(stderr)
			}
			return errors.New("providers login: could not confirm saving; no API key saved")
		}
		if !save {
			if _, err := fmt.Fprintln(stdout, "API key not saved; no credential changes made."); err != nil {
				return err
			}
			return errProviderSaveDeclined
		}
		key = &entered
	}
	state, err := c.backend.updateAPIKey(ctx, authPath, authfile.APIKeyUpdate{Provider: res.providerName, APIKey: key})
	if state == authfile.CommitReplacementAppliedDurabilityUnknown {
		return fmt.Errorf("providers: replacement_applied_durability_unknown; the API key change may already be active; crash durability is uncertain. Inspect `mecatui providers status %s` and the configured credential file before a manual retry", providerDisplay(res.providerName))
	}
	if err != nil {
		if state == authfile.CommitNotApplied && errors.Is(err, context.Canceled) {
			return c.credentialCancellation(stderr)
		}
		return fmt.Errorf("providers %s: update locally managed API key: %w", res.providerAction, err)
	}
	if res.providerAction == providerActionLogin {
		if state == authfile.CommitNoop {
			_, err = fmt.Fprintf(stdout, "API key for provider %q is already configured\n", res.providerName)
		} else {
			_, err = fmt.Fprintf(stdout, "API key saved for provider %q\n", res.providerName)
		}
		return err
	}
	if state == authfile.CommitNoop {
		_, err = fmt.Fprintf(stdout, "no locally managed API key for provider %q\n", res.providerName)
	} else {
		_, err = fmt.Fprintf(stdout, "removed locally managed API key for provider %q\n", res.providerName)
	}
	return err
}

// Only fixed, reader-owned errors may cross the secret-input boundary verbatim.
func (c providerCommands) providerKeyReadError(err error, stderr io.Writer) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return c.credentialCancellation(stderr)
	}
	var safeError providerTerminalError
	if errors.As(err, &safeError) {
		return safeError
	}
	return errors.New("providers login: could not read API key from the local terminal")
}

func validateProviderAPIKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("providers login: API key cannot be empty")
	}
	if !utf8.ValidString(key) || strings.ContainsFunc(key, func(r rune) bool { return !unicode.IsPrint(r) || isTrustControl(r) }) {
		return errors.New("providers login: API key contains unsupported control characters or invalid text")
	}
	return nil
}

func (c providerCommands) runOIDC(ctx context.Context, res invocationResolution, definition permconfig.ProviderDefinition, stdout, stderr io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, nativeLLMEnrollmentTimeout)
	defer cancel()
	noBrowser := len(res.remaining) == 1 && res.remaining[0] == "--no-browser"
	rootWasAbsent := false
	if res.providerAction == providerActionLogin && c.backend.prepareOIDCRoot != nil {
		if definition.Auth.OIDC == nil || definition.Auth.OIDC.CredentialStore == nil {
			return providerOIDCLifecycleError(res.providerAction, res.providerName, errors.New("OIDC credential store is not configured"))
		}
		_, statErr := os.Lstat(definition.Auth.OIDC.CredentialStore.Home)
		rootWasAbsent = errors.Is(statErr, os.ErrNotExist)
		if err := c.backend.prepareOIDCRoot(definition.Auth.OIDC.CredentialStore.Home); err != nil {
			return providerOIDCLifecycleError(res.providerAction, res.providerName, err)
		}
	}
	runtime, err := c.backend.openOIDCRuntime(ctx, definition, noBrowser, stderr)
	if err == nil {
		defer func() { _ = runtime.Close() }()
		if res.providerAction == providerActionLogin {
			err = runtime.Login(ctx)
		} else {
			err = runtime.Logout(ctx)
		}
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			message := "Cancelled; OIDC enrollment did not complete. Inspect `mecatui providers status` before retrying; local and remote credential state is not asserted unchanged."
			if res.providerAction != providerActionLogin {
				message = "Cancelled; OIDC logout did not complete. Inspect `mecatui providers status` before retrying."
			}
			if rootWasAbsent {
				message += " A local credential directory may have been created; it was not removed."
			}
			if _, writeErr := fmt.Fprintln(stderr, message); writeErr != nil {
				return writeErr
			}
			return errProviderCredentialCancelled
		}
		return providerOIDCLifecycleError(res.providerAction, res.providerName, err)
	}
	if res.providerAction == providerActionLogin {
		_, err = fmt.Fprintf(stdout, "OIDC login successful for provider %q\n", res.providerName)
	} else {
		_, err = fmt.Fprintf(stdout, "removed locally managed OIDC credentials for provider %q\n", res.providerName)
	}
	return err
}

func writeProviderKeyGuidance(out io.Writer, provider string) error {
	guidance := "Custom provider: obtain an API key from your operator or service documentation; use the configured transport, not a guessed console."
	if stock, ok := stockAPIKeyProviderFor(provider); ok {
		guidance = stock.guidance
	}
	_, err := fmt.Fprintln(out, guidance+"\nAPI use may incur charges; check the service's billing terms. No browser is opened.\nSaving is optional and requires separate consent. The configured credential_store.api_key.file is owner-only plaintext, readable by same-UID processes, including permitted agent Shell commands. Never paste a key into a command argument.")
	return err
}

func (c providerCommands) confirmProviderAction(ctx context.Context, prompt string) (bool, error) {
	value, err := c.terminal.readField(ctx, prompt)
	if errors.Is(err, io.EOF) {
		err = context.Canceled
	}
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(value), "y") || strings.EqualFold(strings.TrimSpace(value), "yes"), nil
}

func (c providerCommands) credentialCancellation(stderr io.Writer) error {
	if c.deferCredentialCancellation {
		return errProviderCredentialCancelled
	}
	return providerCredentialCancellation(stderr)
}

func providerCredentialCancellation(stderr io.Writer) error {
	_, writeErr := fmt.Fprintln(stderr, "Cancelled; no changes made.")
	if writeErr != nil {
		return writeErr
	}
	return errProviderCredentialCancelled
}

func (c providerCommands) runToolHiveCredential(ctx context.Context, res invocationResolution, stderr io.Writer) error {
	if res.providerAction != providerActionLogin {
		return errors.New("ToolHive owns this provider lifecycle; use `thv llm` tooling")
	}
	ctx, cancel := context.WithTimeout(ctx, nativeLLMEnrollmentTimeout)
	defer cancel()
	if err := c.backend.toolHiveLogin(ctx, len(res.remaining) == 1 && res.remaining[0] == "--no-browser"); err != nil {
		if errors.Is(err, context.Canceled) {
			return c.credentialCancellation(stderr)
		}
		return errors.New("ToolHive LLM gateway login failed")
	}
	_, err := fmt.Fprintln(stderr, "ToolHive LLM gateway login successful")
	return err
}

func providerOIDCLifecycleError(action, provider string, err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("provider OIDC lifecycle timed out; retry and complete the browser callback within five minutes")
	case errors.Is(err, oidcclient.ErrStorage), errors.Is(err, credentialstore.ErrUnavailable), errors.Is(err, credentialstore.ErrClosed), errors.Is(err, credentialstore.ErrCorrupt):
		return errors.New("provider OIDC protected credential storage is unavailable; check credential_store.oidc.home and credential_store.oidc.key, then retry")
	case errors.Is(err, oidcclient.ErrDiscovery):
		return errors.New("provider OIDC issuer discovery failed; check issuer trust, DNS, TLS, and the exact provider configuration, then retry")
	case callbackAddressInUse(err):
		return errors.New("provider OIDC login cannot listen on localhost port 8666 because it is already in use; stop the process using port 8666, then retry")
	case errors.Is(err, oidcclient.ErrAuthorization):
		return errors.New("provider OIDC authorization was not completed; retry and complete the newest browser flow")
	case errors.Is(err, oidcclient.ErrToken):
		return errors.New("provider OIDC token was rejected; check the resource audience, scopes, and OIDC configuration, then retry login")
	case errors.Is(err, llmendpoint.ErrNotEnrolled):
		if action == providerActionLogout {
			return fmt.Errorf("provider OIDC enrollment is unavailable for logout; check the provider configuration and run `mecatui providers status %s`", provider)
		}
		return fmt.Errorf("provider is not enrolled; run `mecatui providers login %s`", provider)
	default:
		return fmt.Errorf("provider %s OIDC lifecycle failed; retry or run `mecatui providers status %s` for local state", action, provider)
	}
}

func callbackAddressInUse(err error) bool {
	var bind *oauthlogin.CallbackBindError
	return errors.As(err, &bind) && bind.Reason == oauthlogin.CallbackBindAddressInUse
}
