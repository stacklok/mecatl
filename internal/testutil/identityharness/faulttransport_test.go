package identityharness_test

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

const (
	grantTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	grantJWTBearer     = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	tokenTypeIDJAG     = "urn:ietf:params:oauth:token-type:id-jag"
)

// tokenServer is a stub token endpoint: it issues an ID-JAG for an ID-JAG
// exchange unless revoked is set, then answering invalid_request revoked, a
// refresh token for any other token exchange, and refuses a redemption with
// invalid_grant. A client assertion it has seen before is refused with 401
// invalid_client.
type tokenServer struct {
	base, endpoint string
	client         *http.Client

	mu      sync.Mutex
	revoked bool
	seen    map[string]bool
	issued  int
}

func startTokenServer(t *testing.T) *tokenServer {
	t.Helper()
	s := &tokenServer{seen: map[string]bool{}}
	server := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(server.Close)
	s.base, s.endpoint, s.client = server.URL, server.URL+"/token", server.Client()
	return s
}

func (s *tokenServer) serve(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	answer := func(status int, body map[string]string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if assertion := r.PostForm.Get("client_assertion"); assertion != "" {
		if s.seen[assertion] {
			answer(http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
			return
		}
		s.seen[assertion] = true
	}
	switch {
	case r.URL.Path != "/token":
		w.WriteHeader(http.StatusOK)
	case r.PostForm.Get("grant_type") == grantJWTBearer:
		answer(http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
	case r.PostForm.Get("requested_token_type") == tokenTypeIDJAG && s.revoked:
		answer(http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": "revoked"})
	default:
		s.issued++
		answer(http.StatusOK, map[string]string{"access_token": "issued-" + strconv.Itoa(s.issued)})
	}
}

func (s *tokenServer) revoke() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked = true
}

func (s *tokenServer) issuedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.issued
}

// faultClient is a client for s through a FaultTransport.
func faultClient(s *tokenServer) (*http.Client, *identityharness.FaultTransport) {
	ft := identityharness.NewFaultTransport(s.client.Transport, s.endpoint)
	return &http.Client{Transport: ft}, ft
}

// post sends form to endpoint through client and returns the status, or the
// transport error.
func post(t *testing.T, client *http.Client, endpoint string, form url.Values) (int, error) {
	t.Helper()
	resp, err := client.PostForm(endpoint, form)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// idjagForm is an ID-JAG exchange with a fresh client assertion.
func idjagForm() url.Values {
	return url.Values{
		"grant_type": {grantTokenExchange}, "requested_token_type": {tokenTypeIDJAG},
		"subject_token": {"grant"}, "actor_token": {"svid"}, "mecatl_operation": {"op-1"},
		"client_assertion": {rand.Text()},
	}
}

func TestFaultTransportTellsKindsApart(t *testing.T) {
	s := startTokenServer(t)
	client, ft := faultClient(s)
	if status, err := post(t, client, s.endpoint, idjagForm()); err != nil || status != http.StatusOK {
		t.Fatalf("ID-JAG exchange = %d, %v", status, err)
	}
	open := url.Values{"grant_type": {grantTokenExchange}, "requested_token_type": {"urn:ietf:params:oauth:token-type:refresh_token"}}
	if status, err := post(t, client, s.endpoint, open); err != nil || status != http.StatusOK {
		t.Fatalf("session open = %d, %v", status, err)
	}
	// The server refuses a redemption, but the transport still records it.
	if status, err := post(t, client, s.endpoint, url.Values{"grant_type": {grantJWTBearer}, "assertion": {"jag"}}); err != nil || status < 400 {
		t.Fatalf("redemption = %d, %v", status, err)
	}
	// Requests elsewhere pass through unrecorded.
	if _, err := post(t, client, s.base+"/revoke", url.Values{"token": {"x"}}); err != nil {
		t.Fatal(err)
	}
	got := ft.Requests()
	if len(got) != 3 || got[0].Kind != identityharness.KindIDJAG || got[1].Kind != "" || got[2].Kind != identityharness.KindRedemption {
		t.Fatalf("requests = %+v, want an ID-JAG exchange, a session open of no kind, and a redemption", got)
	}
	if got[0].IssuedToken == "" || got[0].ClientAssertion == "" || got[0].ActorToken != "svid" || got[0].Operation != "op-1" ||
		!got[0].Reached || got[0].Status != http.StatusOK {
		t.Fatalf("ID-JAG request = %+v, want the issued token, client assertion, actor token, and operation recorded", got[0])
	}
	if got[2].Assertion != "jag" || got[2].IssuedToken != "" || got[2].Code == "" {
		t.Fatalf("redemption = %+v, want its assertion and the server's refusal recorded", got[2])
	}
	for kind, want := range map[identityharness.GrantKind]int{identityharness.KindIDJAG: 1, identityharness.KindRedemption: 1} {
		if n := ft.Count(kind); n != want {
			t.Errorf("Count(%s) = %d, want %d", kind, n, want)
		}
	}
}

func TestFaultTransportFaults(t *testing.T) {
	s := startTokenServer(t)
	client, ft := faultClient(s)
	idjag := func() (int, error) { return post(t, client, s.endpoint, idjagForm()) }

	// Faults apply to their kind only, in the order queued.
	ft.FailNext(identityharness.KindIDJAG, identityharness.Fault{Error: "invalid_request", Reason: "revoked: grant revoked"})
	ft.LoseNextResponse(identityharness.KindIDJAG)
	ft.FailNext(identityharness.KindRedemption, identityharness.Fault{Status: http.StatusServiceUnavailable})
	if status, err := idjag(); err != nil || status != http.StatusBadRequest {
		t.Fatalf("failed exchange = %d, %v; want 400", status, err)
	}
	if _, err := idjag(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("lost exchange = %v, want a closed connection", err)
	}
	if status, err := idjag(); err != nil || status != http.StatusOK {
		t.Fatalf("exchange after the faults = %d, %v; want 200", status, err)
	}
	got := ft.Requests()
	want := []struct {
		fault   string
		reached bool
	}{
		{identityharness.FaultAnswered, false},
		{identityharness.FaultLost, true},
		{identityharness.FaultForwarded, true},
	}
	if len(got) != len(want) {
		t.Fatalf("requests = %+v", got)
	}
	for i, w := range want {
		if got[i].Fault != w.fault || got[i].Reached != w.reached || got[i].Code != "" || got[i].Reason != "" {
			t.Errorf("request %d = %+v, want %+v", i, got[i], w)
		}
	}
	// A lost response's ID-JAG was issued: the server processed it.
	if got[1].IssuedToken == "" || s.issuedCount() != 2 {
		t.Fatalf("lost request = %+v with %d ID-JAGs issued, want it issued", got[1], s.issuedCount())
	}
	// The redemption fault is still queued for its own kind.
	if status, err := post(t, client, s.endpoint, url.Values{"grant_type": {grantJWTBearer}, "assertion": {"jag"}}); err != nil || status != http.StatusServiceUnavailable {
		t.Fatalf("faulted redemption = %d, %v; want 503", status, err)
	}
}

func TestFaultTransportOnNextAndDuplicate(t *testing.T) {
	s := startTokenServer(t)
	client, ft := faultClient(s)

	// OnNext runs before forwarding, so revoking there refuses this request.
	ft.OnNext(identityharness.KindIDJAG, s.revoke)
	if status, err := post(t, client, s.endpoint, idjagForm()); err != nil || status != http.StatusBadRequest {
		t.Fatalf("exchange after OnNext revoked = %d, %v; want 400", status, err)
	}
	if r := ft.Requests()[0]; r.Fault != identityharness.FaultOnNext || r.Code != "invalid_request" || r.Reason != "revoked" {
		t.Fatalf("request = %+v, want the OnNext request refused as revoked", r)
	}

	// DuplicateNext returns the first answer and records the resubmission's:
	// the replayed client assertion is refused.
	s.mu.Lock()
	s.revoked = false // a newer grant
	s.mu.Unlock()
	ft.DuplicateNext(identityharness.KindIDJAG)
	if status, err := post(t, client, s.endpoint, idjagForm()); err != nil || status != http.StatusOK {
		t.Fatalf("duplicated exchange = %d, %v; want the first answer, 200", status, err)
	}
	got := ft.Requests()
	if len(got) != 3 || got[1].Fault != identityharness.FaultForwarded || got[1].Status != http.StatusOK ||
		got[2].Fault != identityharness.FaultDuplicate || got[2].Status != http.StatusUnauthorized || got[2].ClientAssertion != got[1].ClientAssertion {
		t.Fatalf("requests = %+v, want the original and a refused duplicate with the same assertion", got)
	}
	if n := ft.Count(identityharness.KindIDJAG); n != 2 {
		t.Fatalf("Count = %d, want 2 caller requests", n)
	}
	if got[2].Code != "invalid_client" {
		t.Fatalf("duplicate = %+v, want invalid_client for the replayed assertion", got[2])
	}
}
