package identitybroker_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// grantedFixture is a broker on a settable clock whose session holds a
// grant, with its main agent registered.
type grantedFixture struct {
	b     *identitybroker.Broker
	s     identitybroker.Session
	cred  identitybroker.Token
	clock *time.Time
}

// openGranted opens openHost with a token on a broker converting at a fake
// AS-1, and registers the session's main agent.
func openGranted(t *testing.T, opts ...identitybroker.Option) grantedFixture {
	t.Helper()
	as1 := &fakeAS1{answer: func(context.Context, int) (identitybroker.TokenExchangeResult, error) {
		return grantResult("rt-fake-" + rand.Text()), nil
	}}
	clock := new(time.Time)
	*clock = fixedNow()
	opts = append([]identitybroker.Option{
		identitybroker.WithAuthServer(as1),
		identitybroker.WithClock(func() time.Time { return *clock }),
	}, opts...)
	b := newBroker(t, opts...)
	s, err := b.OpenSession(context.Background(), testWorkload, identitybroker.OpenSessionRequest{
		HostSessionID: openHost, Incarnation: openIncarnation, Owner: testOwner, AccessToken: unsignedToken(t, testOwner),
	})
	if err != nil || !s.HoldsRefreshToken {
		t.Fatalf("OpenSession = %+v, %v; want a session holding a grant", s, err)
	}
	reg, err := b.RegisterAgent(context.Background(), testWorkload, mainAgent(s))
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	return grantedFixture{b: b, s: s, cred: reg.Credential, clock: clock}
}

func (f grantedFixture) authorize(req identitybroker.AuthorizeCallRequest) (identitybroker.Authorization, error) {
	return f.b.AuthorizeCall(context.Background(), testWorkload, req)
}

// wantDetail fails t unless err's detail contains want.
func wantDetail(t *testing.T, err error, want string) {
	t.Helper()
	var e *identitybroker.Error
	if !errors.As(err, &e) || !strings.Contains(e.Detail, want) {
		t.Fatalf("error = %v, want detail containing %q", err, want)
	}
}

func TestNewRefusesAnInvalidGateway(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	config := identitybroker.Config{TrustDomain: "agents.example.com", Audience: "https://broker.example.com", CredentialLifetime: 5 * time.Minute}
	collaborators := []identitybroker.Option{
		identitybroker.WithRecorder(identitybroker.NewMemoryRecorder()), identitybroker.WithIDJAGIssuer(&fakeIssuer{}),
	}
	as2 := identitybroker.GatewayAS2{Issuer: "https://as2.example.com", Resource: "https://vmcp.example.com/mcp", AuthServer: &fakeAS2{}}
	for name, g := range map[string]identitybroker.Gateway{
		"no servers":             {},
		"an empty server":        {Servers: []string{""}},
		"a spaced server":        {Servers: []string{"git hub"}},
		"a repeat":               {Servers: []string{"github", "calendar", "github"}},
		"an AS-2 issuer only":    {Servers: []string{"github"}, AS2: identitybroker.GatewayAS2{Issuer: as2.Issuer}},
		"an AS-2 without a port": {Servers: []string{"github"}, AS2: identitybroker.GatewayAS2{Issuer: as2.Issuer, Resource: as2.Resource}},
		"a spaced AS-2 resource": {Servers: []string{"github"}, AS2: identitybroker.GatewayAS2{Issuer: as2.Issuer, Resource: "a b", AuthServer: as2.AuthServer}},
	} {
		_, err := identitybroker.New(config, nil, key, append(collaborators, identitybroker.WithGateway(g))...)
		if !errors.Is(err, identitybroker.ErrInvalidConfig) {
			t.Errorf("New with %s = %v, want ErrInvalidConfig", name, err)
		}
	}
	gateway := identitybroker.WithGateway(identitybroker.Gateway{Servers: []string{"github"}})
	for name, opts := range map[string][]identitybroker.Option{
		"no recorder":         {identitybroker.WithIDJAGIssuer(&fakeIssuer{}), gateway},
		"no ID-JAG issuer":    {identitybroker.WithRecorder(identitybroker.NewMemoryRecorder()), gateway},
		"no collaborators":    {gateway},
		"a nil ID-JAG issuer": {identitybroker.WithRecorder(identitybroker.NewMemoryRecorder()), identitybroker.WithIDJAGIssuer(nil), gateway},
	} {
		_, err := identitybroker.New(config, nil, key, opts...)
		if !errors.Is(err, identitybroker.ErrInvalidConfig) {
			t.Errorf("New with a gateway and %s = %v, want ErrInvalidConfig", name, err)
		}
	}
	_, err = identitybroker.New(config, nil, key,
		identitybroker.WithRecorder(identitybroker.NewMemoryRecorder()), identitybroker.WithIDJAGIssuer(&fakeIssuer{}),
		gateway, identitybroker.WithGateway(identitybroker.Gateway{Servers: []string{"calendar"}}))
	if !errors.Is(err, identitybroker.ErrInvalidConfig) {
		t.Errorf("New with two gateways = %v, want ErrInvalidConfig", err)
	}
}

