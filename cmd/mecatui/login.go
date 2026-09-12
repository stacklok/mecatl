// login.go implements the separate ToolHive LLM and remote OIDC login routes.
package main

import (
	"bufio"
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
	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/adapter/oidcclient"
	"github.com/stacklok/mecatl/internal/adapter/toolhivellm"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/flaghelp"
	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

const (
	savedLoginCallbackTimeout  = 5 * time.Minute
	nativeLLMEnrollmentTimeout = 5 * time.Minute
)

var (
	executeRemoteLogin          = runSelectedRemoteLogin
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
func newNativeLLMEnrollmentContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	return newSavedLoginContext(timeout)
}

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
	var scopes, credentialStore string
	var timeout time.Duration
	fs.StringVar(&credentialStore, "credential-store", "auto", "credential backend: auto, keyring, or file (pinned per local store root)")
	fs.StringVar(&issuer, "issuer", "", "HTTPS OIDC issuer")
	fs.StringVar(&clientID, "client-id", "", "public OIDC client ID")
	fs.StringVar(&audience, "audience", "", "OIDC token audience")
	fs.StringVar(&tlsCA, "tls-ca", "", "path to a PEM CA bundle for issuer verification (replaces system roots in public mode)")
	fs.StringVar(&grpcTarget, "grpc-target", "", "gRPC transport target for protected-resource discovery")
	fs.BoolVar(&privateIssuer, "private-issuer", false, "allow only private issuer addresses; requires --tls-ca")
	fs.StringVar(&scopes, "scopes", defaultOIDCScopes, "comma-separated OIDC scopes to request (explicit identity login only)")
	fs.BoolVar(&noBrowser, "no-browser", false, "print the OIDC authorization URL instead of opening a browser, then wait for the loopback callback (headless/SSH use)")
	fs.DurationVar(&timeout, "callback-timeout", 5*time.Minute, "maximum time to wait for the loopback OAuth callback")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mecatui login ADDRESS [--issuer HTTPS_URL --client-id ID --audience AUDIENCE] [--grpc-target HOST:PORT]")
		flaghelp.PrintDefaults(fs.Output(), fs)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateRemoteLoginOptions(fs.NArg(), timeout, credentialStore); err != nil {
		return err
	}
	storeMode := clientauth.CredentialStoreMode(credentialStore)
	explicitIssuer := flagWasSet(fs, "issuer")
	explicitClientID := flagWasSet(fs, "client-id")
	explicitAudience := flagWasSet(fs, "audience")
	explicitIdentity := explicitIssuer || explicitClientID || explicitAudience
	if !explicitIdentity {
		if tlsCA != "" || privateIssuer {
			return errors.New("login: --tls-ca and --private-issuer require explicit --issuer, --client-id, and --audience")
		}
		return runDiscoveredRemoteLogin(address, grpcTarget, flagWasSet(fs, "scopes"), noBrowser, timeout, storeMode)
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
	if err := executeRemoteLogin(ctx, conn, noBrowser, storeMode); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "login cancelled")
			return nil
		}
		return fmt.Errorf("login: %w", err)
	}
	fmt.Fprintln(os.Stderr, "login successful")
	return nil
}

func validateRemoteLoginOptions(extra int, timeout time.Duration, credentialStore string) error {
	if extra != 0 {
		return errors.New("login: unexpected arguments after ADDRESS; usage: mecatui login ADDRESS")
	}
	if timeout <= 0 {
		return errors.New("login: a positive --callback-timeout is required")
	}
	if credentialStore != "auto" && credentialStore != "keyring" && credentialStore != "file" {
		return errors.New("login: --credential-store must be auto, keyring, or file")
	}
	return nil
}

