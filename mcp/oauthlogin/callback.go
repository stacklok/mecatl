package oauthlogin

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
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

type callbackOutcome struct {
	result Result
	err    error
}

type callbackFlow struct {
	path           string
	host           string
	expectedIssuer string
	expectedState  string
	stateSet       atomic.Bool
	attempts       atomic.Int32
	completed      atomic.Bool
	resultOnce     sync.Once
	result         chan callbackOutcome
}

func newCallbackFlow(path, host, issuer string) *callbackFlow {
	return &callbackFlow{
		path:           path,
		host:           host,
		expectedIssuer: issuer,
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
	attempt := f.attempts.Add(1)
	if attempt > maxRequestAttempts {
		f.complete(callbackOutcome{err: ErrCallbackAttempts})
		writeFailure(w, http.StatusTooManyRequests)
		return
	}

	outcome, status, ok := f.validate(r)
	if !ok {
		if attempt == maxRequestAttempts {
			f.complete(callbackOutcome{err: ErrCallbackAttempts})
		}
		writeFailure(w, status)
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

func (f *callbackFlow) validate(r *http.Request) (callbackOutcome, int, bool) {
	query, status, ok := f.callbackQuery(r)
	if !ok {
		return callbackOutcome{}, status, false
	}
	state := query.Get("state")
	issuer := query.Get("iss")
	if !f.stateSet.Load() || subtle.ConstantTimeCompare([]byte(state), []byte(f.expectedState)) != 1 {
		return callbackOutcome{}, http.StatusBadRequest, false
	}
	if issuer != "" {
		canonical, err := canonicalIssuer(issuer)
		if err != nil || canonical != f.expectedIssuer {
			return callbackOutcome{}, http.StatusBadRequest, false
		}
	}

	_, hasError := query["error"]
	_, hasCode := query["code"]
	if hasError {
		if hasCode {
			return callbackOutcome{}, http.StatusBadRequest, false
		}
		return callbackOutcome{err: ErrAuthorizationFailed}, http.StatusBadRequest, true
	}
	if !hasCode {
		return callbackOutcome{}, http.StatusBadRequest, false
	}
	return callbackOutcome{result: Result{Code: query.Get("code"), State: state, Iss: issuer}}, http.StatusOK, true
}

func (f *callbackFlow) callbackQuery(r *http.Request) (url.Values, int, bool) {
	if r.Method != http.MethodGet {
		return nil, http.StatusMethodNotAllowed, false
	}
	if r.URL.Path != f.path || r.URL.RawPath != "" || r.URL.Fragment != "" || r.Host != f.host {
		return nil, http.StatusNotFound, false
	}
	if r.ContentLength > 0 || len(r.TransferEncoding) != 0 || (r.Body != nil && r.Body != http.NoBody) {
		return nil, http.StatusBadRequest, false
	}
	if len(r.URL.RawQuery) == 0 || len(r.URL.RawQuery) > maxRawQueryBytes {
		return nil, http.StatusBadRequest, false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || !validQuery(query) {
		return nil, http.StatusBadRequest, false
	}
	return query, http.StatusOK, true
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
