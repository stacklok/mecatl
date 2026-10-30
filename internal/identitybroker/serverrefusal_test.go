package identitybroker

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNewServerRefusalKeepsOnlyKnownCodes(t *testing.T) {
	tests := []struct {
		code, description string
		want              ServerRefusal
	}{
		{"invalid_request", "session_closed", ServerRefusal{400, "invalid_request", "session_closed"}},
		{"invalid_request", "session_closed: session ended", ServerRefusal{400, "invalid_request", "session_closed"}},
		{"invalid_grant", "", ServerRefusal{400, "invalid_grant", ""}},
		{"invalid_authorization_details", "session_owned", ServerRefusal{400, "invalid_authorization_details", "session_owned"}},
		{"unsupported_token_type", "", ServerRefusal{400, "unsupported_token_type", ""}},
		{"invalid_request", "subject_token_invalid", ServerRefusal{400, "invalid_request", "subject_token_invalid"}},
		{"invalid_request", "subject_token_audience", ServerRefusal{400, "invalid_request", "subject_token_audience"}},
		{"invalid_request", "subject_token_source_client: rogue", ServerRefusal{400, "invalid_request", "subject_token_source_client"}},
		{"invalid_request", "grant_invalid", ServerRefusal{400, "invalid_request", "grant_invalid"}},
		{"invalid_request", "the token eyJhbGciOi.x.y was refused", ServerRefusal{400, "invalid_request", "unknown"}},
		{"eyJhbGciOi.x.y", "Session_Closed", ServerRefusal{400, "unknown", "unknown"}},
		{"access_denied", "", ServerRefusal{400, "unknown", ""}},
		{"", "", ServerRefusal{400, "", ""}},
	}
	for _, tt := range tests {
		if got := NewServerRefusal(400, tt.code, tt.description); *got != tt.want {
			t.Errorf("(%q, %q): got %+v, want %+v", tt.code, tt.description, *got, tt.want)
		}
	}
}

// TestServerRefusalHidesOpaqueCodes: a lowercase, underscore-only opaque
// value, which an alphabet filter would pass, never reaches the fields or
// Error, as the code or as the reason.
func TestServerRefusalHidesOpaqueCodes(t *testing.T) {
	const opaque = "rt_abcdefghijklmnopqrstuvwxyz_secret"
	for _, r := range []*ServerRefusal{
		NewServerRefusal(400, opaque, ""),
		NewServerRefusal(400, "invalid_request", opaque),
		NewServerRefusal(400, "invalid_request", opaque+": more"),
	} {
		if strings.Contains(r.Code+r.Reason+r.Error(), opaque) {
			t.Errorf("%+v, Error() = %q discloses the value", *r, r.Error())
		}
	}
	// Built directly, bypassing NewServerRefusal.
	direct := &ServerRefusal{Status: 401, Code: opaque, Reason: "invalid_client eyJsecret"}
	if got := direct.Error(); strings.Contains(got, opaque) || strings.Contains(got, "eyJsecret") || !strings.Contains(got, "HTTP 401 unknown (unknown)") {
		t.Fatalf("Error() = %q", got)
	}
	if got := (&ServerRefusal{Status: 400, Code: "invalid_request", Reason: "session_closed"}).Error(); !strings.Contains(got, "HTTP 400 invalid_request (session_closed)") {
		t.Fatalf("Error() = %q", got)
	}
}

