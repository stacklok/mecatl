package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/internal/adapter/anthropicsub"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/internal/adapter/openaicodex"
	"github.com/stacklok/mecatl/internal/adapter/subcred"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

// subscriptionProviders are the providers whose credential is a plan
// entitlement obtained by signing in, not an API key.
var subscriptionProviders = map[string]string{
	subcred.ProviderAnthropic:   "Claude Pro/Max",
	subcred.ProviderOpenAICodex: "ChatGPT Plus/Pro",
}

// isSubscriptionProvider reports whether a provider supports a subscription login.
func isSubscriptionProvider(provider string) bool {
	_, ok := subscriptionProviders[provider]
	return ok
}

// parseSubscriptionFlags reads the subscription modifiers from the remaining
// argv. It reports whether the subscription path was selected: Codex has no
// API-key alternative so it is implicit there, while Anthropic requires an
// explicit --subscription so an existing API-key login keeps working.
func parseSubscriptionFlags(res invocationResolution) (subscriptionLoginOptions, bool, error) {
	opts := subscriptionLoginOptions{}
	explicit := false
	for _, arg := range res.remaining {
		switch arg {
		case "--subscription":
			explicit = true
		case "--no-browser":
			opts.noBrowser = true
		case "--device":
			opts.device = true
		default:
			return subscriptionLoginOptions{}, false, fmt.Errorf("providers %s: unknown flag %q", res.providerAction, arg)
		}
	}
	if opts.device && opts.noBrowser {
		return subscriptionLoginOptions{}, false, errors.New("providers login: --device and --no-browser are mutually exclusive")
	}

	selected := explicit || res.providerName == subcred.ProviderOpenAICodex
	if !selected && (opts.noBrowser || opts.device) {
		return subscriptionLoginOptions{}, false, errors.New("providers login: --no-browser and --device require --subscription")
	}
	return opts, selected, nil
}

// subscriptionLoginOptions are the parsed modifiers of a subscription login.
type subscriptionLoginOptions struct {
	noBrowser bool
	device    bool
}

// openSubscriptionStore opens the host's credential store for subscription
// grants. It reuses the same custody the remote login uses, so a subscription
// grant is protected exactly like a saved session credential.
func openSubscriptionStore(ctx context.Context) (*subcred.Store, func(), error) {
	root := filepath.Join(xdg.ConfigHome, "mecatl")
	selection, err := clientauth.ResolveCredentialStore(ctx, root, clientauth.CredentialStoreAuto)
	if err != nil {
		return nil, nil, err
	}
	backing, err := clientauth.OpenCredentialStore(ctx, root, selection.Backend)
	if err != nil {
		return nil, nil, err
	}
	store, err := subcred.New(backing)
	if err != nil {
		_ = backing.Close()
		return nil, nil, err
	}
	return store, func() { _ = backing.Close() }, nil
}

// runSubscriptionLogin performs the provider's sign-in flow and persists the
// grant. The grant, not an API key, is what the provider's transport carries.
func runSubscriptionLogin(ctx context.Context, provider string, opts subscriptionLoginOptions, shadowedBy string, stdout, stderr io.Writer) error {
	store, closeStore, err := openSubscriptionStore(ctx)
	if err != nil {
		return fmt.Errorf("providers login: open credential storage: %w", err)
	}
	defer closeStore()

	// Stated before the browser opens: an operator should learn the sign-in
	// will be shadowed before spending time completing it, not afterwards.
	if err := warnSubscriptionShadowed(shadowedBy, stderr); err != nil {
		return err
	}

	switch provider {
	case subcred.ProviderOpenAICodex:
		return loginOpenAICodexSubscription(ctx, store, opts, stdout, stderr)
	case subcred.ProviderAnthropic:
		if opts.device {
			return errors.New("providers login: --device is available only for openai-codex")
		}
		return loginAnthropicSubscription(ctx, store, opts, stdout, stderr)
	default:
		return fmt.Errorf("provider %q has no subscription login", provider)
	}
}

func loginOpenAICodexSubscription(ctx context.Context, store *subcred.Store, opts subscriptionLoginOptions, stdout, stderr io.Writer) error {
	var tokens openaicodex.OAuthTokens
	var err error
	if opts.device {
		tokens, err = openaicodex.LoginDevice(ctx, openaicodex.DeviceOptions{
			Prompt: func(verificationURL, userCode string) error {
				_, writeErr := fmt.Fprintf(stderr,
					"Open %s in a browser and enter the code %s\nWaiting for authorization...\n",
					verificationURL, userCode)
				return writeErr
			},
		})
	} else {
		tokens, err = openaicodex.Login(ctx, openaicodex.LoginOptions{
			NoBrowser: opts.noBrowser,
			URLWriter: stderr,
		})
	}
	if err != nil {
		return subscriptionLoginError(subcred.ProviderOpenAICodex, err)
	}
	if err := store.Save(ctx, subcred.FromCodex(tokens)); err != nil {
		return fmt.Errorf("providers login: store subscription grant: %w", err)
	}
	_, _ = fmt.Fprintf(stdout, "Signed in to %s (account %s).\n",
		subscriptionProviders[subcred.ProviderOpenAICodex], shortAccount(tokens.AccountID))
	return nil
}

