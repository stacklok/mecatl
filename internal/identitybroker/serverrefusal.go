package identitybroker

import (
	"errors"
	"fmt"
	"strings"
)

// unknownCode stands in for an error code or reason code that is present but
// not one the broker knows, so a server cannot place arbitrary text, such as
// a token, in a refusal.
const unknownCode = "unknown"

// knownErrorCodes are the OAuth error codes the broker may receive from AS-1
// and AS-2: RFC 6749 §5.2 and its server_error and temporarily_unavailable,
// RFC 8693 invalid_target, RFC 9396 invalid_authorization_details, and
// RFC 7009 unsupported_token_type.
var knownErrorCodes = map[string]bool{
	"invalid_request": true, "invalid_client": true, "invalid_grant": true,
	"unauthorized_client": true, "unsupported_grant_type": true, "invalid_scope": true,
	"server_error": true, "temporarily_unavailable": true,
	"invalid_target":                true,
	"invalid_authorization_details": true,
	"unsupported_token_type":        true,
}

// knownReasons are the reason codes of the design's error reasons
// (docs/drafts/agent-identity/credentials-and-lifecycle.md).
var knownReasons = map[string]bool{
	"subject_invalid": true, "token_used": true, "owner_mismatch": true,
	"session_owned": true, "session_closed": true, "signed_out": true,
	"upstream_revoked": true, "schedule_ended": true, "owner_action_required": true,
	"superseded": true, "svid_expired": true, "denied": true,
	"consent_required": true, "invalid_assertion": true,
}

// transientStatuses are HTTP statuses that mean the request may succeed
// later. The design's error reasons name only 503; 429, 502, and 504 are a
// deliberate extension, since a rate limit or a proxy's bad or timed-out
// gateway is not an OAuth refusal, whatever its body.
var transientStatuses = map[int]bool{429: true, 502: true, 503: true, 504: true}

// ErrNoResponse reports a token or revocation endpoint request to AS-1 or
// AS-2 that got no response, such as a dropped connection: the server may or
// may not have processed it. Callers may retry once, with a new client
// assertion.
var ErrNoResponse = errors.New("identitybroker: no response from the authorization server")

// ServerRefusal is AS-1's or AS-2's refusal of a token or revocation endpoint
// request: the HTTP status and, from an RFC 6749 §5.2 error body, the error
// code and the reason code that starts error_description (design "Error
// reasons"). Code and Reason are empty when absent and "unknown" when
// present but not a code the broker knows, so a server cannot place
// arbitrary text, such as a token, in the error.
type ServerRefusal struct {
	Status int
	Code   string
	Reason string
}

// NewServerRefusal returns the refusal for status and a response's error and
// error_description members, keeping the code and the leading reason code
// (description "reason" or "reason: text") only when each is known, and
// nothing else.
func NewServerRefusal(status int, code, description string) *ServerRefusal {
	reason, _, _ := strings.Cut(description, ":")
	return &ServerRefusal{Status: status, Code: known(knownErrorCodes, code), Reason: known(knownReasons, reason)}
}

// known returns s when it is in set, "" when it is empty, and "unknown"
// otherwise.
func known(set map[string]bool, s string) string {
	switch {
	case s == "" || set[s]:
		return s
	default:
		return unknownCode
	}
}

func (r *ServerRefusal) Error() string {
	msg := fmt.Sprintf("identitybroker: the authorization server refused the request: HTTP %d", r.Status)
	if c := known(knownErrorCodes, r.Code); c != "" {
		msg += " " + c
	}
	if reason := known(knownReasons, r.Reason); reason != "" {
		msg += " (" + reason + ")"
	}
	return msg
}

// IsConfigurationFault reports whether err is a refusal the design's error
// reasons treat as a configuration fault or broker bug (gRPC INTERNAL):
// invalid_client, unauthorized_client, or invalid_target, or any other
// refusal without a reason code. A refusal with a transient status (see
// IsUnavailable) never is. A failed client authentication is always
// invalid_client. A caller needing a reason-specific outcome, such as an
// AS-2 invalid_grant with no reason, checks the ServerRefusal first.
func IsConfigurationFault(err error) bool {
	var r *ServerRefusal
	if !errors.As(err, &r) || transientStatuses[r.Status] {
		return false
	}
	switch r.Code {
	case "invalid_client", "unauthorized_client", "invalid_target":
		return true
	}
	return r.Reason == ""
}

// IsUnavailable reports whether err is a retryable infrastructure failure
// (gRPC UNAVAILABLE), not a denial: ErrNoResponse, or a refusal with HTTP
// status 503, as the design names, or 429, 502, or 504, whatever its body.
func IsUnavailable(err error) bool {
	var r *ServerRefusal
	if errors.As(err, &r) {
		return transientStatuses[r.Status]
	}
	return errors.Is(err, ErrNoResponse)
}
