package identitybroker

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNewServerRefusalKeepsOnlyCodes(t *testing.T) {
	tests := []struct {
		code, description string
		want              ServerRefusal
	}{
		{"invalid_request", "session_closed", ServerRefusal{400, "invalid_request", "session_closed"}},
		{"invalid_request", "session_closed: session ended", ServerRefusal{400, "invalid_request", "session_closed"}},
		{"invalid_grant", "", ServerRefusal{400, "invalid_grant", ""}},
		{"invalid_request", "the token eyJhbGciOi.x.y was refused", ServerRefusal{400, "invalid_request", ""}},
		{"eyJhbGciOi.x.y", "Session_Closed", ServerRefusal{400, "", ""}},
		{"", "", ServerRefusal{400, "", ""}},
	}
	for _, tt := range tests {
		if got := NewServerRefusal(400, tt.code, tt.description); *got != tt.want {
			t.Errorf("(%q, %q): got %+v, want %+v", tt.code, tt.description, *got, tt.want)
		}
	}
}

// TestServerRefusalErrorShowsOnlyCodes checks Error on a refusal built
// directly, bypassing NewServerRefusal's sanitizing.
func TestServerRefusalErrorShowsOnlyCodes(t *testing.T) {
	err := &ServerRefusal{Status: 401, Code: "invalid_client eyJsecret", Reason: "eyJsecret"}
	if strings.Contains(err.Error(), "eyJsecret") {
		t.Fatalf("Error() = %q discloses a value", err.Error())
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
		{"no body", &ServerRefusal{502, "", ""}, true, false},
		{"redirect", &ServerRefusal{307, "", ""}, true, false},
		{"reason for the caller", &ServerRefusal{400, "invalid_request", "session_closed"}, false, false},
		{"503", &ServerRefusal{503, "temporarily_unavailable", ""}, false, true},
		{"503 invalid_client", &ServerRefusal{503, "invalid_client", ""}, false, true},
		{"wrapped 503", fmt.Errorf("open: %w", &ServerRefusal{503, "", ""}), false, true},
		{"no response", fmt.Errorf("%w: EOF", ErrNoResponse), false, true},
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
