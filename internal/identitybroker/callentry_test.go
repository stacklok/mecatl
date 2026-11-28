package identitybroker_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// TestAuthorizeCallSendsTheCallEntry: the broker asks AS-1 for an ID-JAG
// carrying one call entry for every gateway call: a tool-call entry naming
// the server, the tool as sent, the argument hash, and the operation for a
// tools/call; a list-call entry naming the server and method for a
// tools/list (credentials-and-lifecycle.md, steps (7) and (8)).
func TestAuthorizeCallSendsTheCallEntry(t *testing.T) {
	call := func(f perCallFixture) identitybroker.AuthorizeCallRequest { return callRequest(f.s.ID, f.cred) }
	list := func(f perCallFixture) identitybroker.AuthorizeCallRequest {
		req := callRequest(f.s.ID, f.cred)
		req.Method, req.Tool, req.ArgumentsSHA256 = identitybroker.MethodToolsList, "", ""
		return req
	}
	for name, c := range map[string]struct {
		req  func(perCallFixture) identitybroker.AuthorizeCallRequest
		want []any
	}{
		"tools/call": {call, []any{map[string]any{
			"type": identitybroker.CallEntryType, "server": "github", "tool": "github.pull_request_read",
			"arguments_sha256": strings.Repeat("ab", 32), "operation_id": "op-1",
		}}},
		"tools/list": {list, []any{map[string]any{"type": identitybroker.CallEntryType, "server": "github", "method": "tools/list"}}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPerCall(t)
			if _, err := f.authorize(c.req(f)); err != nil {
				t.Fatalf("AuthorizeCall: %v", err)
			}
			reqs := f.issuer.requests()
			var sent []any
			if len(reqs) != 1 || json.Unmarshal(reqs[0].AuthorizationDetails, &sent) != nil || !reflect.DeepEqual(sent, c.want) {
				t.Fatalf("the ID-JAG exchange carried %s, want %v", reqs[0].AuthorizationDetails, c.want)
			}
			if d := f.rec.Decisions(); len(d) != 1 || d[0].CallEntryCheck != identitybroker.CallEntryEqual {
				t.Fatalf("decisions %+v, want one whose call entry check is equal", d)
			}
		})
	}
}

