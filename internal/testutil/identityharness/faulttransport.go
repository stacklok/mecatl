package identityharness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
)

const grantJWTBearer = "urn:ietf:params:oauth:grant-type:jwt-bearer" // #nosec G101 -- RFC 7523 grant type URN, not a credential.

// GrantKind is the kind of a token endpoint request FaultTransport tells
// apart.
type GrantKind string

// The grant kinds. A request of any other kind, such as a session open's
// token exchange, is forwarded and recorded with an empty kind, and no fault
// applies to it.
const (
	// KindIDJAG is a token exchange for an ID-JAG (design step (7)).
	KindIDJAG GrantKind = "id_jag"
	// KindRedemption is a JWT-bearer redemption of an ID-JAG (design step (8)).
	KindRedemption GrantKind = "redemption"
)

// What FaultTransport did to a request, in GrantRequest.Fault.
const (
	FaultForwarded = ""
	FaultAnswered  = "answered"
	FaultLost      = "lost"
	FaultOnNext    = "on_next"
	// FaultDuplicate is the resubmission DuplicateNext sends.
	FaultDuplicate = "duplicate"
)

// errConnectionClosed is what a lost response returns,
// as a connection closed without a response.
var errConnectionClosed = fmt.Errorf("identityharness: connection closed without a response: %w", io.ErrUnexpectedEOF)

// FaultTransport is an http.RoundTripper in front of an authorization
// server's transport, such as HTTPClient's for AS-1 or an in-process client
// for AS-2. It forwards every request, records each one for the token
// endpoint, and applies the faults a test queued, one per request of the
// fault's kind in the order queued. It is a transport rather than a handler
// wrapper because a lost connection can only be modelled on the caller's
// side. It is safe for concurrent use.
type FaultTransport struct {
	next          http.RoundTripper
	tokenEndpoint string

	mu       sync.Mutex
	queues   map[GrantKind][]queuedFault
	requests []GrantRequest
}

type queuedFault struct {
	fault  string
	answer Fault
	fn     func()
}

// NewFaultTransport returns a FaultTransport forwarding to next that
// records and faults requests for tokenEndpoint.
func NewFaultTransport(next http.RoundTripper, tokenEndpoint string) *FaultTransport {
	return &FaultTransport{next: next, tokenEndpoint: tokenEndpoint, queues: make(map[GrantKind][]queuedFault)}
}

// GrantRequest is a token endpoint request FaultTransport saw. The token
// fields are test instrumentation only.
type GrantRequest struct {
	Kind GrantKind
	// Fault is what FaultTransport did to the request: FaultForwarded,
	// FaultAnswered, FaultLost, FaultOnNext, or FaultDuplicate.
	Fault string
	// Reached reports whether the server processed the request.
	Reached bool
	// Status is the server's HTTP status, zero when it did not process the
	// request. A lost response keeps the status the server answered.
	Status int
	// Code and Reason are the server's OAuth error code and the reason code
	// that starts its error_description, empty when absent.
	Code, Reason string
	// ClientAssertion and Assertion are the request's client_assertion and
	// assertion; IssuedToken is the access_token of a success response, even
	// a lost one.
	ClientAssertion, Assertion, IssuedToken string
}

// FailNext answers the next request of kind with answer without forwarding
// it.
func (f *FaultTransport) FailNext(kind GrantKind, answer Fault) {
	f.push(kind, queuedFault{fault: FaultAnswered, answer: answer})
}

// LoseNextResponse forwards the next request of kind and then fails it
// without a response, as a response lost after the server processed it.
func (f *FaultTransport) LoseNextResponse(kind GrantKind) {
	f.push(kind, queuedFault{fault: FaultLost})
}

// OnNext runs fn when the next request of kind arrives, before forwarding
// it.
func (f *FaultTransport) OnNext(kind GrantKind, fn func()) {
	f.push(kind, queuedFault{fault: FaultOnNext, fn: fn})
}

