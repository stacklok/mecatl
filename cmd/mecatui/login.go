// login.go implements the separate ToolHive LLM and remote OIDC login routes.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/toolhivellm"
	"github.com/stacklok/mecatl/internal/cliconfig"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

const savedLoginCallbackTimeout = 5 * time.Minute

var (
	executeRemoteLogin          = runSavedRemoteLogin
	newRemoteLoginRuntime       = oauthlogin.New
	prepareSavedLogin           = prepareSavedRemoteLogin
	prepareExistingSavedLogin   = prepareExistingSavedRemoteLogin
	discoverRemoteResource      = discoverWithPublicBootstrap
	confirmDiscoveredEnrollment = confirmDiscoveredLogin
)

const defaultOIDCScopes = "openid,profile,offline_access"

type discoveredEnrollment struct {
	Resource    string
	MetadataURL string
	Connection  clientauth.Connection
}

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
	var issuer, clientID, audience, tlsCA, grpcTarget string
	var privateIssuer bool
	var noBrowser bool
	var scopes string
	var timeout time.Duration
	fs.StringVar(&issuer, "issuer", "", "HTTPS OIDC issuer")
	fs.StringVar(&clientID, "client-id", "", "public OIDC client ID")
	fs.StringVar(&audience, "audience", "", "OIDC token audience")
	fs.StringVar(&tlsCA, "tls-ca", "", "path to a PEM CA bundle for issuer verification (replaces system roots in public mode)")
	fs.StringVar(&grpcTarget, "grpc-target", "", "gRPC transport target for protected-resource discovery")
	fs.BoolVar(&privateIssuer, "private-issuer", false, "allow only private issuer addresses; requires --tls-ca")
	fs.StringVar(&scopes, "scopes", defaultOIDCScopes, "comma-separated OIDC scopes to request; overrides advertised profile scopes")
	fs.BoolVar(&noBrowser, "no-browser", false, "print the OIDC authorization URL instead of opening a browser, then wait for the loopback callback (headless/SSH use)")
	fs.DurationVar(&timeout, "callback-timeout", 5*time.Minute, "maximum time to wait for the loopback OAuth callback")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mecatui login ADDRESS [--issuer HTTPS_URL --client-id ID --audience AUDIENCE] [--grpc-target HOST:PORT]")
		cliconfig.PrintDefaults(fs.Output(), fs)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("login: unexpected arguments after ADDRESS; usage: mecatui login ADDRESS")
	}
	if timeout <= 0 {
		return errors.New("login: a positive --callback-timeout is required")
	}

	explicitIdentity := issuer != "" || clientID != "" || audience != ""
	if !explicitIdentity {
		if tlsCA != "" || privateIssuer {
			return errors.New("login: --tls-ca and --private-issuer require explicit --issuer, --client-id, and --audience")
		}
		resource, err := parseProtectedResource(address)
		if err != nil {
			return errors.New("login: --issuer, --client-id, and --audience are required for a non-resource address")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		discovered, err := discoverRemoteResource(ctx, resource)
		if err != nil {
			return errors.New("login: protected-resource discovery failed")
		}
		scopes, err := discoveredScopes(discovered, scopes, flagWasSet(fs, "scopes"))
		if err != nil {
			return errors.New("login: invalid --scopes")
		}
		enrollment, err := discoveredEnrollmentFrom(discovered, grpcTarget, strings.Join(scopes, ","))
		if err != nil {
			return errors.New("login: protected-resource discovery returned an invalid enrollment profile")
		}
		confirmed, err := confirmDiscoveredEnrollment(os.Stdin, os.Stderr, enrollment)
		if err != nil {
			return fmt.Errorf("login: confirmation failed: %w", err)
		}
		if !confirmed {
			return errors.New("login: discovered enrollment was not confirmed")
		}
		if err := executeRemoteLogin(ctx, enrollment.Connection, noBrowser); err != nil {
			if errors.Is(err, context.Canceled) {
				fmt.Fprintln(os.Stderr, "login cancelled")
				return nil
			}
			return fmt.Errorf("login: %w", err)
		}
		fmt.Fprintln(os.Stderr, "login successful")
		return nil
	}
	if issuer == "" || clientID == "" || audience == "" {
		return errors.New("login: --issuer, --client-id, and --audience are required together")
	}
	if grpcTarget != "" {
		return errors.New("login: --grpc-target is only valid with protected-resource discovery")
	}
	if privateIssuer && tlsCA == "" {
		return errors.New("login: --private-issuer requires --tls-ca")
	}
	issuerCAFile := ""
	if tlsCA != "" {
		var err error
		issuerCAFile, err = filepath.Abs(tlsCA)
		if err != nil {
			return fmt.Errorf("login: resolve --tls-ca: %w", err)
		}
		issuerCAFile = filepath.Clean(issuerCAFile)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	policy := clientauth.IssuerAddressPolicyPublic
	if privateIssuer {
		policy = clientauth.IssuerAddressPolicyPrivate
	}
	conn := clientauth.Connection{Identity: clientauth.Identity{Target: address, Issuer: issuer, ClientID: clientID, Audience: audience, RedirectURI: oauthlogin.ExactRedirectURL, Scopes: splitScopes(scopes)}, IssuerCAFile: issuerCAFile, IssuerAddressPolicy: policy}
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

func discoverWithPublicBootstrap(ctx context.Context, resource protectedResource) (discoveredResource, error) {
	return discoverProtectedResource(ctx, resource, nil)
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

func discoveredScopes(discovered discoveredResource, explicit string, explicitSet bool) ([]string, error) {
	raw := discovered.Scopes
	if explicitSet {
		raw = splitScopes(explicit)
	}
	scopes := make(map[string]bool, len(raw)+3)
	for _, scope := range append(splitScopes(defaultOIDCScopes), raw...) {
		if !validScope(scope) {
			return nil, errDiscoveryRejected
		}
		scopes[scope] = true
	}
	result := make([]string, 0, len(scopes))
	for scope := range scopes {
		result = append(result, scope)
	}
	slices.Sort(result)
	return result, nil
}

func discoveredEnrollmentFrom(discovered discoveredResource, grpcTarget, scopes string) (discoveredEnrollment, error) {
	target := discovered.GRPCTarget
	if grpcTarget != "" {
		target = grpcTarget
	}
	if scopes == "" {
		scopes = defaultOIDCScopes
	}
	identity := clientauth.Identity{Target: target, Issuer: discovered.Issuer, ClientID: discovered.ClientID, Audience: discovered.Audience, RedirectURI: oauthlogin.ExactRedirectURL, Scopes: splitScopes(scopes)}
	identity, err := identity.Canonical()
	if err != nil {
		return discoveredEnrollment{}, err
	}
	return discoveredEnrollment{Resource: discovered.Resource, MetadataURL: discovered.MetadataURL, Connection: clientauth.Connection{Identity: identity, ResourceURL: discovered.Resource, IssuerAddressPolicy: clientauth.IssuerAddressPolicyPublic}}, nil
}

func confirmDiscoveredLogin(in io.Reader, out io.Writer, enrollment discoveredEnrollment) (bool, error) {
	fmt.Fprintf(out, "Discovered protected resource:\n  resource: %s\n  metadata: %s\n  issuer: %s\n  audience: %s\n  client ID: %s\n  scopes: %s\n  gRPC target: %s\nContinue with browser login? [y/N]: ", enrollment.Resource, enrollment.MetadataURL, enrollment.Connection.Identity.Issuer, enrollment.Connection.Identity.Audience, enrollment.Connection.Identity.ClientID, strings.Join(enrollment.Connection.Identity.Scopes, ","), enrollment.Connection.Identity.Target)
	var answer string
	if _, err := fmt.Fscanln(in, &answer); err != nil {
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		return false, err
	}
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
}

// preparedSavedLogin owns the local handles proven usable before interactive OIDC.
// Its store must remain open until enrollment completes.
type preparedSavedLogin struct {
	registry *clientauth.Registry
	creds    *clientauth.Credentials
	close    func()
	// expectedTarget, when non-nil, is the target's connection snapshot taken
	// HERE, before the interactive browser OAuth exchange runs (which can
	// take arbitrarily long). It is threaded into Enroll as
	// EnrollmentConfig.ExpectedTarget so a concurrent logout for the same
	// target during that wait is detected rather than silently resurrected.
	// nil for a fresh enrollment, which has no prior state to preserve.
	expectedTarget *[]clientauth.Connection
	// expectedCredential is the SAME identity's own credential snapshot taken
	// at the same preflight time -- see EnrollmentConfig.ExpectedCredential;
	// expectedTarget alone cannot detect a newer credential enrolled for the
	// identical identity.
	expectedCredential *clientauth.ExpectedCredentialState
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

// prepareExistingSavedRemoteLogin opens only existing saved-target state. It is
// used by TUI reauthentication so a broken or removed local store is recovered
// in the UI rather than starting an enrollment browser flow.
func prepareExistingSavedRemoteLogin(ctx context.Context, conn clientauth.Connection) (preparedSavedLogin, error) {
	root := filepath.Join(xdg.ConfigHome, "mecatl")
	registry, err := clientauth.OpenExistingRegistry(root)
	if err != nil {
		return preparedSavedLogin{}, &client.AuthError{Reason: client.AuthStorageUnavailable}
	}
	keys, err := clientauth.NewExistingKeyringProvider(root)
	if err != nil {
		return preparedSavedLogin{}, &client.AuthError{Reason: client.AuthStorageUnavailable}
	}
	store, err := clientauth.OpenExistingStore(ctx, root, keys)
	if err != nil {
		return preparedSavedLogin{}, &client.AuthError{Reason: client.AuthStorageUnavailable}
	}
	closeStore := func() { _ = store.Close() }
	creds, err := clientauth.NewCredentials(store)
	if err != nil {
		closeStore()
		return preparedSavedLogin{}, &client.AuthError{Reason: client.AuthStorageUnavailable}
	}
	rec, loadErr := creds.Load(ctx, conn.Identity)
	var expectedCredential *clientauth.ExpectedCredentialState
	switch {
	case loadErr == nil:
		expectedCredential = &clientauth.ExpectedCredentialState{Found: true, Version: rec.Version}
	case errors.Is(loadErr, credentialstore.ErrNotFound):
		expectedCredential = &clientauth.ExpectedCredentialState{Found: false}
	case errors.Is(loadErr, clientauth.ErrCorrupt):
		// Enroll's corrupt-record repair path may still run, but only if the
		// record is STILL corrupt at commit time -- if another process
		// repaired or replaced it while this sign-in's browser flow was
		// open, this stale sign-in must not overwrite that.
		expectedCredential = &clientauth.ExpectedCredentialState{Corrupt: true}
	default:
		closeStore()
		return preparedSavedLogin{}, &client.AuthError{Reason: client.AuthStorageUnavailable}
	}
	all, err := registry.List()
	if err != nil {
		closeStore()
		return preparedSavedLogin{}, &client.AuthError{Reason: client.AuthStorageUnavailable}
	}
	var expected []clientauth.Connection
	// Canonicalize defensively (matching Enroll's own discipline) rather than
	// trust the caller's conn.Identity.Target is already canonical -- a
	// mismatch here would silently see zero existing entries and misreport
	// every reauth as a changed target.
	target := conn.Identity.Target
	if canon, canonErr := conn.Identity.Canonical(); canonErr == nil {
		target = canon.Target
	}
	for _, existing := range all {
		if existing.Identity.Target == target {
			expected = append(expected, existing)
		}
	}
	return preparedSavedLogin{registry: registry, creds: creds, close: closeStore, expectedTarget: &expected, expectedCredential: expectedCredential}, nil
}

// runSavedRemoteLogin performs the ordinary OIDC flow for an already-saved public
// target. It runs only after Bubble Tea has exited; neither the UI nor its restart
// intent receives OAuth material.
func runSavedRemoteLogin(ctx context.Context, conn clientauth.Connection, noBrowser bool) error {
	return runSavedRemoteLoginWith(ctx, conn, noBrowser, prepareSavedLogin)
}

// runExistingSavedRemoteLogin reauthenticates a saved target without initializing
// replacement local storage.
func runExistingSavedRemoteLogin(ctx context.Context, conn clientauth.Connection, noBrowser bool) error {
	return runSavedRemoteLoginWith(ctx, conn, noBrowser, prepareExistingSavedLogin)
}

func runSavedRemoteLoginWith(ctx context.Context, conn clientauth.Connection, noBrowser bool, prepare func(context.Context, clientauth.Connection) (preparedSavedLogin, error)) error {
	var ca []byte
	if conn.IssuerCAFile != "" {
		var err error
		ca, err = os.ReadFile(conn.IssuerCAFile)
		if err != nil {
			return &client.AuthError{Reason: client.AuthStorageUnavailable}
		}
	}
	prepared, err := prepare(ctx, conn)
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
	token, err := clientauth.Login(ctx, clientauth.LoginConfig{Identity: conn.Identity, Presenter: presenter, IssuerAddressPolicy: conn.IssuerAddressPolicy, TrustedCAPEM: ca})
	if err != nil {
		return signinError(err)
	}
	if err := clientauth.Enroll(ctx, conn, token, clientauth.EnrollmentConfig{Registry: prepared.registry, Credentials: prepared.creds, ExpectedTarget: prepared.expectedTarget, ExpectedCredential: prepared.expectedCredential}); err != nil {
		if errors.Is(err, clientauth.ErrTargetChanged) {
			return &client.AuthError{Reason: client.AuthTargetChanged}
		}
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