func loginAnthropicSubscription(ctx context.Context, store *subcred.Store, opts subscriptionLoginOptions, stdout, stderr io.Writer) error {
	tokens, err := anthropicsub.Login(ctx, anthropicsub.LoginOptions{
		NoBrowser: opts.noBrowser,
		URLWriter: stderr,
	})
	if err != nil {
		return subscriptionLoginError(subcred.ProviderAnthropic, err)
	}
	if err := store.Save(ctx, subcred.FromAnthropic(tokens)); err != nil {
		return fmt.Errorf("providers login: store subscription grant: %w", err)
	}

	account := tokens.Email
	if account == "" {
		account = shortAccount(tokens.AccountID)
	}
	_, _ = fmt.Fprintf(stdout, "Signed in to %s (%s).\n",
		subscriptionProviders[subcred.ProviderAnthropic], account)
	// The grant family expires about a month after this login regardless of
	// refresh, so the deadline is stated now rather than surfacing later as an
	// unexplained refusal.
	if deadline := tokens.GrantExpiresAt(); !deadline.IsZero() {
		_, _ = fmt.Fprintf(stdout,
			"This sign-in expires on %s; refresh cannot extend it and you will need to sign in again.\n",
			deadline.Format(time.DateOnly))
	}
	return nil
}

// runSubscriptionLogout removes a stored grant. It is idempotent so a repeated
// logout is not an error.
func runSubscriptionLogout(ctx context.Context, provider string, stdout io.Writer) error {
	store, closeStore, err := openSubscriptionStore(ctx)
	if err != nil {
		return fmt.Errorf("providers logout: open credential storage: %w", err)
	}
	defer closeStore()

	if err := store.Delete(ctx, provider); err != nil {
		return fmt.Errorf("providers logout: remove subscription grant: %w", err)
	}
	_, _ = fmt.Fprintf(stdout, "Removed the stored %s subscription sign-in.\n", subscriptionProviders[provider])
	return nil
}

// subscriptionLoginError maps a flow failure onto an actionable message. The
// underlying errors are already redacted by the flow packages.
func subscriptionLoginError(provider string, err error) error {
	var bind *oauthlogin.CallbackBindError
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("providers login: %s sign-in was not completed", provider)
	case errors.As(err, &bind) && bind.Reason == oauthlogin.CallbackBindAddressInUse:
		// The provider validates the redirect against an exact registered
		// port, so another process holding it cannot be worked around.
		return fmt.Errorf("providers login: the %s callback port is already in use; stop the process holding it and retry", provider)
	default:
		return fmt.Errorf("providers login: %s sign-in failed: %w", provider, err)
	}
}

// shortAccount renders an account identifier without printing it whole.
func shortAccount(accountID string) string {
	if accountID == "" {
		return "unknown account"
	}
	if len(accountID) <= 8 {
		return accountID
	}
	return accountID[:8] + "…"
}

// warnSubscriptionShadowed states plainly when a stored sign-in will not be
// used because another credential takes precedence. Without this the login
// reports success and nothing observable changes.
func warnSubscriptionShadowed(shadowedBy string, out io.Writer) error {
	if shadowedBy == "" {
		return nil
	}
	_, err := fmt.Fprintf(out,
		"Warning: %s is configured and takes precedence, so requests will continue to use it, not this sign-in.\n"+
			"Remove it to use the subscription: unset the environment variable or delete the entry from auth.yaml.\n",
		shadowedBy)
	return err
}

// subscriptionCounterpart maps an API-key provider to the subscription login
// that serves the same vendor. `anthropic` signs in under its own id with a
// flag, because it keeps an API-key path; `openai` has no subscription of its
// own and points at the separate `openai-codex` provider.
var subscriptionCounterpart = map[string]string{
	subcred.ProviderAnthropic:   subcred.ProviderAnthropic,
	subcred.ProviderOpenAICodex: subcred.ProviderOpenAICodex,
	"openai":                    subcred.ProviderOpenAICodex,
}

// writeSubscriptionHint names the subscription alternative for a provider
// whose API key is about to be read. Providers with no subscription
// counterpart print nothing.
func writeSubscriptionHint(provider string, out io.Writer) {
	counterpart, ok := subscriptionCounterpart[provider]
	if !ok {
		return
	}
	command := "mecatui providers login " + counterpart
	if counterpart == provider {
		// Same provider, so the subscription path is opt-in by flag.
		command += " --subscription"
	}
	_, _ = fmt.Fprintf(out,
		"Reading an API key for %s. To use a %s subscription instead, cancel and run:\n  %s\n",
		provider, subscriptionProviders[counterpart], command)
}
