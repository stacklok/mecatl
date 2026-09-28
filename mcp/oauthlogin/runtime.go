// Package oauthlogin provides an opt-in, host-side OAuth loopback login runtime.
package oauthlogin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	callbackPrefix  = "/oauth/callback/"
	callbackBytes   = 32
	shutdownTimeout = time.Second

	// ExactRedirectURL is the fixed callback URI used by remote mecatui login.
	// It is deliberately IPv4-literal and must not be changed to localhost or
	// a wildcard address.
	ExactRedirectURL = "http://127.0.0.1:18473/oauth/callback"

	// ToolHiveCompatibleRedirectURL is the fixed callback URI used by native
	// LLM login so an existing ToolHive-compatible client registration works.
	ToolHiveCompatibleRedirectURL = "http://localhost:8666/callback"

	// CodexRedirectURL is the fixed callback URI accepted for OpenAI Codex
	// subscription login. OpenAI validates the redirect against an exact
	// registered string, so neither the host spelling, the port, nor the path
	// may change: 127.0.0.1 is a different string to the provider and is
	// rejected. Because the host is a name, the listener must cover every
	// loopback address family the browser may resolve it to.
	CodexRedirectURL = "http://localhost:1455/auth/callback"

	// AnthropicRedirectURL is the fixed callback URI accepted for Anthropic
	// subscription login. The host is the NAME "localhost", matching the
	// first-party client exactly: redirect matching is an exact string
	// comparison, and the IPv4 literal is a different string that the
	// provider rejects with "Invalid request format". Because the host is a
	// name, the listener must cover every loopback family it can resolve to.
	AnthropicRedirectURL = "http://localhost:54545/callback" //nolint:gosec // G101 false-positive on a loopback callback URL; not a credential
)

var (
	// ErrAuthorizationFailed reports a redacted failure of the authorization interaction.
	ErrAuthorizationFailed = errors.New("OAuth authorization failed")
	// ErrCallbackAttempts reports exhaustion of the callback request budget.
	ErrCallbackAttempts = errors.New("OAuth callback request limit exceeded")
)

// CallbackBindReason is the closed, safe reason a callback listener could not bind.
type CallbackBindReason uint8

const (
	// CallbackBindUnavailable reports a bind failure with no safely actionable detail.
	CallbackBindUnavailable CallbackBindReason = iota
	// CallbackBindAddressInUse reports that another process owns the callback address.
	CallbackBindAddressInUse
)

// CallbackBindError reports a callback listener bind failure without retaining the
// operating-system error, address, or other nested network data.
type CallbackBindError struct{ Reason CallbackBindReason }

func (e *CallbackBindError) Error() string {
	if e != nil && e.Reason == CallbackBindAddressInUse {
		return "OAuth callback listener address is already in use"
	}
	return "OAuth callback listener is unavailable"
}

// Is keeps callback bind failures in the authorization-failure category.
func (*CallbackBindError) Is(target error) bool { return target == ErrAuthorizationFailed }

// BrowserLauncher opens an authorization URL according to host policy.
type BrowserLauncher interface {
	Open(context.Context, string) error
}

// Options configures a Runtime.
type Options struct {
	NoBrowser bool
	URLWriter io.Writer
	Launcher  BrowserLauncher

	// RedirectURL enables an explicitly configured callback. Only the package's
	// fixed redirect constants are accepted; empty preserves the random-path,
	// ephemeral-port behavior used by existing callers.
	RedirectURL string
}

// Result is the validated loopback authorization response.
type Result struct {
	Code  string
	State string
	Iss   string
}

// AuthorizeFunc performs the controller-owned OAuth operation. It may call present once.
type AuthorizeFunc func(ctx context.Context, redirectURL string, present func(context.Context, string) (Result, error)) error

type listenFunc func(context.Context, string, string) (net.Listener, error)

// Runtime serializes complete loopback authorization interactions.
type Runtime struct {
	gate     chan struct{}
	opts     Options
	launcher BrowserLauncher
	listen   listenFunc
	random   io.Reader
}

