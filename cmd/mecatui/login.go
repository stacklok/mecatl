// login.go implements the separate ToolHive LLM and remote OIDC login routes.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/toolhivellm"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

const savedLoginCallbackTimeout = 5 * time.Minute

var (
	executeRemoteLogin    = runSavedRemoteLogin
	newRemoteLoginRuntime = oauthlogin.New
	prepareSavedLogin     = prepareSavedRemoteLogin
)

type notifyContextFunc func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)

// newSavedLoginContext owns the post-TUI login lifetime. It deliberately does
// not inherit Bubble Tea's already-cancelled context.
func newSavedLoginContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	return newSavedLoginContextWithNotifier(timeout, signal.NotifyContext)
}

func newSavedLoginContextWithNotifier(timeout time.Duration, notify notifyContextFunc) (context.Context, context.CancelFunc) {
	parent, stop := notify(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(parent, timeout)
	return ctx, func() {
		cancel()
		stop()
	}
}

func runRemoteLogin(address string, args []string) error {
	fs := flag.NewFlagSet("mecatui login", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var issuer, clientID, audience, tlsCA string
	var noBrowser bool
	var scopes string
	var timeout time.Duration
	fs.StringVar(&issuer, "issuer", "", "HTTPS OIDC issuer")
	fs.StringVar(&clientID, "client-id", "", "public OIDC client ID")
	fs.StringVar(&audience, "audience", "", "OIDC token audience")
	fs.StringVar(&tlsCA, "tls-ca", "", "path to a PEM CA bundle for a private HTTPS issuer")
	fs.StringVar(&scopes, "scopes", "openid,profile,offline_access", "comma-separated OIDC scopes to request; offline_access is what earns a refresh token, but a provider that has not granted it to this client will refuse the whole request")
	fs.BoolVar(&noBrowser, "no-browser", false, "print the OIDC authorization URL instead of opening a browser, then wait for the loopback callback (headless/SSH use)")
	fs.DurationVar(&timeout, "callback-timeout", 5*time.Minute, "maximum time to wait for the loopback OAuth callback")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mecatui login ADDRESS --issuer HTTPS_URL --client-id ID --audience AUDIENCE --tls-ca PATH")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("login: unexpected arguments after ADDRESS; usage: mecatui login ADDRESS")
	}
	if issuer == "" || clientID == "" || audience == "" || tlsCA == "" || timeout <= 0 {
		return errors.New("login: --issuer, --client-id, --audience, --tls-ca, and a positive --callback-timeout are required")
	}
	issuerCAFile, err := filepath.Abs(tlsCA)
	if err != nil {
		return fmt.Errorf("login: resolve --tls-ca: %w", err)
	}
	issuerCAFile = filepath.Clean(issuerCAFile)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn := clientauth.Connection{Identity: clientauth.Identity{Target: address, Issuer: issuer, ClientID: clientID, Audience: audience, RedirectURI: oauthlogin.ExactRedirectURL, Scopes: splitScopes(scopes)}, IssuerCAFile: issuerCAFile}
	if err := executeRemoteLogin(ctx, conn, noBrowser); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "login cancelled")
			return nil
		}
		return fmt.Errorf("login: %w", err)
	}
	fmt.Fprintln(os.Stderr, "login successful")
	return nil
}

// preparedSavedLogin owns the local handles proven usable before interactive OIDC.
// Its store must remain open until enrollment completes.
type preparedSavedLogin struct {
	registry *clientauth.Registry
	creds    *clientauth.Credentials
	close    func()
}

