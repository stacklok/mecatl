package as1servertest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server"
)

const grantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange" // #nosec G101 -- RFC 8693 grant type URN, not a credential.

// errConnectionClosed is what a dropped request or a lost response returns,
// as a connection closed without a response.
var errConnectionClosed = fmt.Errorf("as1servertest: connection closed without a response: %w", io.ErrUnexpectedEOF)

// FaultTransport is an http.RoundTripper in front of AS-1's in-process
// transport (as1server.Server.InProcessClient). It forwards every request,
// records each one for the token endpoint, and applies the faults a test
// queued to token exchanges, one fault per exchange in the order queued.
// It is a transport rather than a handler wrapper because a lost connection
// can only be modelled on the caller's side: the in-process transport
// re-raises a handler's panic, such as http.ErrAbortHandler, in the caller.
// It is safe for concurrent use.
type FaultTransport struct {
	next          http.RoundTripper
	tokenEndpoint string

	mu       sync.Mutex
	queue    []fault
	requests []Request
}

func newFaultTransport(server *as1server.Server) *FaultTransport {
	return &FaultTransport{next: server.InProcessClient().Transport, tokenEndpoint: server.TokenEndpoint()}
}

// FaultKind is what FaultTransport did to a request.
type FaultKind string

// The fault kinds. FaultNone forwarded the request and returned AS-1's
// response unchanged.
const (
	FaultNone         FaultKind = ""
	FaultAnswered     FaultKind = "answered"
	FaultDropped      FaultKind = "dropped"
	FaultLost         FaultKind = "lost"
	FaultEchoReplaced FaultKind = "echo replaced"
)

// Fault is an OAuth error FaultTransport answers instead of AS-1.
type Fault struct {
	// Status is the HTTP status. Zero selects 400.
	Status int
	// Error is the OAuth error code; empty leaves error out of the body.
	Error string
	// Reason is the error_description, which starts with the reason code
	// in the design's error reasons; empty leaves it out.
	Reason string
}

// ExchangeGrant is an exchange-grant authorization_details entry as AS-1
// echoes it. ExpiresAt is Unix seconds; zero leaves it out.
type ExchangeGrant struct {
	Type               string   `json:"type"`
	IssuableTokenTypes []string `json:"issuable_token_types"`
	ExpiresAt          int64    `json:"expires_at,omitempty"`
}

type fault struct {
	kind   FaultKind
	answer Fault
	echo   []ExchangeGrant
}

// Request is a request FaultTransport saw for AS-1's token endpoint.
type Request struct {
	// GrantType is the request's grant_type.
	GrantType string
	// ClientAssertion is the client assertion the request carried; zero when
	// it carried none.
	ClientAssertion identitybroker.Token
	// Fault is what FaultTransport did to the request.
	Fault FaultKind
	// Reached reports whether AS-1 processed the request.
	Reached bool
	// Status is AS-1's HTTP status, zero when it did not process the
	// request. A lost response keeps the status AS-1 answered.
	Status int
	// Code and Reason are AS-1's OAuth error code and the reason code that
	// starts its error_description, empty when absent.
	Code, Reason string
}

// Exchange reports whether r is a token exchange.
func (r Request) Exchange() bool { return r.GrantType == grantTypeTokenExchange }

// Converted reports whether AS-1 converted r's subject token, whether or not
// the response reached the caller.
func (r Request) Converted() bool { return r.Exchange() && r.Reached && r.Status == http.StatusOK }

// Refused reports whether AS-1 refused r.
func (r Request) Refused() bool { return r.Reached && r.Status >= http.StatusBadRequest }

// FailNextExchange makes FaultTransport answer the next token exchange with
// answer without forwarding it.
func (f *FaultTransport) FailNextExchange(answer Fault) {
	f.push(fault{kind: FaultAnswered, answer: answer})
}

// DropNextExchange makes the next token exchange fail without a response and
// without reaching AS-1, as a request lost on its way.
func (f *FaultTransport) DropNextExchange() { f.push(fault{kind: FaultDropped}) }

// LoseNextExchangeResponse forwards the next token exchange and then fails
// it without a response, as a response lost after AS-1 processed the
// request.
func (f *FaultTransport) LoseNextExchangeResponse() { f.push(fault{kind: FaultLost}) }

// ReplaceNextExchangeEcho forwards the next token exchange and, if AS-1
// converts it, replaces the echoed authorization_details with echo, or
// removes the member when echo is empty. The grant is still the one AS-1
// granted.
func (f *FaultTransport) ReplaceNextExchangeEcho(echo ...ExchangeGrant) {
	f.push(fault{kind: FaultEchoReplaced, echo: slices.Clone(echo)})
}