func runDiscoveredRemoteLogin(address, grpcTarget string, scopesExplicit, noBrowser bool, timeout time.Duration, storeMode clientauth.CredentialStoreMode) error {
	if scopesExplicit {
		return errors.New("login: --scopes is only valid with explicit --issuer, --client-id, and --audience")
	}
	resource, err := parseProtectedResource(address)
	if err != nil {
		return errors.New("login: --issuer, --client-id, and --audience are required for a non-resource address")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	discoverCtx, cancelDiscover := context.WithTimeout(ctx, timeout)
	discovered, err := discoverRemoteResource(discoverCtx, resource)
	cancelDiscover()
	if err != nil {
		return errors.New("login: protected-resource discovery failed")
	}
	selectedScopes, err := discoveredScopes(discovered, scopesExplicit)
	if err != nil {
		return errors.New("login: invalid --scopes")
	}
	enrollment, err := discoveredEnrollmentFrom(discovered, grpcTarget, selectedScopes)
	if err != nil {
		return errors.New("login: protected-resource discovery returned an invalid enrollment profile")
	}
	if !savedDiscoveredEnrollmentMatches(enrollment) {
		confirmed, err := confirmDiscoveredEnrollment(os.Stdin, os.Stderr, enrollment)
		if err != nil {
			return fmt.Errorf("login: confirmation failed: %w", err)
		}
		if !confirmed {
			return errors.New("login: discovered enrollment was not confirmed")
		}
	}
	loginCtx, cancelLogin := context.WithTimeout(ctx, timeout)
	defer cancelLogin()
	if err := executeRemoteLogin(loginCtx, enrollment.Connection, noBrowser, storeMode); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "login cancelled")
			return nil
		}
		return fmt.Errorf("login: %w", err)
	}
	fmt.Fprintln(os.Stderr, "login successful")
	return nil
}

func savedDiscoveredEnrollmentMatches(enrollment discoveredEnrollment) bool {
	registry, err := clientauth.OpenExistingRegistry(filepath.Join(xdg.ConfigHome, "mecatl"))
	if err != nil {
		return false
	}
	saved, err := registry.Find(enrollment.Resource)
	if err != nil {
		return false
	}
	conn := enrollment.Connection
	return saved.Identity.Equal(conn.Identity) &&
		saved.ResourceURL == conn.ResourceURL &&
		saved.IssuerCAFile == conn.IssuerCAFile &&
		saved.IssuerAddressPolicy == conn.IssuerAddressPolicy
}