// DuplicateNext forwards the next request of kind, returns its answer to
// the caller, and then resubmits the same body, client assertion included,
// recording the second answer as a FaultDuplicate request.
func (f *FaultTransport) DuplicateNext(kind GrantKind) {
	f.push(kind, queuedFault{fault: FaultDuplicate})
}

func (f *FaultTransport) push(kind GrantKind, q queuedFault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queues[kind] = append(f.queues[kind], q)
}

func (f *FaultTransport) pop(kind GrantKind) queuedFault {
	f.mu.Lock()
	defer f.mu.Unlock()
	queue := f.queues[kind]
	if kind == "" || len(queue) == 0 {
		return queuedFault{}
	}
	f.queues[kind] = queue[1:]
	return queue[0]
}

// Requests returns the token endpoint requests, oldest first.
func (f *FaultTransport) Requests() []GrantRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// Count returns how many requests of kind the caller sent, not counting
// DuplicateNext's resubmissions.
func (f *FaultTransport) Count(kind GrantKind) int {
	n := 0
	for _, r := range f.Requests() {
		if r.Kind == kind && r.Fault != FaultDuplicate {
			n++
		}
	}
	return n
}

func (f *FaultTransport) record(r GrantRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
}

func grantKind(form url.Values) GrantKind {
	switch form.Get("grant_type") {
	case grantTokenExchange:
		if form.Get("requested_token_type") == TokenTypeIDJAG {
			return KindIDJAG
		}
		return ""
	case grantJWTBearer:
		return KindRedemption
	default:
		return ""
	}
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
	rec := GrantRequest{Kind: grantKind(form), ClientAssertion: form.Get("client_assertion"), Assertion: form.Get("assertion")}
	q := f.pop(rec.Kind)
	rec.Fault = q.fault
	if q.fault == FaultDuplicate {
		// The original goes through unchanged; its resubmission is the fault.
		rec.Fault = FaultForwarded
	}
	switch q.fault {
	case FaultAnswered:
		f.record(rec)
		return oauthErrorResponse(req, q.answer), nil
	case FaultOnNext:
		q.fn()
	}
	resp, err := f.forward(req, body, &rec)
	f.record(rec)
	if err != nil {
		return nil, err
	}
	switch q.fault {
	case FaultLost:
		_ = resp.Body.Close()
		return nil, errConnectionClosed
	case FaultDuplicate:
		dup := GrantRequest{Kind: rec.Kind, Fault: FaultDuplicate, ClientAssertion: rec.ClientAssertion, Assertion: rec.Assertion}
		if again, err := f.forward(req, body, &dup); err == nil {
			_ = again.Body.Close()
		}
		f.record(dup)
	}
	return resp, nil
}

// forward sends body to the server as req did and fills rec in from the
// answer, returning the response with its body buffered.
func (f *FaultTransport) forward(req *http.Request, body []byte, rec *GrantRequest) (*http.Response, error) {
	inner := req.Clone(req.Context())
	inner.Body = io.NopCloser(bytes.NewReader(body))
	inner.ContentLength = int64(len(body))
	resp, err := f.next.RoundTrip(inner)
	if err != nil {
		return nil, err
	}
	answer, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	rec.Reached, rec.Status = true, resp.StatusCode
	var members struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(answer, &members) == nil {
		if resp.StatusCode >= http.StatusBadRequest {
			rec.Code = members.Error
			rec.Reason, _, _ = strings.Cut(members.Description, ":")
		} else {
			rec.IssuedToken = members.AccessToken
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(answer))
	resp.ContentLength = int64(len(answer))
	resp.Header.Set("Content-Length", strconv.Itoa(len(answer)))
	return resp, nil
}

// oauthErrorResponse is FaultTransport's answer to req with answer.
func oauthErrorResponse(req *http.Request, answer Fault) *http.Response {
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
