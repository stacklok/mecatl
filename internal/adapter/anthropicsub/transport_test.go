package anthropicsub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type staticSource struct {
	token   string
	account string
	err     error
}

func (s staticSource) AccessToken(context.Context) (string, error) {
	return s.token, s.err
}

func (s staticSource) AccountID(context.Context) (string, error) {
	return s.account, s.err
}

// captured records what actually reached the wire.
type captured struct {
	header http.Header
	body   []byte
	url    string
}

func roundTrip(t *testing.T, transport *Transport, body string) captured {
	t.Helper()
	var got captured
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		got = captured{header: r.Header.Clone(), body: raw, url: r.URL.String()}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		server.URL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Api-Key", "api-key-should-never-ship")
	req.Header.Set("User-Agent", "should-be-replaced")

	client := &http.Client{Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return got
}

func newTransport() *Transport {
	SetInstallID("install-canary")
	return &Transport{Source: staticSource{token: "oauth-access-token", account: "acct-canary"}}
}

func decodeBody(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("body is not an object: %v\n%s", err, raw)
	}
	return fields
}

// The grant is a bearer token, so it must travel in Authorization and the
// API-key header must never ship alongside it.
func TestTransportAuthenticatesWithBearerAndDropsAPIKey(t *testing.T) {
	got := roundTrip(t, newTransport(), `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`)

	if want := "Bearer oauth-access-token"; got.header.Get("Authorization") != want {
		t.Fatalf("Authorization = %q, want %q", got.header.Get("Authorization"), want)
	}
	if got.header.Get("X-Api-Key") != "" {
		t.Fatal("an API key was sent alongside the subscription grant")
	}
	if got.header.Get("User-Agent") != userAgent {
		t.Fatalf("User-Agent = %q, want the client fingerprint %q", got.header.Get("User-Agent"), userAgent)
	}
}

// Subscription traffic must reach the beta resource; the plain resource
// refuses a subscription grant.
func TestTransportTargetsBetaResource(t *testing.T) {
	got := roundTrip(t, newTransport(), `{"model":"m","messages":[]}`)
	if !strings.Contains(got.url, "beta=true") {
		t.Fatalf("request URL = %q, want beta=true", got.url)
	}
}

