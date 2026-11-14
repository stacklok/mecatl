package identityharness_test

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// faultClient is a client for the harness's AS-1 through a FaultTransport.
func faultClient(f *idjagFixture) (*http.Client, *identityharness.FaultTransport) {
	client := f.h.HTTPClient()
	ft := identityharness.NewFaultTransport(client.Transport, f.h.AS1.TokenEndpoint())
	client.Transport = ft
	return client, ft
}

// post sends form to AS-1's token endpoint through client and returns the
// status, or the transport error.
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

func TestFaultTransportTellsKindsApart(t *testing.T) {
	f := newIDJAGFixture(t, identityharness.Options{})
	client, ft := faultClient(f)
	endpoint := f.h.AS1.TokenEndpoint()
	if status, err := post(t, client, endpoint, idjagForm(f.h, f.grant, svidAlice)); err != nil || status != http.StatusOK {
		t.Fatalf("ID-JAG exchange = %d, %v", status, err)
	}
	open := exchangeForm(f.h, aliceToken(f.h), `[{"type":"`+exchangeGrantType+`","issuable_token_types":["`+tokenTypeIDJAG+`"]}]`)
	if status, err := post(t, client, endpoint, open); err != nil || status != http.StatusOK {
		t.Fatalf("session open = %d, %v", status, err)
	}
	// AS-1 refuses a redemption, but the transport still records it.
	if status, err := post(t, client, endpoint, url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {"jag"}}); err != nil || status < 400 {
		t.Fatalf("redemption = %d, %v", status, err)
	}
	// Requests elsewhere pass through unrecorded.
	if _, err := post(t, client, f.h.AS1.RevocationEndpoint(), url.Values{"token": {"x"}}); err != nil {
		t.Fatal(err)
	}
	got := ft.Requests()
	if len(got) != 3 || got[0].Kind != identityharness.KindIDJAG || got[1].Kind != "" || got[2].Kind != identityharness.KindRedemption {
		t.Fatalf("requests = %+v, want an ID-JAG exchange, a session open of no kind, and a redemption", got)
	}
	if got[0].IssuedToken == "" || got[0].ClientAssertion == "" || !got[0].Reached || got[0].Status != http.StatusOK {
		t.Fatalf("ID-JAG request = %+v, want the issued token and client assertion recorded", got[0])
	}
	if got[2].Assertion != "jag" || got[2].IssuedToken != "" || got[2].Code == "" {
		t.Fatalf("redemption = %+v, want its assertion and AS-1's refusal recorded", got[2])
	}
	for kind, want := range map[identityharness.GrantKind]int{identityharness.KindIDJAG: 1, identityharness.KindRedemption: 1} {
		if n := ft.Count(kind); n != want {
			t.Errorf("Count(%s) = %d, want %d", kind, n, want)
		}
	}
}

func TestFaultTransportFaults(t *testing.T) {
	f := newIDJAGFixture(t, identityharness.Options{})
	client, ft := faultClient(f)
	endpoint := f.h.AS1.TokenEndpoint()
	idjag := func() (int, error) { return post(t, client, endpoint, idjagForm(f.h, f.grant, svidAlice)) }

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
		code    string
		reason  string
	}{
		{identityharness.FaultAnswered, false, "", ""},
		{identityharness.FaultLost, true, "", ""},
		{identityharness.FaultForwarded, true, "", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("requests = %+v", got)
	}
	for i, w := range want {
		if got[i].Fault != w.fault || got[i].Reached != w.reached || got[i].Code != w.code || got[i].Reason != w.reason {
			t.Errorf("request %d = %+v, want %+v", i, got[i], w)
		}
	}
	// A lost response's ID-JAG was issued: the server processed it.
	if got[1].IssuedToken == "" || len(f.h.AS1.IDJAGsIssued()) != 2 {
		t.Fatalf("lost request = %+v with %d ID-JAGs issued, want it issued", got[1], len(f.h.AS1.IDJAGsIssued()))
	}
}

func TestFaultTransportOnNextAndDuplicate(t *testing.T) {
	f := newIDJAGFixture(t, identityharness.Options{})
	client, ft := faultClient(f)
	endpoint := f.h.AS1.TokenEndpoint()

	// OnNext runs before forwarding, so revoking there refuses this request.
	ft.OnNext(identityharness.KindIDJAG, func() { f.h.AS1.RevokeGrants(identityharness.Alice) })
	if status, err := post(t, client, endpoint, idjagForm(f.h, f.grant, svidAlice)); err != nil || status != http.StatusBadRequest {
		t.Fatalf("exchange after OnNext revoked = %d, %v; want 400", status, err)
	}
	if r := ft.Requests()[0]; r.Fault != identityharness.FaultOnNext || r.Code != "invalid_request" || r.Reason != "revoked" {
		t.Fatalf("request = %+v, want the OnNext request refused as revoked", r)
	}

	// DuplicateNext returns the first answer and records the resubmission's:
	// the replayed client assertion is refused.
	grant := f.openGrant(t, identityharness.Alice)
	ft.DuplicateNext(identityharness.KindIDJAG)
	if status, err := post(t, client, endpoint, idjagForm(f.h, grant, svidAlice)); err != nil || status != http.StatusOK {
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
