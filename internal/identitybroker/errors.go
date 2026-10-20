package identitybroker

import (
	"fmt"
	"io"
)

// Code classifies a Service error. The values mirror the gRPC status codes
// the agent identity design assigns, without importing gRPC.
type Code string

// Codes a Service error can carry.
const (
	CodeUnauthenticated    Code = "UNAUTHENTICATED"
	CodePermissionDenied   Code = "PERMISSION_DENIED"
	CodeInvalidArgument    Code = "INVALID_ARGUMENT"
	CodeFailedPrecondition Code = "FAILED_PRECONDITION"
	CodeAlreadyExists      Code = "ALREADY_EXISTS"
	CodeAborted            Code = "ABORTED"
	CodeUnavailable        Code = "UNAVAILABLE"
	CodeInternal           Code = "INTERNAL"
)

// Reason is a design-defined reason code that tells the caller what to do,
// such as opening a new broker session. Reasons are lowercase letters and
// underscores.
type Reason string

// Reasons a Service error can carry. Later work adds the design's other
// reasons as it returns them.
const (
	// ReasonSessionClosed: the broker session is closed; open a new one on the
	// user's next request.
	ReasonSessionClosed Reason = "session_closed"
	// ReasonSessionOwned: the host session's broker session belongs to another
	// owner.
	ReasonSessionOwned Reason = "session_owned"
	// ReasonDenied: the call is not allowed.
	ReasonDenied Reason = "denied"
)

// Code returns the code the design assigns to r.
func (r Reason) Code() Code {
	switch r {
	case ReasonSessionClosed:
		return CodeFailedPrecondition
	case ReasonSessionOwned, ReasonDenied:
		return CodePermissionDenied
	default:
		return CodeInternal
	}
}

// Error is the error every Service call returns. Detail is for diagnostics; it
// never contains credentials.
type Error struct {
	Code   Code
	Reason Reason
	Detail string
}

// NewError returns an *Error. reason may be empty.
func NewError(code Code, reason Reason, detail string) *Error {
	return &Error{Code: code, Reason: reason, Detail: detail}
}

// ReasonError returns an *Error for reason with the code the design assigns.
func ReasonError(reason Reason, detail string) *Error {
	return NewError(reason.Code(), reason, detail)
}

func (e *Error) Error() string {
	msg := "identitybroker: " + string(e.Code)
	if e.Reason != "" {
		msg += " " + string(e.Reason)
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Is reports whether target is an *Error with the same code and, when target
// names one, the same reason, so errors.Is(err, &Error{Code: c}) matches by code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return t.Code == e.Code && (t.Reason == "" || t.Reason == e.Reason)
}

// Token is a bearer credential: an AS-1 access token, an agent credential, or
// a gateway token. Its value is only available through Value; formatting a
// Token with any verb prints a redacted placeholder.
type Token struct {
	value string
}

// NewToken returns a Token holding value.
func NewToken(value string) Token { return Token{value: value} }

// Value returns the credential. Callers pass it only to the party that
// consumes it, never to logs, events, or the model.
func (t Token) Value() string { return t.value }

// IsZero reports whether t holds no credential.
func (t Token) IsZero() bool { return t.value == "" }

func (t Token) String() string {
	if t.IsZero() {
		return "identitybroker.Token(empty)"
	}
	return "identitybroker.Token(redacted)"
}

// Format prints String for every verb, so no verb reaches the value.
func (t Token) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, t.String())
}