// New constructs an opt-in loopback runtime.
func New(opts Options) (*Runtime, error) {
	if opts.NoBrowser && opts.URLWriter == nil {
		return nil, errors.New("no-browser OAuth login requires a URL writer")
	}
	if opts.RedirectURL != "" {
		if _, ok := fixedRedirect(opts.RedirectURL); !ok {
			return nil, errors.New("OAuth redirect URL is invalid")
		}
	}
	launcher := opts.Launcher
	if launcher == nil {
		launcher = systemBrowserLauncher{}
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &Runtime{
		gate:     gate,
		opts:     opts,
		launcher: launcher,
		listen:   (&net.ListenConfig{}).Listen,
		random:   rand.Reader,
	}, nil
}

// Authorize runs one loopback authorization interaction. Calls on the same Runtime are serialized.
func (r *Runtime) Authorize(ctx context.Context, expectedIssuer string, authorize AuthorizeFunc) error {
	return r.authorize(ctx, expectedIssuer, "", authorize)
}

// AuthorizeWithCallbackPath runs one loopback authorization interaction using a
// registration-bound random callback path and a fresh ephemeral IPv4 port.
func (r *Runtime) AuthorizeWithCallbackPath(ctx context.Context, expectedIssuer, callbackPath string, authorize AuthorizeFunc) error {
	if r == nil || r.opts.RedirectURL != "" || !validCallbackPath(callbackPath) {
		return errors.New("OAuth callback path is invalid")
	}
	return r.authorize(ctx, expectedIssuer, callbackPath, authorize)
}

func (r *Runtime) authorize(ctx context.Context, expectedIssuer, callbackPath string, authorize AuthorizeFunc) error { //nolint:gocyclo // callback lifecycle and cleanup states stay explicit.
	if ctx == nil {
		return errors.New("OAuth authorization requires a context")
	}
	if authorize == nil {
		return errors.New("OAuth authorization function is required")
	}
	issuer, err := canonicalIssuer(expectedIssuer)
	if err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.gate:
	}
	defer func() { r.gate <- struct{}{} }()

	if err := ctx.Err(); err != nil {
		return err
	}
	path := ""
	address := "127.0.0.1:0"
	callbackHost := ""
	redirectURL := ""
	companion := ""
	attemptPolicy := attemptMatchingRoute
	if r.opts.RedirectURL == "" {
		path = callbackPath
		if path == "" {
			path, err = randomCallbackPath(r.random)
			if err != nil {
				return errors.New("generate OAuth callback path: failed")
			}
		}
	} else {
		fixed, _ := fixedRedirect(r.opts.RedirectURL)
		path = fixed.path
		address = fixed.address
		callbackHost = fixed.host
		companion = fixed.companion
		redirectURL = r.opts.RedirectURL
		attemptPolicy = attemptFixedRoute
	}
	ln, err := r.listen(ctx, "tcp4", address)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		bindReason := CallbackBindUnavailable
		if errors.Is(err, syscall.EADDRINUSE) {
			bindReason = CallbackBindAddressInUse
		}
		return &CallbackBindError{Reason: bindReason}
	}
	listeners := []net.Listener{ln}
	if companion != "" {
		companionLn, companionErr := r.listen(ctx, "tcp6", companion)
		switch {
		case companionErr == nil:
			listeners = append(listeners, companionLn)
		case ctx.Err() != nil:
			_ = ln.Close()
			return ctx.Err()
		case errors.Is(companionErr, syscall.EADDRINUSE):
			// Another process owns the companion address, so it is positioned
			// to receive callbacks the advertised host can resolve to. Refuse
			// rather than hand the authorization code to it.
			_ = ln.Close()
			return &CallbackBindError{Reason: CallbackBindAddressInUse}
		default:
			// No usable IPv6 loopback on this host: nothing can route to the
			// companion, so the IPv4 listener alone is complete. A disabled
			// IPv6 stack is reported as a generic bind failure, which is why
			// this is not treated as a collision.
		}
	}

	host := ln.Addr().String()
	if callbackHost != "" {
		host = callbackHost
	}
	if redirectURL == "" {
		redirectURL = "http://" + host + path
	}
	flow := newCallbackFlow(path, host, issuer, attemptPolicy)
	server := &http.Server{
		Handler:           flow,
		ReadHeaderTimeout: 2 * time.Second,
		WriteTimeout:      2 * time.Second,
		IdleTimeout:       2 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	serveDone := make(chan error, len(listeners))
	for _, listener := range listeners {
		limited := newLimitedListener(listener, maxConcurrentConnections)
		go func() {
			err := server.Serve(limited)
			if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
				err = nil
			}
			if err != nil {
				flow.complete(callbackOutcome{err: errors.New("OAuth callback server failed")})
			}
			serveDone <- err
		}()
	}

	present := r.presenter(flow)

	opCtx, cancel := context.WithCancel(ctx)
	authorizeErr := authorize(opCtx, redirectURL, present)
	cancel()
	flow.complete(callbackOutcome{err: ErrAuthorizationFailed})
	cleanupErr := stopServer(server, listeners, serveDone)
	if authorizeErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return flow.annotate(ctxErr)
		}
		return safeAuthorizeError(authorizeErr)
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	return nil
}

