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
	"sync"
	"syscall"
	"time"
)

const (
	callbackPrefix  = "/oauth/callback/"
	callbackBytes   = 32
	shutdownTimeout = time.Second

	// fixedCallbackPath is the well-known, pre-registerable callback path shared by
	// ExactRedirectURL (fixed path + fixed port) and Options.PinCallbackPath (fixed
	// path + ephemeral port) — see PinCallbackPath's doc comment for why a target's
	// own capabilities decide which of the two a caller should use.
	fixedCallbackPath = "/oauth/callback"

	// ExactRedirectURL is the fixed callback URI used by clients that have a
	// pre-registered redirect requiring an EXACT string match, port included (a
	// general-purpose OIDC target with no obligation to implement RFC 8252 loopback
	// dynamic-port matching). It is deliberately IPv4-literal and must not be
	// changed to localhost or a wildcard address.
	ExactRedirectURL = "http://127.0.0.1:18473" + fixedCallbackPath
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

	// RedirectURL enables an explicitly configured callback with a FIXED PORT as
	// well as a fixed path. The only accepted value is ExactRedirectURL. Use this
	// only for a target that requires an exact redirect_uri string match (a
	// general-purpose OIDC target with no obligation to implement RFC 8252 loopback
	// dynamic-port matching) — it reintroduces local port-squatting exposure that
	// PinCallbackPath does not (see its doc comment). Mutually exclusive with
	// PinCallbackPath. Empty preserves the random-path, ephemeral-port default.
	RedirectURL string

	// PinCallbackPath fixes the callback's PATH to the same well-known value
	// ExactRedirectURL uses, while still binding an EPHEMERAL port. Use this for a
	// target whose authorization server implements RFC 8252 §7.3 loopback dynamic-
	// port matching — the AS accepts any port for a registered loopback redirect_uri
	// as long as the path matches — which lets a client register one fixed
	// redirect_uri while every login still gets its own unpredictable port,
	// preserving the squatting resistance ExactRedirectURL's fixed port gives up.
	// Mutually exclusive with RedirectURL: New rejects setting both rather than
	// silently picking one.
	PinCallbackPath bool
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
	if opts.RedirectURL != "" && !isExactRedirectURL(opts.RedirectURL) {
		return nil, errors.New("OAuth redirect URL is invalid")
	}
	if opts.RedirectURL != "" && opts.PinCallbackPath {
		return nil, errors.New("OAuth redirect URL and pinned callback path are mutually exclusive")
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
	path, address, callbackHost, redirectURL, attemptPolicy, err := resolveCallbackMode(r.opts, r.random)
	if err != nil {
		return err
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

	host := ln.Addr().String()
	if callbackHost != "" {
		host = callbackHost
	}
	if redirectURL == "" {
		redirectURL = "http://" + host + path
	}
	flow := newCallbackFlow(path, host, issuer, attemptPolicy)
	limited := newLimitedListener(ln, maxConcurrentConnections)
	server := &http.Server{
		Handler:           flow,
		ReadHeaderTimeout: 2 * time.Second,
		WriteTimeout:      2 * time.Second,
		IdleTimeout:       2 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	serveDone := make(chan error, 1)
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

	present := r.presenter(flow)

	opCtx, cancel := context.WithCancel(ctx)
	authorizeErr := authorize(opCtx, redirectURL, present)
	cancel()
	flow.complete(callbackOutcome{err: ErrAuthorizationFailed})
	cleanupErr := stopServer(server, ln, serveDone)
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

// resolveCallbackMode picks the callback path, bind address, Host-header match, any
// pre-computed redirect URL, and attempt policy for one Authorize call. Extracted from
// Authorize purely to keep that function's branch count under the gocyclo limit; the
// three cases mirror Options.RedirectURL/PinCallbackPath's doc comments exactly.
func resolveCallbackMode(opts Options, random io.Reader) (
	path, address, callbackHost, redirectURL string, attemptPolicy callbackAttemptPolicy, err error,
) {
	address = "127.0.0.1:0"
	switch {
	case opts.RedirectURL != "":
		// Fixed path AND fixed port: an exact redirect_uri string match, for a
		// target with no obligation to implement RFC 8252 dynamic-port matching.
		path = fixedCallbackPath
		address = "127.0.0.1:18473"
		callbackHost = "127.0.0.1:18473"
		redirectURL = ExactRedirectURL
		attemptPolicy = attemptFixedRoute
	case opts.PinCallbackPath:
		// Fixed path, ephemeral port: address/callbackHost/redirectURL stay at their
		// random-port defaults — computed by the caller from the OS-assigned
		// listener address, exactly like the random-path case below — so only the
		// path is pinned; an RFC 8252-compliant AS's dynamic-port matching accepts
		// whichever port this run happens to get.
		path = fixedCallbackPath
		attemptPolicy = attemptFixedRoute
	default:
		attemptPolicy = attemptMatchingRoute
		path, err = randomCallbackPath(random)
		if err != nil {
			return "", "", "", "", 0, errors.New("generate OAuth callback path: failed")
		}
	}
	return path, address, callbackHost, redirectURL, attemptPolicy, nil
}

func isExactRedirectURL(raw string) bool {
	return raw == ExactRedirectURL
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

func stopServer(server *http.Server, listener net.Listener, serveDone <-chan error) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := server.Shutdown(cleanupCtx)
	_ = listener.Close()
	if shutdownErr != nil {
		_ = server.Close()
	}

	var serveErr error
	select {
	case serveErr = <-serveDone:
	case <-cleanupCtx.Done():
		_ = server.Close()
		serveErr = <-serveDone
	}
	if shutdownErr != nil && !errors.Is(shutdownErr, context.DeadlineExceeded) {
		return errors.New("shutdown OAuth callback server: failed")
	}
	if serveErr != nil {
		return errors.New("OAuth callback server failed")
	}
	return nil
}
