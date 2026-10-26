package identitybroker

import (
	"errors"
	"fmt"
	"strings"
)

// statusServiceUnavailable is HTTP 503, which the design's error reasons
// treat as a retryable infrastructure failure.
const statusServiceUnavailable = 503

// ErrNoResponse reports a token or revocation endpoint request to AS-1 or
// AS-2 that got no response, such as a dropped connection: the server may or
// may not have processed it. Callers may retry once, with a new client
// assertion.
var ErrNoResponse = errors.New("identitybroker: no response from the authorization server")

// ServerRefusal is AS-1's or AS-2's refusal of a token or revocation endpoint
// request: the HTTP status and, from an RFC 6749 §5.2 error body, the error
// code and the reason code that starts error_description (design "Error
// reasons"). Code and Reason hold only lowercase letters and underscores, so
// a server cannot place arbitrary text, such as a token, in the error.
type ServerRefusal struct {
	Status int
	Code   string
	Reason string
}

// NewServerRefusal returns the refusal for status and a response's error and
// error_description members, keeping the code and the leading reason code
// (description "reason" or "reason: text") only when each is a well-formed
// code, and nothing else.
func NewServerRefusal(status int, code, description string) *ServerRefusal {
	reason, _, _ := strings.Cut(description, ":")
	return &ServerRefusal{Status: status, Code: symbol(code), Reason: symbol(reason)}
}

// symbol returns s when it is a code of lowercase letters and underscores,
// and "" otherwise.
func symbol(s string) string {
	if s == "" || len(s) > 64 || strings.Trim(s, "abcdefghijklmnopqrstuvwxyz_") != "" {
		return ""
	}
	return s
}

func (r *ServerRefusal) Error() string {
	msg := fmt.Sprintf("identitybroker: the authorization server refused the request: HTTP %d", r.Status)
	if c := symbol(r.Code); c != "" {
		msg += " " + c
	}
	if reason := symbol(r.Reason); reason != "" {
		msg += " (" + reason + ")"
	}
	return msg
}

// IsConfigurationFault reports whether err is a refusal the design's error
// reasons treat as a configuration fault or broker bug (gRPC INTERNAL):
// invalid_client, unauthorized_client, or invalid_target, or any other
// refusal without a reason code, except HTTP 503. A failed client
// authentication is always invalid_client. A caller needing a
// reason-specific outcome, such as an AS-2 invalid_grant with no reason,
// checks the ServerRefusal first.
func IsConfigurationFault(err error) bool {
	var r *ServerRefusal
	if !errors.As(err, &r) || r.Status == statusServiceUnavailable {
		return false
	}
	switch r.Code {
	case "invalid_client", "unauthorized_client", "invalid_target":
		return true
	}
	return r.Reason == ""
}

// IsUnavailable reports whether err is a retryable infrastructure failure
// (gRPC UNAVAILABLE), not a denial: an HTTP 503 refusal, or ErrNoResponse.
func IsUnavailable(err error) bool {
	var r *ServerRefusal
	if errors.As(err, &r) {
		return r.Status == statusServiceUnavailable
	}
	return errors.Is(err, ErrNoResponse)
}
