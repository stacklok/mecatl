package oauthlogin

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	maxRequestAttempts         = 16
	maxConcurrentConnections   = 4
	maxRawQueryBytes           = 8 << 10
	maxQueryValueBytes         = 2 << 10
	contentSecurityPolicyValue = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
	permissionsPolicyValue     = "accelerometer=(), autoplay=(), camera=(), display-capture=(), geolocation=(), microphone=(), payment=(), usb=()"
)

const (
	successHTML = "<!doctype html><html lang=en><meta charset=utf-8><title>OAuth complete</title><body><h1>Authorization complete</h1><p>You may close this window.</p></body></html>"
	failureHTML = "<!doctype html><html lang=en><meta charset=utf-8><title>OAuth callback rejected</title><body><h1>Authorization could not be completed</h1><p>Return to the application and try again.</p></body></html>"
)

// Rejection reasons name the validation rule that failed. They are rule names
// only — never a code, state, token, or any other callback value — so they are
// safe to return to the operator.
const (
	reasonRoute          = "path or Host did not match the callback"
	reasonMethod         = "callback used a method other than GET"
	reasonBody           = "callback carried a request body"
	reasonQuery          = "callback query was empty, oversized, or malformed"
	reasonState          = "callback state did not match the authorization request"
	reasonIssuerMismatch = "callback iss did not match the expected issuer"
	reasonErrorAndCode   = "callback carried both error and code"
	reasonNoCode         = "callback carried neither error nor code"
)

// maxOAuthErrorField bounds each surfaced field of a provider error response.
const maxOAuthErrorField = 200

// AuthorizationErrorResponse carries the provider's OAuth error response from the
// callback. Both fields are surfaced: an authorization that fails at the provider
// is otherwise undiagnosable without reproducing the request by hand, and the
// operator owns the authorization server being quoted. They are sanitized, not
// withheld -- RFC 6749 section 4.1.2.1 restricts these values to a printable
// subset, so anything outside it is dropped rather than echoed, and each field is
// clamped. That defeats log injection and unbounded output without hiding the one
// thing the operator needs.
type AuthorizationErrorResponse struct {
	Code        string
	Description string
}

func (e *AuthorizationErrorResponse) Error() string {
	clean := e.Sanitized()
	msg := "OAuth authorization failed: " + clean.Code
	if clean.Description != "" {
		msg += ": " + clean.Description
	}
	return msg
}

// Sanitized returns a bounded copy safe for crossing diagnostic boundaries.
func (e *AuthorizationErrorResponse) Sanitized() *AuthorizationErrorResponse {
	if e == nil {
		return &AuthorizationErrorResponse{Code: "unspecified"}
	}
	code := sanitizeOAuthErrorField(e.Code)
	if code == "" {
		code = "unspecified"
	}
	return &AuthorizationErrorResponse{Code: code, Description: sanitizeOAuthErrorField(e.Description)}
}

// Is reports the sentinel this error stands in for.
func (*AuthorizationErrorResponse) Is(target error) bool { return target == ErrAuthorizationFailed }