func discoverWithPublicBootstrap(ctx context.Context, resource protectedResource) (discoveredResource, error) {
	return discoverProtectedResource(ctx, resource, nil)
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

func discoveredScopes(discovered discoveredResource, explicitSet bool) ([]string, error) {
	if explicitSet {
		return nil, errDiscoveryRejected
	}
	if !discovered.ScopesPresent {
		return splitScopes(defaultOIDCScopes), nil
	}
	if len(discovered.Scopes) == 0 {
		return nil, errDiscoveryRejected
	}
	result := slices.Clone(discovered.Scopes)
	slices.Sort(result)
	for n, scope := range result {
		if !validScope(scope) || n > 0 && scope == result[n-1] {
			return nil, errDiscoveryRejected
		}
	}
	return result, nil
}

func discoveredEnrollmentFrom(discovered discoveredResource, grpcTarget string, scopes []string) (discoveredEnrollment, error) {
	target := discovered.GRPCTarget
	if grpcTarget != "" {
		target = grpcTarget
	}
	identity := clientauth.Identity{Target: target, Issuer: discovered.Issuer, ClientID: discovered.ClientID, Audience: discovered.Audience, RedirectURI: oauthlogin.ExactRedirectURL, Scopes: slices.Clone(scopes)}
	identity, err := identity.Canonical()
	if err != nil {
		return discoveredEnrollment{}, err
	}
	return discoveredEnrollment{Resource: discovered.Resource, MetadataURL: discovered.MetadataURL, Connection: clientauth.Connection{Identity: identity, ResourceURL: discovered.Resource, IssuerAddressPolicy: clientauth.IssuerAddressPolicyPublic}}, nil
}

func confirmDiscoveredLogin(in io.Reader, out io.Writer, enrollment discoveredEnrollment) (bool, error) {
	if !safeDiscoveredEnrollment(enrollment) {
		return false, errDiscoveryRejected
	}
	if _, err := fmt.Fprintf(out, "Discovered protected resource:\n  resource: %s\n  metadata: %s\n  issuer: %s\n  audience: %s\n  client ID: %s\n  scopes: %s\n  gRPC target: %s\nContinue with browser login? [y/N]: ", enrollment.Resource, enrollment.MetadataURL, enrollment.Connection.Identity.Issuer, enrollment.Connection.Identity.Audience, enrollment.Connection.Identity.ClientID, strings.Join(enrollment.Connection.Identity.Scopes, ","), enrollment.Connection.Identity.Target); err != nil {
		return false, err
	}
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer = strings.TrimSpace(answer)
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
}

func safeDiscoveredEnrollment(enrollment discoveredEnrollment) bool {
	values := []string{
		enrollment.Resource,
		enrollment.MetadataURL,
		enrollment.Connection.Identity.Issuer,
		enrollment.Connection.Identity.Audience,
		enrollment.Connection.Identity.ClientID,
		enrollment.Connection.Identity.Target,
		strings.Join(enrollment.Connection.Identity.Scopes, ","),
	}
	for _, value := range values {
		if !safeDisplayValue(value) {
			return false
		}
	}
	return true
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

func storageUnavailable(stage client.AuthStorageStage) error {
	return &client.AuthError{Reason: client.AuthStorageUnavailable, StorageStage: stage}
}

func credentialStorageUnavailable(err error) error {
	if errors.Is(err, clientauth.ErrKeyUnavailable) && !errors.Is(err, credentialstore.ErrUnavailable) {
		return storageUnavailable(client.AuthStorageKeyring)
	}
	return storageUnavailable(client.AuthStorageCredentialStore)
}

func prepareSavedRemoteLogin(ctx context.Context, conn clientauth.Connection) (preparedSavedLogin, error) {
	return prepareSelectedRemoteLogin(ctx, conn, clientauth.CredentialStoreAuto)
}

func prepareSelectedRemoteLogin(ctx context.Context, conn clientauth.Connection, mode clientauth.CredentialStoreMode) (preparedSavedLogin, error) {
	root := filepath.Join(xdg.ConfigHome, "mecatl")
	selection, err := clientauth.ResolveCredentialStore(ctx, root, mode)
	if err != nil {
		return preparedSavedLogin{}, err
	}
	if selection.NewlyPinned && selection.Backend == clientauth.CredentialBackendFile {
		fmt.Fprintln(os.Stderr, "Using file-backed credential storage (owner-only permissions).")
	}
	registry, err := clientauth.OpenRegistry(root)
	if err != nil {
		return preparedSavedLogin{}, storageUnavailable(client.AuthStorageConfigDirectory)
	}
	store, err := clientauth.OpenCredentialStore(ctx, root, selection.Backend)
	if err != nil {
		return preparedSavedLogin{}, credentialStorageUnavailable(err)
	}
	closeStore := func() { _ = store.Close() }
	creds, err := clientauth.NewCredentials(store)
	if err != nil {
		closeStore()
		return preparedSavedLogin{}, storageUnavailable(client.AuthStorageCredentialStore)
	}
	// Snapshot the target/credential state now, BEFORE the caller's interactive
	// browser wait: this route serves both a fresh enrollment (nothing to
	// snapshot yet -- Found:false / an empty target list is itself a valid CAS
	// baseline) and a rediscovery of an already-enrolled resource, and only the
	// snapshot closes the gap where a concurrent logout removes that enrollment
	// while the browser exchange is in flight, which would otherwise let
	// completion resurrect the removed row.
	expectedTarget, expectedCredential, err := snapshotSavedLoginState(ctx, conn, registry, creds)
	if err != nil {
		closeStore()
		return preparedSavedLogin{}, err
	}
	return preparedSavedLogin{registry: registry, creds: creds, close: closeStore, expectedTarget: expectedTarget, expectedCredential: expectedCredential}, nil
}

// snapshotSavedLoginState captures the CAS preconditions Enroll needs to reject
// a commit that races a concurrent logout/re-enrollment of the same target: the
// credential state for conn.Identity and every existing registry row for its
// (canonicalized) target, both read at preflight time.
func snapshotSavedLoginState(ctx context.Context, conn clientauth.Connection, registry *clientauth.Registry, creds *clientauth.Credentials) (*[]clientauth.Connection, *clientauth.ExpectedCredentialState, error) {
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
		// repaired or replaced it while this sign-in's browser flow was open,
		// this stale sign-in must not overwrite that.
		expectedCredential = &clientauth.ExpectedCredentialState{Corrupt: true}
	default:
		return nil, nil, storageUnavailable(client.AuthStorageCredentialStore)
	}
	all, err := registry.List()
	if err != nil {
		return nil, nil, storageUnavailable(client.AuthStorageRegistry)
	}
	var expected []clientauth.Connection
	// Canonicalize defensively (matching Enroll's own discipline) rather than
	// trust the caller's conn.Identity.Target is already canonical -- a
	// mismatch here would silently see zero existing entries and misreport
	// every login as a changed target.
	target := conn.Identity.Target
	if canon, canonErr := conn.Identity.Canonical(); canonErr == nil {
		target = canon.Target
	}
	for _, existing := range all {
		if existing.Identity.Target == target {
			expected = append(expected, existing)
		}
	}
	return &expected, expectedCredential, nil
}

// prepareExistingSavedRemoteLogin opens only existing saved-target state. It is
// used by TUI reauthentication so a broken or removed local store is recovered
// in the UI rather than starting an enrollment browser flow.
func prepareExistingSavedRemoteLogin(ctx context.Context, conn clientauth.Connection) (preparedSavedLogin, error) {
	root := filepath.Join(xdg.ConfigHome, "mecatl")
	registry, err := clientauth.OpenExistingRegistry(root)
	if err != nil {
		return preparedSavedLogin{}, storageUnavailable(client.AuthStorageConfigDirectory)
	}
	store, _, err := clientauth.OpenExistingCredentialStore(ctx, root)
	if err != nil {
		return preparedSavedLogin{}, credentialStorageUnavailable(err)
	}
	closeStore := func() { _ = store.Close() }
	creds, err := clientauth.NewCredentials(store)
	if err != nil {
		closeStore()
		return preparedSavedLogin{}, storageUnavailable(client.AuthStorageCredentialStore)
	}
	expectedTarget, expectedCredential, err := snapshotSavedLoginState(ctx, conn, registry, creds)
	if err != nil {
		closeStore()
		return preparedSavedLogin{}, err
	}
	return preparedSavedLogin{registry: registry, creds: creds, close: closeStore, expectedTarget: expectedTarget, expectedCredential: expectedCredential}, nil
}

// runSavedRemoteLogin performs the ordinary OIDC flow for an already-saved public
// target. It runs only after Bubble Tea has exited; neither the UI nor its restart
// intent receives OAuth material.
func runSelectedRemoteLogin(ctx context.Context, conn clientauth.Connection, noBrowser bool, mode clientauth.CredentialStoreMode) error {
	return runSavedRemoteLoginWith(ctx, conn, noBrowser, func(ctx context.Context, conn clientauth.Connection) (preparedSavedLogin, error) {
		return prepareSelectedRemoteLogin(ctx, conn, mode)
	})
}

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
			return storageUnavailable(client.AuthStorageTLSCA)
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
		return storageUnavailable(client.AuthStorageCredentialStore)
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

type nativeLLMHost interface {
	EndpointIDs() []string
	Login(context.Context, string) error
	Status(context.Context, string) llmendpoint.Status
	Logout(context.Context, string) error
	Close() error
}

var (
	executeToolHiveLogin = func(ctx context.Context, skipBrowser bool) error {
		return toolhivellm.RunInteractiveLogin(ctx, "", skipBrowser, nil)
	}
	openNativeLLMHost = newNativeLLMHost
)

type llmCommandDeps struct {
	setup      setupDeps
	openNative func(context.Context, bool, io.Writer) (nativeLLMHost, error)
}

func defaultLLMCommandDeps() llmCommandDeps {
	return llmCommandDeps{setup: defaultSetupDeps(), openNative: openNativeLLMHost}
}

func runLLMCommand(res invocationResolution) error {
	return runLLMCommandWith(res, os.Stdin, os.Stdout, os.Stderr, defaultLLMCommandDeps())
}

func runLLMCommandWith(res invocationResolution, stdin, stdout *os.File, stderr io.Writer, services llmCommandDeps) error {
	if len(res.remaining) == 1 && isHelpMetaFlag(res.remaining[0]) {
		_, _ = fmt.Fprintln(stderr, "Usage: mecatui llm setup [--auth-file PATH] | mecatui llm config set ENDPOINT [flags] | mecatui llm login ENDPOINT [--no-browser] | mecatui llm status [--auth-file PATH] [ENDPOINT] | mecatui llm logout ENDPOINT")
		_, _ = fmt.Fprintln(stderr, "Native endpoint login: --no-browser prints the authorization URL to stderr and waits at the fixed ToolHive-compatible callback http://localhost:8666/callback.")
		_, _ = fmt.Fprintln(stderr, "ToolHive login: mecatui llm login toolhive [--skip-browser]")
		return flag.ErrHelp
	}
	if res.llmDeprecatedAlias {
		_, _ = fmt.Fprintln(stderr, "WARNING: bare `mecatui llm login` is deprecated; use `mecatui llm login toolhive`")
	}
	if res.llmAction == llmActionStatus && res.llmEndpoint == "" {
		path := res.llmAuthFile
		if !res.llmAuthFileSet {
			path = authfile.DefaultPath(xdgconfig.OSEnv)
		}
		return runAggregateStatus(path, res.llmAuthFileSet, stdout, services.setup)
	}
	ctx, cancel := newNativeLLMEnrollmentContext(nativeLLMEnrollmentTimeout)
	defer cancel()
	if res.llmAction == llmActionSetup {
		path := res.llmAuthFile
		if !res.llmAuthFileSet {
			path = authfile.DefaultPath(xdgconfig.OSEnv)
		}
		deps := services.setup
		deps.nativeLogin = func(loginCtx context.Context, endpoint string) error {
			host, err := services.openNative(loginCtx, false, stderr)
			if err != nil {
				return errors.New("native LLM endpoint lifecycle is unavailable")
			}
			defer func() { _ = host.Close() }()
			return runNativeLLMCommand(loginCtx, llmActionLogin, endpoint, false, host, stdout, stderr)
		}
		deps.nativeUsable = func(statusCtx context.Context, endpoint string) (bool, error) {
			host, err := services.openNative(statusCtx, false, stderr)
			if err != nil {
				return false, errors.New("native LLM endpoint lifecycle is unavailable")
			}
			defer func() { _ = host.Close() }()
			return host.Status(statusCtx, endpoint) == llmendpoint.StatusUsable, nil
		}
		return runSetupCommand(ctx, path, res.llmAuthFileSet, stdin, stdout, deps)
	}
	skipBrowser := len(res.remaining) == 1 && res.remaining[0] == "--skip-browser"
	noBrowser := len(res.remaining) == 1 && res.remaining[0] == "--no-browser"
	if res.llmEndpoint == toolHiveEndpointID {
		return runNativeLLMCommand(ctx, res.llmAction, res.llmEndpoint, skipBrowser, nil, stdout, stderr)
	}
	host, err := services.openNative(ctx, noBrowser, stderr)
	if err != nil {
		return errors.New("native LLM endpoint lifecycle is unavailable")
	}
	defer func() { _ = host.Close() }()
	return runNativeLLMCommand(ctx, res.llmAction, res.llmEndpoint, false, host, stdout, stderr)
}

func unknownNativeEndpointError(endpoint string, ids []string) error {
	if len(ids) == 0 {
		return errors.New("unknown native LLM endpoint; no native LLM endpoints are configured in operator `llm.endpoints`; update settings and retry")
	}
	return fmt.Errorf("unknown native LLM endpoint %q; configured endpoints: %s; run `mecatui llm status` to inspect local enrollment", endpoint, strings.Join(ids, ", "))
}

func nativeLifecycleError(action, endpoint string, err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return errors.New("native LLM endpoint lifecycle cancelled; retry when ready")
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("native LLM endpoint lifecycle timed out; retry and complete the browser callback within five minutes")
	case errors.Is(err, oidcclient.ErrStorage), errors.Is(err, credentialstore.ErrUnavailable), errors.Is(err, credentialstore.ErrClosed), errors.Is(err, credentialstore.ErrCorrupt):
		return errors.New("native LLM protected credential storage is unavailable; check the configured credential home and llm.credential_key source (OS keyring or environment key), then retry")
	case errors.Is(err, oidcclient.ErrDiscovery):
		return errors.New("native LLM issuer discovery failed; check issuer trust, DNS, TLS, and exact endpoint configuration, then retry")
	case callbackAddressInUse(err):
		return errors.New("native LLM login cannot listen on localhost port 8666 because it is already in use; stop the process using port 8666, then retry")
	case errors.Is(err, oidcclient.ErrAuthorization):
		return errors.New("native LLM authorization was not completed; retry and complete the newest browser flow")
	case errors.Is(err, oidcclient.ErrToken):
		return errors.New("native LLM token was rejected; check the OIDC endpoint configuration, resource audience, and scopes, then retry login")
	case errors.Is(err, llmendpoint.ErrNotEnrolled):
		return fmt.Errorf("native LLM endpoint is not enrolled; run `mecatui llm login %s`", endpoint)
	default:
		return fmt.Errorf("native LLM endpoint %s failed; retry or run `mecatui llm status %s` for local state", action, endpoint)
	}
}