func TestServerRefusalClassification(t *testing.T) {
	tests := []struct {
		name                string
		err                 error
		config, unavailable bool
	}{
		{"invalid_client", &ServerRefusal{401, "invalid_client", ""}, true, false},
		{"invalid_client with a reason", &ServerRefusal{401, "invalid_client", "denied"}, true, false},
		{"unauthorized_client with a reason", &ServerRefusal{400, "unauthorized_client", "denied"}, true, false},
		{"invalid_target with a reason", &ServerRefusal{400, "invalid_target", "denied"}, true, false},
		{"no reason", &ServerRefusal{400, "invalid_request", ""}, true, false},
		{"500 no body", &ServerRefusal{500, "", ""}, true, false},
		{"500 server_error", &ServerRefusal{500, "server_error", ""}, false, true},
		{"400 server_error with a reason", &ServerRefusal{400, "server_error", "denied"}, false, true},
		{"500 temporarily_unavailable", &ServerRefusal{500, "temporarily_unavailable", ""}, false, true},
		{"400 temporarily_unavailable", NewServerRefusal(400, "temporarily_unavailable", "try later"), false, true},
		{"redirect", &ServerRefusal{307, "", ""}, true, false},
		{"reason for the caller", &ServerRefusal{400, "invalid_request", "session_closed"}, false, false},
		{"revoked grant", NewServerRefusal(400, "invalid_request", "revoked"), false, false},
		{"revoked parent grant", NewServerRefusal(400, "invalid_request", "parent_revoked: parent grant revoked"), false, false},
		{"not issuable", NewServerRefusal(400, "invalid_request", "not_issuable"), false, false},
		{"token used", NewServerRefusal(400, "invalid_request", "token_used"), false, false},
		{"subject token invalid", NewServerRefusal(400, "invalid_request", "subject_token_invalid"), false, false},
		{"subject token audience", NewServerRefusal(400, "invalid_request", "subject_token_audience"), true, false},
		{"subject token source client", NewServerRefusal(400, "invalid_request", "subject_token_source_client: not a public client"), true, false},
		{"grant invalid", NewServerRefusal(400, "invalid_request", "grant_invalid"), true, false},
		{"503 grant invalid", NewServerRefusal(503, "invalid_request", "grant_invalid"), false, true},
		{"superseded is no longer a reason", NewServerRefusal(400, "invalid_request", "superseded"), true, false},
		{"unknown reason is no reason", NewServerRefusal(400, "invalid_request", "brand_new_reason"), true, false},
		{"prose description is no reason", NewServerRefusal(400, "invalid_grant", "The provided authorization grant is invalid"), true, false},
		{"directly built unknown reason", &ServerRefusal{400, "invalid_request", "unknown"}, true, false},
		{"transport configuration", fmt.Errorf("%w: tls", ErrTransportConfiguration), true, false},
		{"unknown code, no reason", NewServerRefusal(400, "brand_new_code", ""), true, false},
		{"429", &ServerRefusal{429, "", ""}, false, true},
		{"429 invalid_request", &ServerRefusal{429, "invalid_request", ""}, false, true},
		{"502", &ServerRefusal{502, "", ""}, false, true},
		{"502 invalid_client", &ServerRefusal{502, "invalid_client", ""}, false, true},
		{"503", &ServerRefusal{503, "", ""}, false, true},
		{"503 temporarily_unavailable", &ServerRefusal{503, "temporarily_unavailable", ""}, false, true},
		{"503 invalid_client", &ServerRefusal{503, "invalid_client", ""}, false, true},
		{"504", &ServerRefusal{504, "", ""}, false, true},
		{"504 unauthorized_client with a reason", &ServerRefusal{504, "unauthorized_client", "denied"}, false, true},
		{"wrapped 503", fmt.Errorf("open: %w", &ServerRefusal{503, "", ""}), false, true},
		{"no response", fmt.Errorf("%w: timeout", ErrNoResponse), false, true},
		{"other error", errors.New("boom"), false, false},
	}
	for _, tt := range tests {
		if got := IsConfigurationFault(tt.err); got != tt.config {
			t.Errorf("%s: IsConfigurationFault = %v, want %v", tt.name, got, tt.config)
		}
		if got := IsUnavailable(tt.err); got != tt.unavailable {
			t.Errorf("%s: IsUnavailable = %v, want %v", tt.name, got, tt.unavailable)
		}
	}
}