// TestAuthorizeCallChecksTheGatewayTokensCallEntry is step (8a): the broker
// compares the authorization_details of AS-2's token response with the entry
// it sent, as decoded JSON values. A response without it fails the call with
// call_entry_missing, and one with any other value with call_entry_mismatch,
// both INTERNAL, before the token reaches the gateway, recorded with the
// check's result and the token's reference, and never retried.
func TestAuthorizeCallChecksTheGatewayTokensCallEntry(t *testing.T) {
	// entry is the entry the broker sends for callRequest, as AS-2 might
	// write it, with edit applied.
	entry := func(edit func(map[string]any)) json.RawMessage {
		e := map[string]any{
			"type": identitybroker.CallEntryType, "server": "github", "tool": "github.pull_request_read",
			"arguments_sha256": strings.Repeat("ab", 32), "operation_id": "op-1",
		}
		if edit != nil {
			edit(e)
		}
		raw, err := json.Marshal([]any{e})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	withEntry := func(raw json.RawMessage) func(json.RawMessage) json.RawMessage {
		return func(json.RawMessage) json.RawMessage { return raw }
	}
	for name, c := range map[string]struct {
		details func(signed json.RawMessage) json.RawMessage
		reason  identitybroker.Reason
		check   identitybroker.CallEntryCheck
	}{
		"the entry, reordered and spaced": {
			withEntry(json.RawMessage(` [ {"operation_id":"op-1", "arguments_sha256":"` + strings.Repeat("ab", 32) + `","tool":"github.pull_request_read","server":"github","type":"` + identitybroker.CallEntryType + `"} ] `)),
			"", identitybroker.CallEntryEqual,
		},
		"a dropped claim":  {withEntry(nil), identitybroker.ReasonCallEntryMissing, identitybroker.CallEntryAbsent},
		"a null claim":     {withEntry(json.RawMessage(`null`)), identitybroker.ReasonCallEntryMissing, identitybroker.CallEntryAbsent},
		"an empty array":   {withEntry(json.RawMessage(`[]`)), identitybroker.ReasonCallEntryMismatch, identitybroker.CallEntryDiffers},
		"a different hash": {withEntry(entry(func(e map[string]any) { e["arguments_sha256"] = strings.Repeat("cd", 32) })), identitybroker.ReasonCallEntryMismatch, identitybroker.CallEntryDiffers},
		"a different op":   {withEntry(entry(func(e map[string]any) { e["operation_id"] = "op-2" })), identitybroker.ReasonCallEntryMismatch, identitybroker.CallEntryDiffers},
		"a different tool": {withEntry(entry(func(e map[string]any) { e["tool"] = "github.pull_request_merge" })), identitybroker.ReasonCallEntryMismatch, identitybroker.CallEntryDiffers},
		"an extra member":  {withEntry(entry(func(e map[string]any) { e["locations"] = []any{"x"} })), identitybroker.ReasonCallEntryMismatch, identitybroker.CallEntryDiffers},
		"a list entry":     {withEntry(json.RawMessage(`[{"type":"` + identitybroker.CallEntryType + `","server":"github","method":"tools/list"}]`)), identitybroker.ReasonCallEntryMismatch, identitybroker.CallEntryDiffers},
		"an additional entry": {func(signed json.RawMessage) json.RawMessage {
			return json.RawMessage(`[` + string(signed[1:len(signed)-1]) + `,{"type":"other"}]`)
		}, identitybroker.ReasonCallEntryMismatch, identitybroker.CallEntryDiffers},
		"the entry, not in an array": {func(signed json.RawMessage) json.RawMessage { return signed[1 : len(signed)-1] }, identitybroker.ReasonCallEntryMismatch, identitybroker.CallEntryDiffers},
		"a repeated member":          {withEntry(json.RawMessage(`[{"type":"` + identitybroker.CallEntryType + `","server":"other","server":"github","tool":"github.pull_request_read","arguments_sha256":"` + strings.Repeat("ab", 32) + `","operation_id":"op-1"}]`)), identitybroker.ReasonCallEntryMismatch, identitybroker.CallEntryDiffers},
		"not JSON":                   {withEntry(json.RawMessage(`[{`)), identitybroker.ReasonCallEntryMismatch, identitybroker.CallEntryDiffers},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPerCall(t)
			f.as2.details = c.details
			got, err := f.authorize(callRequest(f.s.ID, f.cred))
			decisions := f.rec.Decisions()
			if len(decisions) != 1 {
				t.Fatalf("recorded %d decisions, want 1", len(decisions))
			}
			d := decisions[0]
			if d.CallEntryCheck != c.check || d.GatewayToken.ID == "" {
				t.Errorf("recorded check %q and token %+v, want %q and the token's reference", d.CallEntryCheck, d.GatewayToken, c.check)
			}
			if c.reason == "" {
				if err != nil || got.GatewayToken.IsZero() || d.Outcome != identitybroker.OutcomeSuccess {
					t.Fatalf("AuthorizeCall = %v, %v, decision %s; want a gateway token", got, err, d.Outcome)
				}
				return
			}
			tokens := f.as2.tokens()
			wantError(t, err, identitybroker.CodeInternal, c.reason, tokens[0])
			if !got.GatewayToken.IsZero() {
				t.Fatal("a token that failed the output check was returned")
			}
			f.wantCounts(t, 1, 1)
			if d.Outcome != identitybroker.OutcomeError || d.Reason != c.reason {
				t.Errorf("recorded %s %q, want an error with %q", d.Outcome, d.Reason, c.reason)
			}
			// The attempt was never issued, so it cannot be reported rejected.
			report := callRequest(f.s.ID, f.cred)
			report.RejectedAttemptID = d.AttemptID
			_, err = f.authorize(report)
			wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied)
		})
	}
}