// sanitizeOAuthErrorField keeps only the printable subset RFC 6749 allows for
// error and error_description, then clamps. A value that is entirely disallowed
// comes back empty, never partially reconstructed.
func sanitizeOAuthErrorField(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch {
		case r == 0x20 || r == 0x21, r >= 0x23 && r <= 0x5B, r >= 0x5D && r <= 0x7E:
			b.WriteRune(r)
		default:
			// Everything else, control characters included, is dropped.
		}
		if b.Len() >= maxOAuthErrorField {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// CallbackRejectedError names the rule that rejected an authenticated callback.
// It satisfies errors.Is(err, ErrAuthorizationFailed) so existing callers that
// test for that sentinel keep working.
type CallbackRejectedError struct{ Reason string }

func (e *CallbackRejectedError) Error() string {
	return "OAuth callback rejected: " + e.Sanitized().Reason
}

// Sanitized returns a copy containing only a callback validator's closed reason.
func (e *CallbackRejectedError) Sanitized() *CallbackRejectedError {
	reason := "callback did not satisfy validation requirements"
	if e != nil {
		switch e.Reason {
		case reasonRoute, reasonMethod, reasonBody, reasonQuery, reasonState,
			reasonIssuerMismatch, reasonErrorAndCode, reasonNoCode:
			reason = e.Reason
		}
	}
	return &CallbackRejectedError{Reason: reason}
}

// Is reports the sentinel this error stands in for.
func (*CallbackRejectedError) Is(target error) bool { return target == ErrAuthorizationFailed }

// rejection carries why a callback was refused. authenticated is true once the
// sender has proved knowledge of the state secret.
type rejection struct {
	status        int
	reason        string
	authenticated bool
}

func reject(status int, reason string) rejection {
	return rejection{status: status, reason: reason}
}

func rejectAuthenticated(reason string) rejection {
	return rejection{status: http.StatusBadRequest, reason: reason, authenticated: true}
}

type callbackOutcome struct {
	result Result
	err    error
}

type callbackAttemptPolicy uint8

const (
	attemptMatchingRoute callbackAttemptPolicy = iota
	attemptFixedRoute
)

type callbackFlow struct {
	path           string
	host           string
	expectedIssuer string
	attemptPolicy  callbackAttemptPolicy
	expectedState  string
	stateSet       atomic.Bool
	attempts       atomic.Int32
	completed      atomic.Bool
	lastReject     atomic.Pointer[string]
	resultOnce     sync.Once
	result         chan callbackOutcome
}

func newCallbackFlow(path, host, issuer string, attemptPolicy callbackAttemptPolicy) *callbackFlow {
	return &callbackFlow{
		path:           path,
		host:           host,
		expectedIssuer: issuer,
		attemptPolicy:  attemptPolicy,
		result:         make(chan callbackOutcome, 1),
	}
}

func (f *callbackFlow) setExpectedState(state string) bool {
	if f.stateSet.Load() {
		return false
	}
	f.expectedState = state
	f.stateSet.Store(true)
	return true
}

func (f *callbackFlow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	// The random path is the callback capability. Unauthenticated loopback probes
	// (including a forged Host) must not spend the capability-bearing attempt
	// budget and prevent the real browser callback from completing.
	if r.URL.Path != f.path || r.URL.RawPath != "" || r.URL.Fragment != "" || r.Host != f.host {
		writeFailure(w, http.StatusNotFound)
		return
	}
	attempt := int32(0)
	if f.attemptPolicy == attemptMatchingRoute {
		attempt = f.attempts.Add(1)
		if attempt > maxRequestAttempts {
			f.complete(callbackOutcome{err: ErrCallbackAttempts})
			writeFailure(w, http.StatusTooManyRequests)
			return
		}
	}

	outcome, rej, ok := f.validate(r)
	if !ok {
		if rej.authenticated {
			// The sender knew the state secret, so this is the real browser and
			// its rejection reason is trustworthy: end the flow now instead of
			// waiting out the callback timeout.
			f.complete(callbackOutcome{err: (&CallbackRejectedError{Reason: rej.reason}).Sanitized()})
		} else {
			// Fixed callbacks are public, pre-registered routes. Ambient malformed
			// requests and wrong-state probes cannot consume their attempt budget.
			// Random-path callbacks retain the bounded matching-route policy.
			f.recordRejection(rej.reason)
			if f.attemptPolicy == attemptMatchingRoute && attempt == maxRequestAttempts {
				f.complete(callbackOutcome{err: ErrCallbackAttempts})
			}
		}
		writeFailure(w, rej.status)
		return
	}
	if !f.tryComplete(outcome) {
		writeFailure(w, http.StatusGone)
		return
	}
	if outcome.err != nil {
		writeFailure(w, http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(successHTML))
}

func (f *callbackFlow) validate(r *http.Request) (callbackOutcome, rejection, bool) {
	query, rej, ok := f.callbackQuery(r)
	if !ok {
		return callbackOutcome{}, rej, false
	}
	state := query.Get("state")
	issuer := query.Get("iss")
	if !f.stateSet.Load() || subtle.ConstantTimeCompare([]byte(state), []byte(f.expectedState)) != 1 {
		return callbackOutcome{}, reject(http.StatusBadRequest, reasonState), false
	}
	// Everything below the state compare is authenticated: only the browser that
	// received the authorization URL knows this value.
	//
	// RFC 9207 section 2.4: validate iss whenever it is present, but do not
	// require it here. Whether the authorization server promised to send one is a
	// discovery fact this listener does not have; the caller holds the discovery
	// document and enforces the required-if-advertised half. Requiring it
	// unconditionally locks out every provider that does not implement RFC 9207.
	// main reached the same conclusion independently in #766; this keeps that
	// guard and adds only the named reason on the mismatch path.
	if issuer != "" {
		canonical, err := canonicalIssuer(issuer)
		if err != nil || canonical != f.expectedIssuer {
			return callbackOutcome{}, rejectAuthenticated(reasonIssuerMismatch), false
		}
	}

	_, hasError := query["error"]
	_, hasCode := query["code"]
	if hasError {
		if hasCode {
			return callbackOutcome{}, rejectAuthenticated(reasonErrorAndCode), false
		}
		code := sanitizeOAuthErrorField(query.Get("error"))
		if code == "" {
			code = "unspecified"
		}
		return callbackOutcome{err: &AuthorizationErrorResponse{
			Code:        code,
			Description: sanitizeOAuthErrorField(query.Get("error_description")),
		}}, reject(http.StatusBadRequest, ""), true
	}
	if !hasCode {
		return callbackOutcome{}, rejectAuthenticated(reasonNoCode), false
	}
	return callbackOutcome{result: Result{Code: query.Get("code"), State: state, Iss: issuer}}, reject(http.StatusOK, ""), true
}

func (f *callbackFlow) recordRejection(reason string) {
	if reason == "" {
		return
	}
	f.lastReject.Store(&reason)
}

// annotate adds the last unauthenticated rejection reason to a timeout or
// cancellation, so a flow that never received a valid callback still reports
// what it did receive.
func (f *callbackFlow) annotate(err error) error {
	reason := f.lastReject.Load()
	if reason == nil {
		return err
	}
	return fmt.Errorf("%w (last callback rejected: %s)", err, *reason)
}

func (f *callbackFlow) callbackQuery(r *http.Request) (url.Values, rejection, bool) {
	if r.Method != http.MethodGet {
		return nil, reject(http.StatusMethodNotAllowed, reasonMethod), false
	}
	if r.URL.Path != f.path || r.URL.RawPath != "" || r.URL.Fragment != "" || r.Host != f.host {
		return nil, reject(http.StatusNotFound, reasonRoute), false
	}
	if r.ContentLength > 0 || len(r.TransferEncoding) != 0 || (r.Body != nil && r.Body != http.NoBody) {
		return nil, reject(http.StatusBadRequest, reasonBody), false
	}
	if len(r.URL.RawQuery) == 0 || len(r.URL.RawQuery) > maxRawQueryBytes {
		return nil, reject(http.StatusBadRequest, reasonQuery), false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || !validQuery(query) {
		return nil, reject(http.StatusBadRequest, reasonQuery), false
	}
	return query, reject(http.StatusOK, ""), true
}

func validQuery(query url.Values) bool {
	for key, values := range query {
		if key == "" || len(values) != 1 || values[0] == "" || len(values[0]) > maxQueryValueBytes {
			return false
		}
	}
	return len(query["state"]) == 1
}

func (f *callbackFlow) complete(outcome callbackOutcome) {
	f.tryComplete(outcome)
}

func (f *callbackFlow) tryComplete(outcome callbackOutcome) bool {
	if !f.completed.CompareAndSwap(false, true) {
		return false
	}
	f.resultOnce.Do(func() { f.result <- outcome })
	return true
}

func setSecurityHeaders(header http.Header) {
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Security-Policy", contentSecurityPolicyValue)
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Permissions-Policy", permissionsPolicyValue)
}

func writeFailure(w http.ResponseWriter, status int) {
	w.WriteHeader(status)
	_, _ = w.Write([]byte(failureHTML))
}

type limitedListener struct {
	net.Listener
	active chan struct{}
}

func newLimitedListener(listener net.Listener, concurrent int) *limitedListener {
	return &limitedListener{Listener: listener, active: make(chan struct{}, concurrent)}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.active <- struct{}{}:
			return &limitedConn{Conn: conn, release: func() { <-l.active }}, nil
		default:
			// Shed only this excess connection. An arbitrary loopback peer must not
			// consume a flow-wide accept budget and terminate the OAuth flow.
			_ = conn.Close()
		}
	}
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