func (f *FaultTransport) push(q fault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, q)
}

func (f *FaultTransport) pop() fault {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queue) == 0 {
		return fault{}
	}
	q := f.queue[0]
	f.queue = f.queue[1:]
	return q
}

// Pending returns the kinds of the queued faults no token exchange has
// used yet, in the order queued.
func (f *FaultTransport) Pending() []FaultKind {
	f.mu.Lock()
	defer f.mu.Unlock()
	kinds := make([]FaultKind, 0, len(f.queue))
	for _, q := range f.queue {
		kinds = append(kinds, q.kind)
	}
	return kinds
}

// Requests returns the requests for the token endpoint, oldest first.
func (f *FaultTransport) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// Conversions returns how many token exchanges AS-1 converted, including any
// whose response was lost.
func (f *FaultTransport) Conversions() int {
	n := 0
	for _, r := range f.Requests() {
		if r.Converted() {
			n++
		}
	}
	return n
}

// Refusals returns the requests AS-1 refused, oldest first.
func (f *FaultTransport) Refusals() []Request {
	var refused []Request
	for _, r := range f.Requests() {
		if r.Refused() {
			refused = append(refused, r)
		}
	}
	return refused
}

func (f *FaultTransport) record(r Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
}

// RoundTrip implements http.RoundTripper.
func (f *FaultTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || req.URL.Scheme+"://"+req.URL.Host+req.URL.Path != f.tokenEndpoint || req.Body == nil {
		return f.next.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	form, _ := url.ParseQuery(string(body))
	rec := Request{GrantType: form.Get("grant_type"), ClientAssertion: identitybroker.NewToken(form.Get("client_assertion"))}
	var q fault
	if rec.Exchange() {
		q = f.pop()
	}
	rec.Fault = q.kind
	switch q.kind {
	case FaultAnswered:
		f.record(rec)
		return oauthError(req, q.answer), nil
	case FaultDropped:
		f.record(rec)
		return nil, errConnectionClosed
	}
	inner := req.Clone(req.Context())
	inner.Body = io.NopCloser(bytes.NewReader(body))
	inner.ContentLength = int64(len(body))
	resp, err := f.next.RoundTrip(inner)
	if err != nil {
		rec.Fault = FaultNone
		f.record(rec)
		return nil, err
	}
	answer, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	rec.Reached, rec.Status = true, resp.StatusCode
	if err != nil {
		rec.Fault = FaultNone
		f.record(rec)
		return nil, err
	}
	var oauth struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if resp.StatusCode >= http.StatusBadRequest && json.Unmarshal(answer, &oauth) == nil {
		rec.Code = oauth.Error
		rec.Reason, _, _ = strings.Cut(oauth.Description, ":")
	}
	switch {
	case q.kind == FaultLost:
		f.record(rec)
		return nil, errConnectionClosed
	case q.kind == FaultEchoReplaced && resp.StatusCode == http.StatusOK:
		answer, err = replaceEcho(answer, q.echo)
		if err != nil {
			return nil, err
		}
	case q.kind == FaultEchoReplaced:
		rec.Fault = FaultNone
	}
	f.record(rec)
	resp.Body = io.NopCloser(bytes.NewReader(answer))
	resp.ContentLength = int64(len(answer))
	resp.Header.Set("Content-Length", strconv.Itoa(len(answer)))
	return resp, nil
}

// replaceEcho returns the success response body with authorization_details
// replaced by echo, or removed when echo is empty.
func replaceEcho(body []byte, echo []ExchangeGrant) ([]byte, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		return nil, errors.New("as1servertest: AS-1's exchange response is not a JSON object")
	}
	if len(echo) == 0 {
		delete(members, "authorization_details")
	} else {
		raw, err := json.Marshal(echo)
		if err != nil {
			return nil, err
		}
		members["authorization_details"] = raw
	}
	return json.Marshal(members)
}

// oauthError is FaultTransport's answer to req with answer.
func oauthError(req *http.Request, answer Fault) *http.Response {
	status := answer.Status
	if status == 0 {
		status = http.StatusBadRequest
	}
	members := map[string]string{}
	if answer.Error != "" {
		members["error"] = answer.Error
	}
	if answer.Reason != "" {
		members["error_description"] = answer.Reason
	}
	body, _ := json.Marshal(members)
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	rec.Header().Set("Cache-Control", "no-store")
	rec.Header().Set("Content-Length", strconv.Itoa(len(body)))
	rec.WriteHeader(status)
	_, _ = rec.Write(body)
	resp := rec.Result()
	resp.Request = req
	return resp
}
