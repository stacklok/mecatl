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
		ReasonSVIDExpired:   CodeUnauthenticated,
		Reason("unknown"):   CodeInternal,
	}
	for reason, code := range want {
		if got := reason.Code(); got != code {
			t.Errorf("%s.Code() = %s, want %s", reason, got, code)
		}
	}
}

// recordingCore records the workload each call receives.
type recordingCore struct {
	workloads []Workload
}

func (c *recordingCore) OpenSession(_ context.Context, workload Workload, _ OpenSessionRequest) (Session, error) {
	c.workloads = append(c.workloads, workload)
	return Session{}, nil
}

func (c *recordingCore) FindSession(_ context.Context, workload Workload, _ FindSessionRequest) (Session, bool, error) {
	c.workloads = append(c.workloads, workload)
	return Session{}, false, nil
}

func (c *recordingCore) CloseSession(_ context.Context, workload Workload, _ CloseSessionRequest) error {
	c.workloads = append(c.workloads, workload)
	return nil
}

func (c *recordingCore) RegisterAgent(_ context.Context, workload Workload, _ RegisterAgentRequest) (Registration, error) {
	c.workloads = append(c.workloads, workload)
	return Registration{}, nil
}

func (c *recordingCore) AuthorizeCall(_ context.Context, workload Workload, _ AuthorizeCallRequest) (Authorization, error) {
	c.workloads = append(c.workloads, workload)
	return Authorization{}, nil
}

func (*recordingCore) VerifyCredential(context.Context, VerifyCredentialRequest) (VerifiedAgent, error) {
	return VerifiedAgent{}, nil
}

// callAll makes every Service call once and returns their errors in order.
func callAll(svc Service) []error {
	ctx := context.Background()
	_, openErr := svc.OpenSession(ctx, OpenSessionRequest{})
	_, _, findErr := svc.FindSession(ctx, FindSessionRequest{})
	closeErr := svc.CloseSession(ctx, CloseSessionRequest{})
	_, registerErr := svc.RegisterAgent(ctx, RegisterAgentRequest{})
	_, authorizeErr := svc.AuthorizeCall(ctx, AuthorizeCallRequest{})
	return []error{openErr, findErr, closeErr, registerErr, authorizeErr}
}

type authenticatorFunc func(context.Context) (Workload, error)

func (f authenticatorFunc) Authenticate(ctx context.Context) (Workload, error) { return f(ctx) }

func TestInProcessPassesTheAuthenticatedWorkloadToEveryCall(t *testing.T) {
	want := Workload{Name: "system:serviceaccount:mecatl:mecak8s", Verified: true}
	core := &recordingCore{}
	svc := NewInProcess(authenticatorFunc(func(context.Context) (Workload, error) { return want, nil }), core)
	for i, err := range callAll(svc) {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if len(core.workloads) != 5 {
		t.Fatalf("core received %d calls, want 5", len(core.workloads))
	}
	for i, got := range core.workloads {
		if got != want {
			t.Errorf("call %d reached the core with workload %+v, want %+v", i, got, want)
		}
	}
}

func TestInProcessStopsWhenAuthenticationFails(t *testing.T) {
	cases := map[string]struct {
		err  error
		want *Error
	}{
		"broker error passes through":    {NewError(CodePermissionDenied, "", "not allowed"), &Error{Code: CodePermissionDenied}},
		"other error is unauthenticated": {errors.New("token file unreadable: /var/run/secret"), &Error{Code: CodeUnauthenticated}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			core := &recordingCore{}
			svc := NewInProcess(authenticatorFunc(func(context.Context) (Workload, error) { return Workload{}, c.err }), core)
			for i, err := range callAll(svc) {
				if !errors.Is(err, c.want) {
					t.Errorf("call %d error = %v, want %s", i, err, c.want.Code)
				}
				if err != nil && strings.Contains(err.Error(), "/var/run/secret") {
					t.Errorf("call %d error %q exposes the authenticator's failure", i, err)
				}
			}
			if len(core.workloads) != 0 {
				t.Fatalf("core received %d calls after authentication failed", len(core.workloads))
			}
		})
	}
}

func TestUnverifiedAuthenticatorNeverVerifies(t *testing.T) {
	got, err := UnverifiedAuthenticator{Name: "mecak8s-unverified"}.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got != (Workload{Name: "mecak8s-unverified"}) || got.Verified {
		t.Fatalf("Authenticate = %+v, want an unverified mecak8s-unverified workload", got)
	}
}

func TestWorkloadValidateRequiresAName(t *testing.T) {
	for _, w := range []Workload{{}, {Name: "a b"}, {Name: "a\x00b"}, {Verified: true}} {
		if err := w.Validate(); !errors.Is(err, &Error{Code: CodeUnauthenticated}) {
			t.Errorf("Validate(%+v) = %v, want UNAUTHENTICATED", w, err)
		}
	}
	if err := (Workload{Name: "mecak8s-unverified"}).Validate(); err != nil {
		t.Errorf("Validate(named workload) = %v", err)
	}
}
