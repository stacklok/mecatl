package identitybroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestTokenNeverFormatsItsValue(t *testing.T) {
	const secret = "eyJhbGciOiJFUzI1NiJ9.secret"
	tok := NewToken(secret)
	if tok.Value() != secret {
		t.Fatalf("Value = %q, want the credential", tok.Value())
	}
	req := AuthorizeCallRequest{SessionID: "A42", Credential: tok}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		for _, operand := range []any{tok, req, &req} {
			if got := fmt.Sprintf(verb, operand); strings.Contains(got, secret) || strings.Contains(got, fmt.Sprintf("%x", secret)) {
				t.Errorf("Sprintf(%s, %T) = %q leaks the credential", verb, operand, got)
			}
		}
	}
	if data, err := json.Marshal(req); err != nil || strings.Contains(string(data), secret) {
		t.Errorf("json.Marshal = %s, %v; leaks the credential", data, err)
	}
}

func TestErrorMatchesByCodeAndReason(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", ReasonError(ReasonSessionClosed, "closed"))
	cases := []struct {
		target *Error
		want   bool
	}{
		{&Error{Code: CodeFailedPrecondition}, true},
		{&Error{Code: CodeFailedPrecondition, Reason: ReasonSessionClosed}, true},
		{&Error{Code: CodeFailedPrecondition, Reason: ReasonDenied}, false},
		{&Error{Code: CodePermissionDenied}, false},
	}
	for _, c := range cases {
		if got := errors.Is(err, c.target); got != c.want {
			t.Errorf("errors.Is(%v, %s %q) = %v, want %v", err, c.target.Code, c.target.Reason, got, c.want)
		}
	}
	if got, want := err.Error(), "wrapped: identitybroker: FAILED_PRECONDITION session_closed: closed"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestReasonsCarryTheDesignCodes(t *testing.T) {
	want := map[Reason]Code{
		ReasonSessionClosed: CodeFailedPrecondition,
		ReasonSessionOwned:  CodePermissionDenied,
		ReasonDenied:        CodePermissionDenied,
		Reason("unknown"):   CodeInternal,
	}
	for reason, code := range want {
		if got := reason.Code(); got != code {
			t.Errorf("%s.Code() = %s, want %s", reason, got, code)
		}
	}
}

func TestCallerTravelsWithTheContext(t *testing.T) {
	if _, ok := CallerFrom(context.Background()); ok {
		t.Fatal("CallerFrom found a caller on an empty context")
	}
	want := Caller{Workload: "system:serviceaccount:mecatl:mecak8s"}
	got, ok := CallerFrom(WithCaller(context.Background(), want))
	if !ok || got != want {
		t.Fatalf("CallerFrom = %+v, %v; want %+v, true", got, ok, want)
	}
}