func callbackAddressInUse(err error) bool {
	var bind *oauthlogin.CallbackBindError
	return errors.As(err, &bind) && bind.Reason == oauthlogin.CallbackBindAddressInUse
}

func runNativeLLMCommand(ctx context.Context, action, endpoint string, skipBrowser bool, host nativeLLMHost, stdout, stderr io.Writer) error {
	if endpoint == toolHiveEndpointID {
		switch action {
		case llmActionLogin:
			if err := executeToolHiveLogin(ctx, skipBrowser); err != nil {
				return errors.New("ToolHive LLM gateway login failed")
			}
			_, err := fmt.Fprintln(stderr, "ToolHive LLM gateway login successful")
			return err
		case llmActionStatus, llmActionLogout:
			return errors.New("ToolHive owns this LLM gateway lifecycle; use `thv llm` tooling")
		}
	}
	if host == nil {
		return errors.New("native LLM endpoint lifecycle is unavailable")
	}
	ids := host.EndpointIDs()
	slices.Sort(ids)
	configured := slices.Contains(ids, endpoint)
	switch action {
	case llmActionLogin:
		if endpoint == "" || !configured {
			return unknownNativeEndpointError(endpoint, ids)
		}
		if err := host.Login(ctx, endpoint); err != nil {
			return nativeLifecycleError("login", endpoint, err)
		}
		_, err := fmt.Fprintln(stderr, "LLM endpoint login successful")
		return err
	case llmActionStatus:
		if endpoint != "" {
			if !configured {
				return unknownNativeEndpointError(endpoint, ids)
			}
			_, err := fmt.Fprintf(stdout, "%s\t%s\n", endpoint, host.Status(ctx, endpoint))
			return err
		}
		if len(ids) == 0 {
			return errors.New("no native LLM endpoints are configured in operator `llm.endpoints`; update settings before running status")
		}
		for _, id := range ids {
			if _, err := fmt.Fprintf(stdout, "%s\t%s\n", id, host.Status(ctx, id)); err != nil {
				return err
			}
		}
		return nil
	case llmActionLogout:
		if endpoint == "" || !configured {
			return unknownNativeEndpointError(endpoint, ids)
		}
		if err := host.Logout(ctx, endpoint); err != nil {
			return nativeLifecycleError("logout", endpoint, err)
		}
		_, err := fmt.Fprintln(stderr, "LLM endpoint logout successful")
		return err
	default:
		return errors.New("unknown native LLM endpoint lifecycle action")
	}
}