// Every fingerprint header must be present with its exact value; a missing or
// altered one is what the provider rejects.
func TestTransportStampsCompleteFingerprint(t *testing.T) {
	got := roundTrip(t, newTransport(), `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	for key, want := range map[string]string{
		"Accept":            "application/json",
		"Content-Type":      "application/json",
		"anthropic-version": apiVersion,
		"anthropic-dangerous-direct-browser-access": "true",
		"x-app":                       "cli",
		"X-Stainless-Lang":            "js",
		"X-Stainless-OS":              "Linux",
		"X-Stainless-Package-Version": "0.94.0",
		"X-Stainless-Retry-Count":     "0",
		"X-Stainless-Runtime":         "node",
		"X-Stainless-Runtime-Version": "v26.3.0",
		"X-Stainless-Timeout":         "600",
	} {
		if have := got.header.Get(key); have != want {
			t.Errorf("%s = %q, want %q", key, have, want)
		}
	}
	if got.header.Get("x-client-request-id") == "" {
		t.Error("x-client-request-id was not stamped")
	}
}

// The beta list selects on whether the request carries tools or thinking; the
// value is part of the fingerprint, so both variants must be exact.
func TestTransportSelectsBetaListByRequestShape(t *testing.T) {
	utility := roundTrip(t, newTransport(), `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if got, want := utility.header.Get("anthropic-beta"), joinBetas(utilityBetas); got != want {
		t.Fatalf("utility beta = %q, want %q", got, want)
	}

	agent := roundTrip(t, newTransport(),
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read"}]}`)
	wantAgent := joinBetas(append(append([]string{}, agentBetas...), fallbackCreditBeta))
	if got := agent.header.Get("anthropic-beta"); got != wantAgent {
		t.Fatalf("agent beta = %q, want %q", got, wantAgent)
	}

	thinking := roundTrip(t, newTransport(),
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled"}}`)
	wantThinking := joinBetas(append(append([]string{}, agentBetas...), effortBeta, fallbackCreditBeta))
	if got := thinking.header.Get("anthropic-beta"); got != wantThinking {
		t.Fatalf("thinking beta = %q, want %q", got, wantThinking)
	}
}

// The long-context beta must never be advertised: a subscription grant has no
// long-context credit and the provider hard-refuses the request.
func TestTransportNeverAdvertisesLongContextBeta(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read"}],"thinking":{"type":"enabled"}}`,
	} {
		got := roundTrip(t, newTransport(), body)
		if strings.Contains(got.header.Get("anthropic-beta"), "context-1m") {
			t.Fatalf("long-context beta advertised: %q", got.header.Get("anthropic-beta"))
		}
	}
}

// system[0] must be the billing block and system[1] the identity block, with
// the caller's own prompt following. The order is what the attestation anchors
// on.
func TestTransportPrependsBillingThenIdentityBlocks(t *testing.T) {
	got := roundTrip(t, newTransport(),
		`{"model":"m","messages":[{"role":"user","content":"hello there"}],"system":"caller prompt"}`)

	var blocks []systemBlock
	if err := json.Unmarshal(decodeBody(t, got.body)["system"], &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 {
		t.Fatalf("system blocks = %d, want 3", len(blocks))
	}
	if !strings.HasPrefix(blocks[0].Text, billingHeaderPrefix) {
		t.Fatalf("system[0] = %q, want the billing block", blocks[0].Text)
	}
	if !strings.Contains(blocks[0].Text, "cc_entrypoint=claude-desktop") {
		t.Fatalf("billing block lacks the entrypoint: %q", blocks[0].Text)
	}
	if !strings.Contains(blocks[0].Text, "cc_version="+ClientVersion+".") {
		t.Fatalf("billing block lacks the versioned fingerprint: %q", blocks[0].Text)
	}
	if blocks[1].Text != identityInstruction {
		t.Fatalf("system[1] = %q, want the identity block", blocks[1].Text)
	}
	if blocks[2].Text != "caller prompt" {
		t.Fatalf("system[2] = %q, want the caller prompt preserved", blocks[2].Text)
	}
}

// A retried or resumed request already carrying the billing block must not
// accumulate a second one.
func TestTransportDoesNotDuplicateBillingBlock(t *testing.T) {
	first := roundTrip(t, newTransport(),
		`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	second := roundTrip(t, newTransport(), string(first.body))

	var blocks []systemBlock
	if err := json.Unmarshal(decodeBody(t, second.body)["system"], &blocks); err != nil {
		t.Fatal(err)
	}
	billing := 0
	for _, block := range blocks {
		if strings.HasPrefix(block.Text, billingHeaderPrefix) {
			billing++
		}
	}
	if billing != 1 {
		t.Fatalf("billing blocks = %d, want exactly 1", billing)
	}
}

// The attestation must be computed over the final bytes and be deterministic
// for identical input; the placeholder must never ship unpatched.
func TestTransportPatchesAttestationDeterministically(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"hello there friend"}]}`
	first := roundTrip(t, newTransport(), body)
	second := roundTrip(t, newTransport(), body)

	if bytes.Contains(first.body, []byte(cchPlaceholder)) {
		t.Fatal("the attestation placeholder shipped unpatched")
	}
	firstBlocks, secondBlocks := billingText(t, first.body), billingText(t, second.body)
	if firstBlocks != secondBlocks {
		t.Fatalf("attestation is not deterministic:\n%s\n%s", firstBlocks, secondBlocks)
	}
	if !strings.Contains(firstBlocks, "cch=") {
		t.Fatalf("billing block lost its attestation field: %q", firstBlocks)
	}
	digits := firstBlocks[strings.Index(firstBlocks, "cch=")+4:]
	digits = digits[:cchDigits]
	if strings.Trim(digits, "0123456789abcdef") != "" {
		t.Fatalf("attestation digits are not lowercase hex: %q", digits)
	}
}

// Changing the body must change the attestation; otherwise it is not attesting
// anything.
func TestAttestationCoversBodyBytes(t *testing.T) {
	one := roundTrip(t, newTransport(), `{"model":"m","messages":[{"role":"user","content":"first body"}]}`)
	two := roundTrip(t, newTransport(), `{"model":"m","messages":[{"role":"user","content":"second body"}]}`)
	if billingText(t, one.body) == billingText(t, two.body) {
		t.Fatal("attestation did not change with the body")
	}
}

func billingText(t *testing.T, raw []byte) string {
	t.Helper()
	var blocks []systemBlock
	if err := json.Unmarshal(decodeBody(t, raw)["system"], &blocks); err != nil {
		t.Fatal(err)
	}
	for _, block := range blocks {
		if strings.HasPrefix(block.Text, billingHeaderPrefix) {
			return block.Text
		}
	}
	t.Fatal("no billing block present")
	return ""
}

// The provider refuses a larger ceiling on a subscription grant, and a smaller
// caller request must still be honoured.
func TestTransportClampsMaxTokens(t *testing.T) {
	for body, want := range map[string]int64{
		`{"model":"m","messages":[],"max_tokens":200000}`: MaxOutputTokens,
		`{"model":"m","messages":[]}`:                     MaxOutputTokens,
		`{"model":"m","messages":[],"max_tokens":1024}`:   1024,
	} {
		got := roundTrip(t, newTransport(), body)
		var ceiling int64
		if err := json.Unmarshal(decodeBody(t, got.body)["max_tokens"], &ceiling); err != nil {
			t.Fatal(err)
		}
		if ceiling != want {
			t.Fatalf("max_tokens for %s = %d, want %d", body, ceiling, want)
		}
	}
}

// Caller tools are namespaced away from the client's built-ins, which are left
// alone; an empty tools array is always present.
func TestTransportNamespacesCallerToolsOnly(t *testing.T) {
	got := roundTrip(t, newTransport(),
		`{"model":"m","messages":[],"tools":[{"name":"read"},{"name":"web_search"},{"name":"_already"}]}`)

	var tools []struct{ Name string }
	if err := json.Unmarshal(decodeBody(t, got.body)["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	want := []string{"_read", "web_search", "_already"}
	if len(tools) != len(want) {
		t.Fatalf("tools = %d, want %d", len(tools), len(want))
	}
	for i, name := range want {
		if tools[i].Name != name {
			t.Errorf("tools[%d] = %q, want %q", i, tools[i].Name, name)
		}
	}

	empty := roundTrip(t, newTransport(), `{"model":"m","messages":[]}`)
	if got := string(decodeBody(t, empty.body)["tools"]); got != "[]" {
		t.Fatalf("tools = %s, want an empty array", got)
	}
}

// metadata.user_id must be the client-shaped JSON document, carrying the
// account and a stable device id.
func TestTransportStampsClientShapedUserID(t *testing.T) {
	got := roundTrip(t, newTransport(), `{"model":"m","messages":[]}`)

	var metadata struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(decodeBody(t, got.body)["metadata"], &metadata); err != nil {
		t.Fatal(err)
	}
	var userID map[string]string
	if err := json.Unmarshal([]byte(metadata.UserID), &userID); err != nil {
		t.Fatalf("user_id is not a JSON document: %v (%q)", err, metadata.UserID)
	}
	if userID["account_uuid"] != "acct-canary" {
		t.Fatalf("account_uuid = %q", userID["account_uuid"])
	}
	if len(userID["device_id"]) != 64 {
		t.Fatalf("device_id = %q, want a 64-char hash", userID["device_id"])
	}
	if userID["session_id"] == "" {
		t.Fatal("session_id was not stamped")
	}
}

// The device id identifies one installation, so it must not change per
// process-local request; a fresh value each time inflates the provider's
// device count for a single install.
func TestDeviceIDIsStablePerInstallAndAccount(t *testing.T) {
	SetInstallID("install-canary")
	first, second := deriveDeviceID("acct-a"), deriveDeviceID("acct-a")
	if first != second {
		t.Fatal("device id is not stable for one account")
	}
	if deriveDeviceID("acct-b") == first {
		t.Fatal("device id does not vary by account")
	}
	if strings.Contains(first, "install-canary") {
		t.Fatal("device id leaks the install identity verbatim")
	}
}

// A credential source failure must abort before any request reaches the
// provider.
func TestTransportFailsClosedWithoutCredential(t *testing.T) {
	transport := &Transport{Source: staticSource{err: errors.New("no grant")}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		"https://api.anthropic.com/v1/messages", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("a request was sent without a credential")
	}

	bare := &Transport{}
	if _, err := bare.RoundTrip(req); !errors.Is(err, errNoTransportSource) {
		t.Fatalf("error = %v, want errNoTransportSource", err)
	}
}