func TestNewCopiesTheGatewayServers(t *testing.T) {
	servers := []string{"github"}
	f := openGranted(t, withGateway(identitybroker.Gateway{Servers: servers}))
	servers[0] = "calendar"
	_, err := f.authorize(callRequest(f.s.ID, f.cred))
	if err == nil {
		t.Fatal("AuthorizeCall allowed a call with AS-2 unbound")
	}
	var e *identitybroker.Error
	if errors.As(err, &e) && strings.Contains(e.Detail, "no gateway routes") {
		t.Fatalf("changing the caller's server list changed the broker's: %v", err)
	}
}

func TestAuthorizeCallRefusesAnInvalidCredential(t *testing.T) {
	gateway := withGateway(identitybroker.Gateway{Servers: []string{"github"}})

	t.Run("an expired credential", func(t *testing.T) {
		f := openGranted(t, gateway)
		*f.clock = f.clock.Add(6 * time.Minute) // past the 5-minute lifetime and the skew
		got, err := f.authorize(callRequest(f.s.ID, f.cred))
		wantError(t, err, identitybroker.CodeUnauthenticated, identitybroker.ReasonSVIDExpired, f.cred)
		if !got.GatewayToken.IsZero() {
			t.Fatal("a refused call returned a gateway token")
		}
	})

	t.Run("another broker's credential", func(t *testing.T) {
		f := openGranted(t, gateway)
		other := openGranted(t, gateway)
		_, err := f.authorize(callRequest(f.s.ID, other.cred))
		wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, other.cred)
	})
}

func TestAuthorizeCallWithoutAGatewayIsDenied(t *testing.T) {
	f := openGranted(t)
	got, err := f.authorize(callRequest(f.s.ID, f.cred))
	wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, f.cred)
	wantDetail(t, err, "no gateway routes")
	if !got.GatewayToken.IsZero() {
		t.Fatal("a refused call returned a gateway token")
	}
}

func TestAuthorizeCallUnroutedServerIsDenied(t *testing.T) {
	f := openGranted(t, withGateway(identitybroker.Gateway{Servers: []string{"calendar"}}))
	_, err := f.authorize(callRequest(f.s.ID, f.cred))
	wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, f.cred)
	wantDetail(t, err, "no gateway routes")

	// A routed server passes check 6.
	req := callRequest(f.s.ID, f.cred)
	req.Server = "calendar"
	_, err = f.authorize(req)
	var e *identitybroker.Error
	if !errors.As(err, &e) || strings.Contains(e.Detail, "no gateway routes") {
		t.Fatalf("a call to a routed server = %v, want past the server check", err)
	}
}

// failingRecorder refuses every record.
type failingRecorder struct{}

func (failingRecorder) RecordIDJAG(context.Context, identitybroker.IDJAGRecord) error {
	return errors.New("recorder unavailable")
}

func (failingRecorder) RecordDecision(context.Context, identitybroker.DecisionRecord) error {
	return errors.New("recorder unavailable")
}