func prepareSavedRemoteLogin(ctx context.Context, conn clientauth.Connection) (preparedSavedLogin, error) {
	root := filepath.Join(xdg.ConfigHome, "mecatl")
	registry, err := clientauth.OpenRegistry(root)
	if err != nil {
		return preparedSavedLogin{}, &client.AuthError{Reason: client.AuthStorageUnavailable}
	}
	keys, err := clientauth.NewKeyringProvider(root)
	if err != nil {
		return preparedSavedLogin{}, &client.AuthError{Reason: client.AuthStorageUnavailable}
	}
	store, err := clientauth.OpenStore(ctx, root, keys)
	if err != nil {
		return preparedSavedLogin{}, &client.AuthError{Reason: client.AuthStorageUnavailable}
	}
	closeStore := func() { _ = store.Close() }
	creds, err := clientauth.NewCredentials(store)
	if err != nil {
		closeStore()
		return preparedSavedLogin{}, &client.AuthError{Reason: client.AuthStorageUnavailable}
	}
	if _, err := creds.Load(ctx, conn.Identity); err != nil &&
		!errors.Is(err, credentialstore.ErrNotFound) && !errors.Is(err, clientauth.ErrCorrupt) {
		closeStore()
		return preparedSavedLogin{}, &client.AuthError{Reason: client.AuthStorageUnavailable}
	}
	return preparedSavedLogin{registry: registry, creds: creds, close: closeStore}, nil
}

// runSavedRemoteLogin performs the ordinary OIDC flow for an already-saved public
// target. It runs only after Bubble Tea has exited; neither the UI nor its restart
// intent receives OAuth material.
func runSavedRemoteLogin(ctx context.Context, conn clientauth.Connection, noBrowser bool) error {
	ca, err := os.ReadFile(conn.IssuerCAFile)
	if err != nil {
		return &client.AuthError{Reason: client.AuthStorageUnavailable}
	}
	prepared, err := prepareSavedLogin(ctx, conn)
	if err != nil {
		return err
	}
	defer prepared.close()

	opts := oauthlogin.Options{RedirectURL: oauthlogin.ExactRedirectURL}
	if noBrowser {
		opts.NoBrowser = true
		opts.URLWriter = os.Stderr
	}
	runtime, err := newRemoteLoginRuntime(opts)
	if err != nil {
		return fmt.Errorf("remote login unavailable: %w", err)
	}
	presenter := clientauth.PresenterFunc(func(ctx context.Context, authorizationURL string) (oauthlogin.Result, error) {
		var result oauthlogin.Result
		err := runtime.Authorize(ctx, conn.Identity.Issuer, func(ctx context.Context, _ string, present func(context.Context, string) (oauthlogin.Result, error)) error {
			var err error
			result, err = present(ctx, authorizationURL)
			return err
		})
		return result, err
	})
	token, err := clientauth.Login(ctx, clientauth.LoginConfig{Identity: conn.Identity, Presenter: presenter, PrivateHTTPS: true, TrustedCAPEM: ca})
	if err != nil {
		if errors.Is(err, clientauth.ErrDiscovery) {
			return &client.AuthError{Reason: client.AuthStorageUnavailable}
		}
		return signinError(err)
	}
	if err := clientauth.Enroll(ctx, conn, token, clientauth.EnrollmentConfig{Registry: prepared.registry, Credentials: prepared.creds}); err != nil {
		return &client.AuthError{Reason: client.AuthStorageUnavailable}
	}
	return nil
}

// splitScopes parses the --scopes list. An empty entry is dropped rather than
// sent as a zero-length scope, which providers reject.
func splitScopes(raw string) []string {
	parts := strings.Split(raw, ",")
	scopes := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			scopes = append(scopes, p)
		}
	}
	return scopes
}

// signinError reports the failing stage and, where the cause is a validation
// rule rather than provider-supplied text, the rule itself. No access token,
// refresh token, authorization code, or state value reaches this path.
func signinError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("remote sign-in timed out waiting for the OAuth callback: %w", err)
	case errors.Is(err, clientauth.ErrDiscovery),
		errors.Is(err, clientauth.ErrAuthorization),
		errors.Is(err, clientauth.ErrTokenExchange):
		return err
	}
	return errors.New("remote sign-in failed")
}

func runLogin(args []string) error {
	fs := flag.NewFlagSet("mecatui llm login", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var skipBrowser bool
	fs.BoolVar(&skipBrowser, "skip-browser", false, "print the OIDC authorization URL instead of opening a browser, then wait for the callback (headless/SSH/CI use)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := toolhivellm.RunInteractiveLogin(ctx, "", skipBrowser, nil)
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "login cancelled")
		return nil
	}
	return err
}