func (r *Runtime) presenter(flow *callbackFlow) func(context.Context, string) (Result, error) {
	var mu sync.Mutex
	presented := false
	return func(ctx context.Context, authorizationURL string) (Result, error) {
		mu.Lock()
		if presented {
			mu.Unlock()
			return Result{}, errors.New("OAuth authorization URL was already presented")
		}
		presented = true
		mu.Unlock()

		state, err := validateAuthorizationURL(authorizationURL)
		if err != nil {
			return Result{}, err
		}
		if !flow.setExpectedState(state) {
			return Result{}, errors.New("OAuth authorization URL was already presented")
		}
		if r.opts.NoBrowser {
			if _, err := fmt.Fprintf(r.opts.URLWriter, "Open this OAuth authorization URL in a browser; the callback must reach this machine:\n%s\n", authorizationURL); err != nil {
				return Result{}, errors.New("write OAuth authorization URL: failed")
			}
		} else if err := r.launcher.Open(ctx, authorizationURL); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Result{}, ctxErr
			}
			return Result{}, newBrowserLaunchError()
		}

		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case outcome := <-flow.result:
			return outcome.result, outcome.err
		}
	}
}

func safeAuthorizeError(err error) error {
	var rejected *CallbackRejectedError
	var provider *AuthorizationErrorResponse
	switch {
	case errors.As(err, &rejected):
		return rejected.Sanitized()
	case errors.As(err, &provider):
		return provider.Sanitized()
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrBrowserLaunch):
		return newBrowserLaunchError()
	case errors.Is(err, ErrCallbackAttempts):
		return ErrCallbackAttempts
	default:
		return ErrAuthorizationFailed
	}
}

func randomCallbackPath(reader io.Reader) (string, error) {
	var raw [callbackBytes]byte
	if _, err := io.ReadFull(reader, raw[:]); err != nil {
		return "", err
	}
	return callbackPrefix + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

type fixedRedirectConfig struct {
	address string
	host    string
	path    string
	// companion is an additional loopback address, in another address family,
	// that must also accept the callback. It is set only when the advertised
	// host is a name that can resolve to more than one loopback literal: a
	// single-family listener would then let another process holding the other
	// family's port receive the authorization code, and because a
	// specific-address bind coexists with a wildcard bind the collision is not
	// reported at bind time.
	companion string
}

func fixedRedirect(raw string) (fixedRedirectConfig, bool) {
	switch raw {
	case ExactRedirectURL:
		return fixedRedirectConfig{address: "127.0.0.1:18473", host: "127.0.0.1:18473", path: "/oauth/callback"}, true
	case ToolHiveCompatibleRedirectURL:
		return fixedRedirectConfig{address: "localhost:8666", host: "localhost:8666", path: "/callback"}, true
	case CodexRedirectURL:
		return fixedRedirectConfig{address: "127.0.0.1:1455", host: "localhost:1455", path: "/auth/callback", companion: "[::1]:1455"}, true
	case AnthropicRedirectURL:
		return fixedRedirectConfig{address: "127.0.0.1:54545", host: "localhost:54545", path: "/callback", companion: "[::1]:54545"}, true
	default:
		return fixedRedirectConfig{}, false
	}
}

func validCallbackPath(path string) bool {
	if !strings.HasPrefix(path, callbackPrefix) {
		return false
	}
	encoded := strings.TrimPrefix(path, callbackPrefix)
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	return err == nil && len(raw) == callbackBytes && base64.RawURLEncoding.EncodeToString(raw) == encoded
}

func canonicalIssuer(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("expected OAuth issuer is invalid")
	}
	if u.String() != raw {
		return "", errors.New("expected OAuth issuer is not canonical")
	}
	return raw, nil
}

func validateAuthorizationURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("OAuth authorization URL is invalid")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["state"]) != 1 || query.Get("state") == "" || len(query.Get("state")) > maxQueryValueBytes {
		return "", errors.New("OAuth authorization URL has invalid state")
	}
	return query.Get("state"), nil
}

func stopServer(server *http.Server, listeners []net.Listener, serveDone <-chan error) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := server.Shutdown(cleanupCtx)
	for _, listener := range listeners {
		_ = listener.Close()
	}
	if shutdownErr != nil {
		_ = server.Close()
	}

	// Every serve goroutine must be accounted for; a listener left unreaped
	// would keep the port held after this call returns.
	var serveErr error
	for range listeners {
		select {
		case err := <-serveDone:
			if err != nil {
				serveErr = err
			}
		case <-cleanupCtx.Done():
			_ = server.Close()
			if err := <-serveDone; err != nil {
				serveErr = err
			}
		}
	}
	if shutdownErr != nil && !errors.Is(shutdownErr, context.DeadlineExceeded) {
		return errors.New("shutdown OAuth callback server: failed")
	}
	if serveErr != nil {
		return errors.New("OAuth callback server failed")
	}
	return nil
}