func TestAuthorizeCallRecordsRefusals(t *testing.T) {
	rec := identitybroker.NewMemoryRecorder()
	f := openGranted(t, withGateway(identitybroker.Gateway{Servers: []string{"calendar"}}), identitybroker.WithRecorder(rec))
	ctx := context.Background()

	// Neither an invalid request nor an unknown session is recorded.
	invalid := callRequest(f.s.ID, f.cred)
	invalid.Method = ""
	_, err := f.authorize(invalid)
	wantError(t, err, identitybroker.CodeInvalidArgument, "", f.cred)
	_, err = f.authorize(callRequest("bs_unknown", f.cred))
	wantError(t, err, identitybroker.CodePermissionDenied, "", f.cred)
	if got := rec.Decisions(); len(got) != 0 {
		t.Fatalf("recorded %d decisions for refusals before the session lookup, want none", len(got))
	}

	tokenless, err := f.b.OpenSession(ctx, testWorkload, identitybroker.OpenSessionRequest{
		HostSessionID: "hostS43", Incarnation: "inc_bbcdefghijklmnopqrstuvwxya", Owner: testOwner,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	report := callRequest(f.s.ID, f.cred)
	report.RejectedAttemptID = "att_unknown"
	type step struct {
		name    string
		session identitybroker.Session
		req     identitybroker.AuthorizeCallRequest
		code    identitybroker.Code
		reason  identitybroker.Reason
		agent   bool // whether check 3 passed
	}
	steps := []step{
		{"an unknown attempt", f.s, report, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, false},
		{"no grant", tokenless, callRequest(tokenless.ID, f.cred), identitybroker.CodeFailedPrecondition, identitybroker.ReasonNoGrant, false},
		{"an unrouted server", f.s, callRequest(f.s.ID, f.cred), identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, true},
		{"a closed session", f.s, callRequest(f.s.ID, f.cred), identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed, false},
	}
	for _, step := range steps {
		if step.reason == identitybroker.ReasonSessionClosed {
			if err := f.b.CloseSession(ctx, testWorkload, identitybroker.CloseSessionRequest{SessionID: step.session.ID, Reason: step.reason}); err != nil {
				t.Fatalf("CloseSession: %v", err)
			}
		}
		_, err := f.authorize(step.req)
		wantError(t, err, step.code, step.reason, f.cred)
	}

	got := rec.Decisions()
	if len(got) != len(steps) {
		t.Fatalf("recorded %d decisions, want %d", len(got), len(steps))
	}
	for i, step := range steps {
		d := got[i]
		session := step.session
		if d.Outcome != identitybroker.OutcomeDenied || d.Code != step.code || d.Reason != step.reason {
			t.Errorf("%s: recorded %s %s %q, want denied %s %q", step.name, d.Outcome, d.Code, d.Reason, step.code, step.reason)
		}
		r := step.req
		if !d.At.Equal(fixedNow()) || d.SessionID != session.ID || d.Workload != testWorkload || d.Owner != session.Owner ||
			d.RunID != r.RunID || d.RunOwner != r.RunOwner || d.DelegationID != r.DelegationID || d.OperationID != r.OperationID ||
			d.RejectedAttemptID != r.RejectedAttemptID || d.Method != r.Method || d.Server != r.Server ||
			d.Tool != r.Tool || d.ArgumentsSHA256 != r.ArgumentsSHA256 || d.AttemptID != "" {
			t.Errorf("%s: recorded %+v, want the request's call fields", step.name, d)
		}
		if gotAgent := !d.Agent.IsZero() && strings.HasPrefix(d.AgentID, "ag_"); gotAgent != step.agent {
			t.Errorf("%s: recorded agent %+v %q, want it set: %v", step.name, d.Agent, d.AgentID, step.agent)
		}
		if strings.Contains(fmt.Sprintf("%+v", d), f.cred.Value()) {
			t.Errorf("%s: the record carries the credential", step.name)
		}
	}
}

func TestAuthorizeCallReturnsRefusalWhenItsRecordFails(t *testing.T) {
	f := openGranted(t, withGateway(identitybroker.Gateway{Servers: []string{"calendar"}}), identitybroker.WithRecorder(failingRecorder{}))
	_, err := f.authorize(callRequest(f.s.ID, f.cred))
	wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, f.cred)
	wantDetail(t, err, "no gateway routes")
}
