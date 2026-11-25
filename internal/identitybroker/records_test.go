package identitybroker_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server/as1servertest"
	"github.com/stacklok/mecatl/internal/identitybroker/conformance"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// userTokenIssuer, as1Subject, and as1Client are the AS-1 claims userToken
// carries besides sub_id.
const (
	userTokenIssuer = "https://broker.example.com/as"
	as1Subject      = "u-alice-91"
	as1Client       = "mecatl-web"
)

// userToken is an access token for owner, as unsignedToken, that also
// carries AS-1's iss, sub, and client_id.
func userToken(t *testing.T, owner identitybroker.Owner) identitybroker.Token {
	t.Helper()
	return jwtShaped(map[string]any{"alg": "none"}, map[string]any{
		"jti": rand.Text(), "exp": fixedNow().Add(time.Hour).Unix(), "iss": userTokenIssuer, "sub": as1Subject, "client_id": as1Client,
		"sub_id": map[string]string{"format": "iss_sub", "iss": owner.Issuer, "sub": owner.Subject},
	})
}

// sink is a MemoryRecorder that refuses the records refuse picks, and
// collects the delivery failures the broker reports.
type sink struct {
	*identitybroker.MemoryRecorder

	mu       sync.Mutex
	refuse   func(identitybroker.Record) bool
	failures []identitybroker.DeliveryFailure
}

func newSink() *sink { return &sink{MemoryRecorder: identitybroker.NewMemoryRecorder()} }

func (s *sink) Record(ctx context.Context, rec identitybroker.Record) error {
	s.mu.Lock()
	refuse := s.refuse
	s.mu.Unlock()
	if refuse != nil && refuse(rec) {
		return errors.New("sink unavailable")
	}
	return s.MemoryRecorder.Record(ctx, rec)
}

// refusing has s refuse the records of type typ, or accept all with "".
func (s *sink) refusing(typ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse = nil
	if typ != "" {
		s.refuse = func(rec identitybroker.Record) bool { return rec.Header().Type == typ }
	}
}

func (s *sink) report(f identitybroker.DeliveryFailure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, f)
}

func (s *sink) reported() []identitybroker.DeliveryFailure {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]identitybroker.DeliveryFailure(nil), s.failures...)
}

func (s *sink) options() []identitybroker.Option {
	return []identitybroker.Option{identitybroker.WithRecorder(s), identitybroker.WithDeliveryFailures(s.report)}
}

// recordFixture is a broker converting at a fake AS-1 on a controlled clock,
// recording into sink.
type recordFixture struct {
	b     *identitybroker.Broker
	sink  *sink
	as1   *fakeAS1
	clock *time.Time
}

func newRecordFixture(t *testing.T, opts ...identitybroker.Option) *recordFixture {
	t.Helper()
	f := &recordFixture{sink: newSink(), clock: new(time.Time)}
	*f.clock = fixedNow()
	f.as1 = &fakeAS1{answer: func(context.Context, int) (identitybroker.TokenExchangeResult, error) {
		return grantResult("rt-fake-" + rand.Text()), nil
	}}
	opts = append(append([]identitybroker.Option{
		identitybroker.WithAuthServer(f.as1),
		identitybroker.WithClock(func() time.Time { return *f.clock }),
	}, f.sink.options()...), opts...)
	f.b = newBroker(t, opts...)
	return f
}

func (f *recordFixture) open(host string, owner identitybroker.Owner, token identitybroker.Token) (identitybroker.Session, error) {
	return f.b.OpenSession(context.Background(), testWorkload, identitybroker.OpenSessionRequest{
		HostSessionID: host, Incarnation: openIncarnation, Owner: owner, AccessToken: token,
	})
}

// lastOpen returns the latest open record, failing t when there is none.
func (f *recordFixture) lastOpen(t *testing.T) identitybroker.SessionOpenRecord {
	t.Helper()
	opens := f.sink.Opens()
	if len(opens) == 0 {
		t.Fatal("no open record")
	}
	return opens[len(opens)-1]
}

// wantSubject fails t unless rec's subject key has value and provenance.
func wantSubject(t *testing.T, rec identitybroker.Record, key, value string, p identitybroker.SubjectProvenance) {
	t.Helper()
	got, ok := rec.Subjects()[key]
	if !ok || got.Value != value || got.Provenance != p {
		t.Errorf("%s subject %s = %+v (present %v), want %q %s", rec.Header().Type, key, got, ok, value, p)
	}
}

// wantUnavailable fails t unless each key is present in rec's subjects and
// marked unavailable.
func wantUnavailable(t *testing.T, rec identitybroker.Record, keys ...string) {
	t.Helper()
	for _, key := range keys {
		wantSubject(t, rec, key, "", identitybroker.ProvenanceUnavailable)
	}
}

func TestSessionOpenRecordsEachKindOfOpen(t *testing.T) {
	f := newRecordFixture(t)

	// A tokenless open pairs a new session with the reported owner.
	s, err := f.open(openHost, testOwner, identitybroker.Token{})
	if err != nil {
		t.Fatalf("tokenless open: %v", err)
	}
	tokenless := f.lastOpen(t)
	if tokenless.Outcome != identitybroker.OutcomeSuccess || tokenless.Kind != identitybroker.OpenTokenless ||
		!tokenless.NewSession || tokenless.SessionID != s.ID || tokenless.SessionRecord != "" || tokenless.User != nil {
		t.Fatalf("tokenless open record = %+v", tokenless)
	}

	// Converting a token names the user AS-1 accepted and the source jti.
	first := userToken(t, testOwner)
	if _, err := f.open(openHost, testOwner, first); err != nil {
		t.Fatalf("converting open: %v", err)
	}
	converted := f.lastOpen(t)
	wantUser := identitybroker.User{Owner: testOwner, Issuer: userTokenIssuer, Subject: as1Subject, ClientID: as1Client}
	if converted.Kind != identitybroker.OpenConverted || converted.NewSession || converted.SessionRecord != tokenless.AuditID ||
		converted.User == nil || *converted.User != wantUser || converted.UserRecord != "" ||
		converted.SourceJTI != jti(t, first) || !converted.HardExpiry.Equal(matchingGrant().ExpiresAt) || converted.Replaced != nil {
		t.Fatalf("converting open record = %+v", converted)
	}
	for key, value := range map[string]string{
		identitybroker.SubjectOwnerIssuer: testOwner.Issuer, identitybroker.SubjectOwnerID: testOwner.Subject,
		identitybroker.SubjectUserIssuer: userTokenIssuer, identitybroker.SubjectUserID: as1Subject,
		identitybroker.SubjectClientID: as1Client, identitybroker.SubjectClientIssuer: userTokenIssuer,
	} {
		wantSubject(t, &converted, key, value, identitybroker.ProvenanceVerified)
	}

	// The same token again: AS-1 may already have converted it.
	if _, err := f.open(openHost, testOwner, first); err != nil {
		t.Fatalf("repeated open: %v", err)
	}
	again := f.lastOpen(t)
	if again.Kind != identitybroker.OpenAlreadyConverted || again.UserRecord != converted.AuditID || again.SourceJTI != jti(t, first) {
		t.Fatalf("repeated open record = %+v", again)
	}
	wantSubject(t, &again, identitybroker.SubjectUserID, as1Subject, identitybroker.ProvenanceFromRecord(converted.AuditID))

	// A fresh token replaces the refresh token the first open stored, which
	// the record names by that open and its source jti.
	if _, err := f.open(openHost, testOwner, userToken(t, testOwner)); err != nil {
		t.Fatalf("replacing open: %v", err)
	}
	replacing := f.lastOpen(t)
	if replacing.Replaced == nil || *replacing.Replaced != (identitybroker.ReplacedGrant{OpenRecord: converted.AuditID, SourceJTI: jti(t, first)}) {
		t.Fatalf("replacing open record = %+v, want it to name the converting open", replacing)
	}

	// A tokenless open now takes the user from the replacing open's record.
	if _, err := f.open(openHost, testOwner, identitybroker.Token{}); err != nil {
		t.Fatalf("later tokenless open: %v", err)
	}
	later := f.lastOpen(t)
	if later.Kind != identitybroker.OpenTokenless || later.UserRecord != replacing.AuditID {
		t.Fatalf("later tokenless open record = %+v", later)
	}

	ids := map[string]bool{}
	for _, rec := range f.sink.Opens() {
		if ids[rec.AuditID] || !strings.HasPrefix(rec.AuditID, "rec_") {
			t.Fatalf("open records share or lack an AuditID: %+v", f.sink.Opens())
		}
		ids[rec.AuditID] = true
	}
}

func TestTokenlessOpenProvenance(t *testing.T) {
	f := newRecordFixture(t)
	s, err := f.open(openHost, testOwner, identitybroker.Token{})
	if err != nil {
		t.Fatal(err)
	}
	open := f.lastOpen(t)
	// The owner is only what mecak8s reported; no user was verified, and the
	// phase 1 workload is unverified.
	wantSubject(t, &open, identitybroker.SubjectOwnerID, testOwner.Subject, identitybroker.ProvenanceReportedByMecak8s)
	wantSubject(t, &open, identitybroker.SubjectOwnerIssuer, testOwner.Issuer, identitybroker.ProvenanceReportedByMecak8s)
	wantUnavailable(t, &open, identitybroker.SubjectUserIssuer, identitybroker.SubjectUserID,
		identitybroker.SubjectClientID, identitybroker.SubjectClientIssuer, identitybroker.SubjectWorkloadIssuer)
	wantSubject(t, &open, identitybroker.SubjectWorkloadSubject, testWorkload.Name, identitybroker.ProvenanceReportedByMecak8s)

	// So do the records of the session's registration and its refused call:
	// run owner, run, and delegation are never verified.
	reg, err := f.b.RegisterAgent(context.Background(), testWorkload, mainAgent(s))
	if err != nil {
		t.Fatal(err)
	}
	r := f.sink.Registrations()[0]
	wantSubject(t, &r, identitybroker.SubjectOwnerID, testOwner.Subject, identitybroker.ProvenanceReportedByMecak8s)
	wantUnavailable(t, &r, identitybroker.SubjectUserID)
	wantSubject(t, &r, identitybroker.SubjectAgentInstanceRef, openIncarnation, identitybroker.ProvenanceReportedByMecak8s)

	_, err = f.b.AuthorizeCall(context.Background(), testWorkload, callRequest(s.ID, reg.Credential))
	wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonNoGrant, reg.Credential)
	d := f.sink.Decisions()
	if len(d) != 1 {
		t.Fatalf("decisions = %+v, want the refusal", d)
	}
	wantSubject(t, &d[0], identitybroker.SubjectOwnerID, testOwner.Subject, identitybroker.ProvenanceReportedByMecak8s)
	wantUnavailable(t, &d[0], identitybroker.SubjectUserID, identitybroker.SubjectAgentSPIFFEID)
	wantSubject(t, &d[0], identitybroker.SubjectRunOwnerID, testOwner.Subject, identitybroker.ProvenanceReportedByMecak8s)
	for _, rec := range []identitybroker.Record{&open, &r, &d[0]} {
		for key, subject := range rec.Subjects() {
			if subject.Provenance == identitybroker.ProvenanceVerified && key != identitybroker.SubjectAgentSPIFFEID && key != identitybroker.SubjectAgentActorID {
				t.Errorf("%s marks %s verified after a tokenless open", rec.Header().Type, key)
			}
		}
	}
}

func TestSessionOpenRecordsEveryRefusal(t *testing.T) {
	bob := identitybroker.Owner{Issuer: testOwner.Issuer, Subject: "bob-117"}
	as1Refusal := func(status int, code, reason string) func(context.Context, int) (identitybroker.TokenExchangeResult, error) {
		return func(context.Context, int) (identitybroker.TokenExchangeResult, error) {
			return identitybroker.TokenExchangeResult{}, refusal(status, code, reason)
		}
	}
	tests := []struct {
		name string
		// as1 answers the exchange; nil keeps the fixture's grant.
		as1 func(context.Context, int) (identitybroker.TokenExchangeResult, error)
		// setup prepares f and returns the open to refuse.
		open       func(t *testing.T, f *recordFixture) (identitybroker.Session, error)
		code       identitybroker.Code
		reason     identitybroker.Reason
		wantCloses int
	}{
		{name: "subject_invalid from the broker", open: func(_ *testing.T, f *recordFixture) (identitybroker.Session, error) {
			return f.open(openHost, testOwner, identitybroker.NewToken("not-a-jwt"))
		}, code: identitybroker.CodeUnauthenticated, reason: identitybroker.ReasonSubjectInvalid},
		{name: "owner_mismatch", open: func(t *testing.T, f *recordFixture) (identitybroker.Session, error) {
			return f.open(openHost, testOwner, userToken(t, bob))
		}, code: identitybroker.CodePermissionDenied, reason: identitybroker.ReasonOwnerMismatch},
		{name: "session_owned by the reported owner", open: func(t *testing.T, f *recordFixture) (identitybroker.Session, error) {
			if _, err := f.open(openHost, testOwner, identitybroker.Token{}); err != nil {
				t.Fatal(err)
			}
			return f.open(openHost, bob, identitybroker.Token{})
		}, code: identitybroker.CodePermissionDenied, reason: identitybroker.ReasonSessionOwned},
		{name: "session_owned by the verified owner", open: func(t *testing.T, f *recordFixture) (identitybroker.Session, error) {
			s, err := f.open(openHost, testOwner, identitybroker.Token{})
			if err != nil {
				t.Fatal(err)
			}
			identitybroker.SetVerifiedOwner(f.b, s.ID, bob)
			return f.open(openHost, testOwner, userToken(t, testOwner))
		}, code: identitybroker.CodePermissionDenied, reason: identitybroker.ReasonSessionOwned},
		{name: "another incarnation", open: func(t *testing.T, f *recordFixture) (identitybroker.Session, error) {
			if _, err := f.open(openHost, testOwner, identitybroker.Token{}); err != nil {
				t.Fatal(err)
			}
			return f.b.OpenSession(context.Background(), testWorkload, identitybroker.OpenSessionRequest{
				HostSessionID: openHost, Incarnation: "inc_zzcdefghijklmnopqrstuvwxya", Owner: testOwner,
			})
		}, code: identitybroker.CodeAlreadyExists},
		{name: "another workload", open: func(t *testing.T, f *recordFixture) (identitybroker.Session, error) {
			if _, err := f.open(openHost, testOwner, identitybroker.Token{}); err != nil {
				t.Fatal(err)
			}
			return f.b.OpenSession(context.Background(), identitybroker.Workload{Name: "other"}, identitybroker.OpenSessionRequest{
				HostSessionID: openHost, Incarnation: openIncarnation, Owner: testOwner,
			})
		}, code: identitybroker.CodePermissionDenied},
		{name: "token_used from the broker", open: func(t *testing.T, f *recordFixture) (identitybroker.Session, error) {
			token := userToken(t, testOwner)
			if _, err := f.open("hostS43", testOwner, token); err != nil {
				t.Fatal(err)
			}
			// openHost's new session holds no refresh token, so the spent
			// token cannot just return it.
			return f.open(openHost, testOwner, token)
		}, code: identitybroker.CodeUnauthenticated, reason: identitybroker.ReasonTokenUsed},
		{name: "token_used from AS-1", as1: as1Refusal(400, "invalid_request", "token_used"),
			code: identitybroker.CodeUnauthenticated, reason: identitybroker.ReasonTokenUsed},
		{name: "subject_token_invalid from AS-1", as1: as1Refusal(400, "invalid_request", "subject_token_invalid"),
			code: identitybroker.CodeUnauthenticated, reason: identitybroker.ReasonSubjectInvalid},
		{name: "signed_out", as1: as1Refusal(400, "invalid_grant", "signed_out"),
			code: identitybroker.CodeFailedPrecondition, reason: identitybroker.ReasonSignedOut},
		{name: "upstream_revoked", as1: as1Refusal(400, "invalid_grant", "upstream_revoked"),
			code: identitybroker.CodePermissionDenied, reason: identitybroker.ReasonUpstreamRevoked},
		{name: "owner_action_required", as1: as1Refusal(400, "invalid_grant", "owner_action_required"),
			code: identitybroker.CodeFailedPrecondition, reason: identitybroker.ReasonOwnerActionRequired},
		{name: "a configuration fault", as1: as1Refusal(400, "invalid_request", "subject_token_audience"),
			code: identitybroker.CodeInternal},
		{name: "AS-1 unavailable", as1: as1Refusal(503, "temporarily_unavailable", ""),
			code: identitybroker.CodeUnavailable},
		{name: "a mismatched answer", as1: func(context.Context, int) (identitybroker.TokenExchangeResult, error) {
			res := grantResult("rt-mismatch")
			res.Grants[0].IssuableTokenTypes = []string{identitybroker.TokenTypeRefreshToken}
			return res, nil
		}, code: identitybroker.CodeInternal, wantCloses: 1},
		{name: "a cancelled exchange", as1: func(context.Context, int) (identitybroker.TokenExchangeResult, error) {
			return identitybroker.TokenExchangeResult{}, context.Canceled
		}, code: identitybroker.CodeUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRecordFixture(t)
			if tt.as1 != nil {
				f.as1.answer = tt.as1
			}
			open := tt.open
			if open == nil {
				open = func(t *testing.T, f *recordFixture) (identitybroker.Session, error) {
					return f.open(openHost, testOwner, userToken(t, testOwner))
				}
			}
			_, err := open(t, f)
			wantError(t, err, tt.code, tt.reason)
			rec := f.lastOpen(t)
			wantOutcome := identitybroker.OutcomeDenied
			if tt.code == identitybroker.CodeInternal || tt.code == identitybroker.CodeUnavailable {
				wantOutcome = identitybroker.OutcomeError
			}
			if rec.Code != tt.code || rec.Reason != tt.reason || rec.Outcome != wantOutcome || rec.Kind != "" || rec.User != nil {
				t.Fatalf("refused open record = %+v, want %s %s %s", rec, wantOutcome, tt.code, tt.reason)
			}
			if rec.SessionID == "" {
				t.Errorf("refused open record names no broker session: %+v", rec)
			}
			wantUnavailable(t, &rec, identitybroker.SubjectUserID)
			if n := len(f.sink.Closes()); n != tt.wantCloses {
				t.Errorf("recorded %d closes, want %d", n, tt.wantCloses)
			}
		})
	}
}

func TestSessionCloseRecordsEveryPathOnce(t *testing.T) {
	ctx := context.Background()
	wantClose := func(t *testing.T, f *recordFixture, s identitybroker.Session, cause identitybroker.CloseCause) identitybroker.SessionCloseRecord {
		t.Helper()
		closes := f.sink.Closes()
		if len(closes) != 1 {
			t.Fatalf("close records = %+v, want one", closes)
		}
		c := closes[0]
		opens := f.sink.Opens()
		if c.Cause != cause || c.SessionID != s.ID || c.ClosingReason != identitybroker.ReasonSessionClosed ||
			c.Outcome != identitybroker.OutcomeSuccess || c.SessionRecord != opens[0].AuditID || !strings.HasPrefix(c.AuditID, "rec_") {
			t.Fatalf("close record = %+v, want %s of %s created by %s", c, cause, s.ID, opens[0].AuditID)
		}
		return c
	}

	t.Run("requested", func(t *testing.T) {
		f := newRecordFixture(t)
		s, err := f.open(openHost, testOwner, userToken(t, testOwner))
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := f.b.CloseSession(ctx, testWorkload, identitybroker.CloseSessionRequest{SessionID: s.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
				t.Fatal(err)
			}
		}
		c := wantClose(t, f, s, identitybroker.CloseRequested)
		if c.UserRecord != f.sink.Opens()[0].AuditID || c.User.Subject != as1Subject {
			t.Errorf("close record = %+v, want the user from the open", c)
		}
	})

	t.Run("hard expiry found by a lookup", func(t *testing.T) {
		f := newRecordFixture(t)
		s, err := f.open(openHost, testOwner, userToken(t, testOwner))
		if err != nil {
			t.Fatal(err)
		}
		*f.clock = matchingGrant().ExpiresAt
		if _, found := findSession(t, f.b, openHost); found {
			t.Fatal("a session past its hard expiry was found")
		}
		findSession(t, f.b, openHost)
		wantClose(t, f, s, identitybroker.CloseHardExpiry)
	})

	t.Run("mismatched answer", func(t *testing.T) {
		f := newRecordFixture(t)
		f.as1.answer = func(context.Context, int) (identitybroker.TokenExchangeResult, error) {
			return identitybroker.TokenExchangeResult{}, fmt.Errorf("%w %w", identitybroker.ErrMalformedResponse, identitybroker.ErrTransportConfiguration)
		}
		if _, err := f.open(openHost, testOwner, userToken(t, testOwner)); err == nil {
			t.Fatal("an undecodable answer opened the session")
		}
		wantClose(t, f, identitybroker.Session{ID: f.sink.Opens()[0].SessionID}, identitybroker.CloseMismatchedAnswer)
	})

	t.Run("grant lost", func(t *testing.T) {
		f := newPerCall(t)
		rec := newSink()
		identitybroker.SetRecorder(f.b, rec)
		f.issuer.answer = func(int, identitybroker.IDJAGRequest) (identitybroker.IDJAGResult, error) {
			return identitybroker.IDJAGResult{}, refusal(400, "invalid_grant", "revoked")
		}
		_, err := f.authorize(callRequest(f.s.ID, f.cred))
		wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed, f.cred)
		closes := rec.Closes()
		if len(closes) != 1 || closes[0].Cause != identitybroker.CloseGrantLost || closes[0].SessionID != f.s.ID {
			t.Fatalf("close records = %+v, want the grant's loss", closes)
		}
	})
}

// registerMain registers s's main agent in f.
func (f *recordFixture) registerMain(t *testing.T, s identitybroker.Session) identitybroker.Registration {
	t.Helper()
	reg, err := f.b.RegisterAgent(context.Background(), testWorkload, mainAgent(s))
	if err != nil {
		t.Fatalf("RegisterAgent main: %v", err)
	}
	return reg
}

// helperRequest registers a helper of parent in s, incarnation inc.
func helperRequest(s identitybroker.Session, parent identitybroker.AgentRef, inc string, standIn bool) identitybroker.RegisterAgentRequest {
	return identitybroker.RegisterAgentRequest{
		SessionID: s.ID, HostSessionID: "hostS-" + inc, Owner: testOwner, Incarnation: inc,
		Definition: conformance.MainDefinition, Parent: parent, ParentHostSessionID: s.HostSessionID, ParentStandIn: standIn,
	}
}

const (
	incHelper = "inc_bbcdefghijklmnopqrstuvwxya"
	incMember = "inc_ccddefghijklmnopqrstuvwxya"
)

func TestRegistrationRecordsAreLineageEvents(t *testing.T) {
	ctx := context.Background()
	f := newRecordFixture(t)
	s, err := f.open(openHost, testOwner, userToken(t, testOwner))
	if err != nil {
		t.Fatal(err)
	}
	openRec := f.lastOpen(t)
	main := f.registerMain(t, s)
	helper, err := f.b.RegisterAgent(ctx, testWorkload, helperRequest(s, main.Agent, incHelper, false))
	if err != nil {
		t.Fatalf("RegisterAgent helper: %v", err)
	}
	member, err := f.b.RegisterAgent(ctx, testWorkload, helperRequest(s, main.Agent, incMember, true))
	if err != nil {
		t.Fatalf("RegisterAgent team member: %v", err)
	}
	regs := f.sink.Registrations()
	if len(regs) != 3 {
		t.Fatalf("registration records = %+v, want three", regs)
	}
	m, h, tm := regs[0], regs[1], regs[2]
	if m.Type != identitybroker.TypeInstanceRegistered || m.Outcome != identitybroker.OutcomeSuccess || !m.New || m.Moved ||
		m.Agent != main.Agent || !strings.HasPrefix(m.AgentID, "ag_") || m.SessionRecord != openRec.AuditID ||
		!m.Parent.IsZero() || m.ParentRecord != "" || !m.CredentialExpiresAt.Equal(main.ExpiresAt) || m.UserRecord != openRec.AuditID {
		t.Fatalf("main's registration record = %+v", m)
	}
	if h.Agent != helper.Agent || h.Parent != main.Agent || h.ParentRecord != m.AuditID || h.ParentStandIn ||
		h.ParentHostSessionID != openHost || h.Incarnation != incHelper {
		t.Fatalf("helper's registration record = %+v, want main's as its parent's", h)
	}
	if tm.Agent != member.Agent || tm.ParentRecord != m.AuditID || !tm.ParentStandIn {
		t.Fatalf("team member's registration record = %+v, want main marked as its stand-in parent", tm)
	}
	wantSubject(t, &h, identitybroker.SubjectAgentSPIFFEID, helper.Agent.Subject, identitybroker.ProvenanceVerified)
	wantSubject(t, &h, identitybroker.SubjectAgentInstanceRef, incHelper, identitybroker.ProvenanceReportedByMecak8s)
	wantSubject(t, &h, identitybroker.SubjectUserID, as1Subject, identitybroker.ProvenanceFromRecord(openRec.AuditID))

	// Registering again re-admits the helper; after a reopen it moves.
	if _, err := f.b.RegisterAgent(ctx, testWorkload, helperRequest(s, main.Agent, incHelper, false)); err != nil {
		t.Fatal(err)
	}
	again := f.sink.Registrations()[3]
	if again.New || again.Moved || again.PreviousRecord != h.AuditID || again.Agent != helper.Agent {
		t.Fatalf("re-registration record = %+v, want it to name the first", again)
	}
	if err := f.b.CloseSession(ctx, testWorkload, identitybroker.CloseSessionRequest{SessionID: s.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	s2, err := f.open(openHost, testOwner, identitybroker.Token{})
	if err != nil {
		t.Fatal(err)
	}
	main2 := f.registerMain(t, s2)
	req := helperRequest(s2, main2.Agent, incHelper, false)
	if _, err := f.b.RegisterAgent(ctx, testWorkload, req); err != nil {
		t.Fatal(err)
	}
	regs = f.sink.Registrations()
	moved := regs[len(regs)-1]
	if !moved.Moved || moved.New || moved.PreviousRecord != again.AuditID || moved.ParentRecord != regs[len(regs)-2].AuditID {
		t.Fatalf("moved registration record = %+v", moved)
	}

	// A refused registration is recorded with its reason, naming the
	// instance its incarnation registered as.
	req.Owner = identitybroker.Owner{Issuer: testOwner.Issuer, Subject: "bob-117"}
	_, err = f.b.RegisterAgent(ctx, testWorkload, req)
	wantError(t, err, identitybroker.CodePermissionDenied, "")
	regs = f.sink.Registrations()
	refused := regs[len(regs)-1]
	if refused.Outcome != identitybroker.OutcomeDenied || refused.Code != identitybroker.CodePermissionDenied ||
		refused.Agent != helper.Agent || refused.PreviousRecord != moved.AuditID || !refused.CredentialExpiresAt.IsZero() {
		t.Fatalf("refused registration record = %+v", refused)
	}
	wantSubject(t, &refused, identitybroker.SubjectAgentSPIFFEID, helper.Agent.Subject, identitybroker.ProvenanceFromRecord(moved.AuditID))
}

// delegationFixture is a recordFixture with a granted session whose main
// agent has a registered helper.
type delegationFixture struct {
	*recordFixture
	s            identitybroker.Session
	main, helper identitybroker.Registration
}

func newDelegationFixture(t *testing.T) delegationFixture {
	t.Helper()
	f := newRecordFixture(t)
	s, err := f.open(openHost, testOwner, userToken(t, testOwner))
	if err != nil {
		t.Fatal(err)
	}
	main := f.registerMain(t, s)
	helper, err := f.b.RegisterAgent(context.Background(), testWorkload, helperRequest(s, main.Agent, incHelper, false))
	if err != nil {
		t.Fatal(err)
	}
	return delegationFixture{recordFixture: f, s: s, main: main, helper: helper}
}

func (f delegationFixture) report(id, from string, delegating identitybroker.AgentRef, to string, receiving identitybroker.AgentRef, standIn bool) error {
	return f.b.ReportDelegation(context.Background(), testWorkload, identitybroker.ReportDelegationRequest{
		SessionID: f.s.ID, DelegationID: id, DelegatingRun: from, DelegatingAgent: delegating,
		ReceivingRun: to, ReceivingAgent: receiving, Owner: testOwner, DelegatingStandIn: standIn,
	})
}

func TestDelegationReportsAreLineageEvents(t *testing.T) {
	f := newDelegationFixture(t)
	regs := f.sink.Registrations()
	mainRec, helperRec := regs[0], regs[1]

	// The first report from an unseen run records it as a root run.
	if err := f.report("D41", "R41", f.main.Agent, "R42", f.helper.Agent, false); err != nil {
		t.Fatal(err)
	}
	d := f.sink.Delegations()
	if len(d) != 1 {
		t.Fatalf("delegation records = %+v, want one", d)
	}
	first := d[0]
	if first.Type != identitybroker.TypeDelegation || first.Outcome != identitybroker.OutcomeSuccess || first.DelegationID != "D41" ||
		first.DelegatingRun != "R41" || first.DelegatingAgent != f.main.Agent || first.DelegatingRecord != mainRec.AuditID ||
		first.ReceivingRun != "R42" || first.ReceivingAgent != f.helper.Agent || first.ReceivingRecord != helperRec.AuditID ||
		first.RunOwner != testOwner || first.Owner != testOwner || !first.DelegatingRunRecorded || first.ParentDelegationRecord != "" || first.DelegatingStandIn {
		t.Fatalf("delegation record = %+v", first)
	}
	wantSubject(t, &first, identitybroker.SubjectRunOwnerID, testOwner.Subject, identitybroker.ProvenanceReportedByMecak8s)
	wantSubject(t, &first, identitybroker.SubjectAgentSPIFFEID, f.main.Agent.Subject, identitybroker.ProvenanceReportedByMecak8s)
	wantSubject(t, &first, identitybroker.SubjectAgentInstanceRef, openIncarnation, identitybroker.ProvenanceReportedByMecak8s)

	// An identical repeat succeeds with no second record.
	if err := f.report("D41", "R41", f.main.Agent, "R42", f.helper.Agent, false); err != nil {
		t.Fatal(err)
	}
	if n := len(f.sink.Delegations()); n != 1 {
		t.Fatalf("an identical repeat made %d records, want 1", n)
	}

	// A delegation from the delegated run references the one it descends
	// from; the delegating run is known, so this report records none.
	member, err := f.b.RegisterAgent(context.Background(), testWorkload, helperRequest(f.s, f.main.Agent, incMember, true))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.report("D43", "R42", f.helper.Agent, "R43", member.Agent, false); err != nil {
		t.Fatal(err)
	}
	nested := f.sink.Delegations()[1]
	if nested.ParentDelegationRecord != first.AuditID || nested.DelegatingRunRecorded {
		t.Fatalf("nested delegation record = %+v, want it to reference %s", nested, first.AuditID)
	}

	// A team run over the API: main stands in, and the run is new.
	if err := f.report("D44", "RT1", f.main.Agent, "R44", member.Agent, true); err != nil {
		t.Fatal(err)
	}
	team := f.sink.Delegations()[2]
	if !team.DelegatingStandIn || !team.DelegatingRunRecorded || team.DelegatingRun != "RT1" {
		t.Fatalf("team delegation record = %+v, want main as a stand-in and its run recorded", team)
	}

	// Refusals, including a changed repeat, are recorded with their reasons.
	refusals := []struct {
		name   string
		report func() error
		code   identitybroker.Code
		reason identitybroker.Reason
	}{
		{"changed repeat", func() error { return f.report("D41", "R41", f.main.Agent, "R42", f.helper.Agent, true) }, identitybroker.CodeAlreadyExists, ""},
		{"receiving run already recorded", func() error { return f.report("D45", "R41", f.main.Agent, "R42", f.helper.Agent, false) },
			identitybroker.CodePermissionDenied, identitybroker.ReasonDenied},
		{"delegating run bound to another agent", func() error { return f.report("D46", "R41", f.helper.Agent, "R46", member.Agent, false) },
			identitybroker.CodePermissionDenied, identitybroker.ReasonDenied},
		{"unseen run of a non-root agent", func() error { return f.report("D47", "R-unseen", f.helper.Agent, "R47", member.Agent, false) },
			identitybroker.CodePermissionDenied, identitybroker.ReasonDenied},
		{"receiving agent not registered", func() error {
			return f.report("D48", "R41", f.main.Agent, "R48", identitybroker.AgentRef{Subject: "spiffe://agents.example.com/agent/ag_unknown"}, false)
		}, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied},
	}
	for _, r := range refusals {
		before := len(f.sink.Delegations())
		wantError(t, r.report(), r.code, r.reason)
		d := f.sink.Delegations()
		if len(d) != before+1 {
			t.Fatalf("%s: made %d records, want 1", r.name, len(d)-before)
		}
		got := d[len(d)-1]
		if got.Outcome != identitybroker.OutcomeDenied || got.Code != r.code || got.Reason != r.reason || got.DelegatingRunRecorded {
			t.Errorf("%s: record = %+v", r.name, got)
		}
	}
}

func TestCallRecordsReferenceTheTrail(t *testing.T) {
	f := newRunFixture(t)
	f.delegate(t, "D42", "R43")
	_, err := f.authorize(f.childCall("R43", "D42"))
	if err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	// newRunFixture records into f.rec, a MemoryRecorder.
	opens, regs, dels := f.rec.Opens(), f.rec.Registrations(), f.rec.Delegations()
	// The fixture registers main twice, then the child.
	if len(opens) != 1 || len(regs) != 3 || len(dels) != 1 {
		t.Fatalf("records: %d opens, %d registrations, %d delegations; want 1, 3, 1", len(opens), len(regs), len(dels))
	}
	child := regs[2]
	jags, decisions := f.rec.IDJAGs(), f.rec.Decisions()
	if len(jags) != 1 || len(decisions) != 1 {
		t.Fatalf("records: %d ID-JAGs, %d decisions; want 1 of each", len(jags), len(decisions))
	}
	jag, d := jags[0], decisions[0]
	if jag.Type != identitybroker.TypeIDJAG || jag.IDJAG.ID == "" || jag.IDJAG.Issuer == "" || jag.UserRecord != opens[0].AuditID ||
		jag.RegistrationRecord != child.AuditID || jag.DelegationRecord != dels[0].AuditID || jag.AgentInstance != "inc_bbcdefghijklmnopqrstuvwxya" {
		t.Fatalf("ID-JAG record = %+v, want the grant's open, the child's registration, and D42's record", jag)
	}
	if d.Outcome != identitybroker.OutcomeSuccess || d.IDJAG != jag.IDJAG || d.AuditID == jag.AuditID ||
		d.UserRecord != opens[0].AuditID || d.RegistrationRecord != child.AuditID || d.DelegationRecord != dels[0].AuditID {
		t.Fatalf("decision record = %+v, want the same references as %+v", d, jag)
	}
	for _, rec := range []identitybroker.Record{&jag, &d} {
		wantSubject(t, rec, identitybroker.SubjectOwnerID, testOwner.Subject, identitybroker.ProvenanceFromRecord(opens[0].AuditID))
		wantSubject(t, rec, identitybroker.SubjectAgentSPIFFEID, f.child.Subject, identitybroker.ProvenanceVerified)
		wantSubject(t, rec, identitybroker.SubjectAgentInstanceRef, "inc_bbcdefghijklmnopqrstuvwxya", identitybroker.ProvenanceCredentialVerified)
		wantSubject(t, rec, identitybroker.SubjectRunOwnerID, testOwner.Subject, identitybroker.ProvenanceReportedByMecak8s)
	}
}

func TestARefusedRecordIsHandledPerEvent(t *testing.T) {
	ctx := context.Background()
	closeReq := func(s identitybroker.Session) identitybroker.CloseSessionRequest {
		return identitybroker.CloseSessionRequest{SessionID: s.ID, Reason: identitybroker.ReasonSessionClosed}
	}

	t.Run("an unrecorded open of a new session leaves it closed", func(t *testing.T) {
		f := newRecordFixture(t)
		f.sink.refusing(identitybroker.TypeSessionOpen)
		_, err := f.open(openHost, testOwner, identitybroker.Token{})
		wantError(t, err, identitybroker.CodeUnavailable, "")
		if _, found := findSession(t, f.b, openHost); found {
			t.Fatal("an unrecorded open left its session open")
		}
		closes := f.sink.Closes()
		if len(closes) != 1 || closes[0].Cause != identitybroker.CloseOpenNotRecorded {
			t.Fatalf("close records = %+v, want the unrecorded open's", closes)
		}
		if failures := f.sink.reported(); len(failures) != 1 || failures[0].Type != identitybroker.TypeSessionOpen || failures[0].AuditID == "" {
			t.Fatalf("delivery failures = %+v, want the open's", failures)
		}
	})

	t.Run("an unrecorded conversion keeps the held refresh token", func(t *testing.T) {
		f := newRecordFixture(t)
		s, err := f.open(openHost, testOwner, userToken(t, testOwner))
		if err != nil {
			t.Fatal(err)
		}
		held, _ := identitybroker.SessionGrant(f.b, s.ID)
		f.sink.refusing(identitybroker.TypeSessionOpen)
		_, err = f.open(openHost, testOwner, userToken(t, testOwner))
		wantError(t, err, identitybroker.CodeUnavailable, "", held.RefreshToken)
		now, ok := identitybroker.SessionGrant(f.b, s.ID)
		if !ok || now.RefreshToken.Value() != held.RefreshToken.Value() || now.SourceJTI != held.SourceJTI {
			t.Fatal("an unrecorded conversion changed the held refresh token")
		}
		if got, found := findSession(t, f.b, openHost); !found || got.ID != s.ID {
			t.Fatal("an unrecorded open of an existing session closed it")
		}
	})

	t.Run("an unrecorded conversion of a new session stores nothing", func(t *testing.T) {
		f := newRecordFixture(t)
		f.sink.refusing(identitybroker.TypeSessionOpen)
		_, err := f.open(openHost, testOwner, userToken(t, testOwner))
		wantError(t, err, identitybroker.CodeUnavailable, "")
		id := f.sink.Closes()[0].SessionID
		if _, ok := identitybroker.SessionGrant(f.b, id); ok {
			t.Fatal("an unrecorded conversion stored its refresh token")
		}
		wantNoVerifiedOwner(t, f.b, id)
	})

	t.Run("a refused open is returned when its record is refused", func(t *testing.T) {
		f := newRecordFixture(t)
		f.sink.refusing(identitybroker.TypeSessionOpen)
		_, err := f.open(openHost, testOwner, userToken(t, identitybroker.Owner{Issuer: testOwner.Issuer, Subject: "bob-117"}))
		wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonOwnerMismatch)
		// The refused open created the session; without its record, the
		// session does not stay open.
		if _, found := findSession(t, f.b, openHost); found {
			t.Fatal("a refused open whose record was refused left its new session open")
		}
		closes := f.sink.Closes()
		if len(closes) != 1 || closes[0].Cause != identitybroker.CloseOpenNotRecorded || closes[0].SessionRecord != "" {
			t.Fatalf("close records = %+v, want the unrecorded open's, naming no open record", closes)
		}
		if failures := f.sink.reported(); len(failures) != 1 || failures[0].Type != identitybroker.TypeSessionOpen {
			t.Fatalf("delivery failures = %+v, want the refused open's", failures)
		}
	})

	t.Run("an open cancelled while waiting for its host session is recorded", func(t *testing.T) {
		f := newRecordFixture(t)
		entered, release := make(chan struct{}), make(chan struct{})
		f.as1.answer = func(context.Context, int) (identitybroker.TokenExchangeResult, error) {
			close(entered)
			<-release
			return grantResult("rt-blocked"), nil
		}
		done := make(chan error, 1)
		go func() {
			_, err := f.open(openHost, testOwner, userToken(t, testOwner))
			done <- err
		}()
		receive(t, entered)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		// Taking an uncontended lock ignores ctx, so this open waits on the
		// first one's and gives up at once.
		_, err := f.b.OpenSession(ctx, testWorkload, identitybroker.OpenSessionRequest{
			HostSessionID: openHost, Incarnation: openIncarnation, Owner: testOwner,
		})
		wantError(t, err, identitybroker.CodeUnavailable, "")
		close(release)
		if err := receive(t, done); err != nil {
			t.Fatalf("first open: %v", err)
		}
		var cancelled []identitybroker.SessionOpenRecord
		for _, o := range f.sink.Opens() {
			if o.Code == identitybroker.CodeUnavailable {
				cancelled = append(cancelled, o)
			}
		}
		if len(cancelled) != 1 || cancelled[0].SessionID != "" || cancelled[0].Outcome != identitybroker.OutcomeError || cancelled[0].HostSessionID != openHost {
			t.Fatalf("records of the cancelled open = %+v, want one error naming the host session and no broker session", cancelled)
		}
	})

	t.Run("an unrecorded registration admits nothing", func(t *testing.T) {
		f := newRecordFixture(t)
		s, err := f.open(openHost, testOwner, userToken(t, testOwner))
		if err != nil {
			t.Fatal(err)
		}
		f.sink.refusing(identitybroker.TypeInstanceRegistered)
		reg, err := f.b.RegisterAgent(ctx, testWorkload, mainAgent(s))
		wantError(t, err, identitybroker.CodeUnavailable, "")
		if !reg.Credential.IsZero() {
			t.Fatal("an unrecorded registration returned a credential")
		}
		f.sink.refusing("")
		f.registerMain(t, s)
		if regs := f.sink.Registrations(); len(regs) != 1 || !regs[0].New {
			t.Fatalf("registration records = %+v, want the instance new once recorded", regs)
		}
	})

	t.Run("a refused registration is returned when its record is refused", func(t *testing.T) {
		f := newRecordFixture(t)
		s, err := f.open(openHost, testOwner, identitybroker.Token{})
		if err != nil {
			t.Fatal(err)
		}
		f.sink.refusing(identitybroker.TypeInstanceRegistered)
		req := mainAgent(s)
		req.Owner.Subject = "bob-117"
		_, err = f.b.RegisterAgent(ctx, testWorkload, req)
		wantError(t, err, identitybroker.CodePermissionDenied, "")
	})

	t.Run("an unrecorded delegation stores nothing", func(t *testing.T) {
		f := newDelegationFixture(t)
		f.sink.refusing(identitybroker.TypeDelegation)
		wantError(t, f.report("D41", "R41", f.main.Agent, "R42", f.helper.Agent, false), identitybroker.CodeUnavailable, "")
		// Nothing was stored, so the report is new again once recorded.
		f.sink.refusing("")
		if err := f.report("D41", "R41", f.main.Agent, "R42", f.helper.Agent, false); err != nil {
			t.Fatal(err)
		}
		if d := f.sink.Delegations(); len(d) != 1 || !d[0].DelegatingRunRecorded {
			t.Fatalf("delegation records = %+v, want the report recorded once, as new", d)
		}
	})

	t.Run("a refused delegation is returned when its record is refused", func(t *testing.T) {
		f := newDelegationFixture(t)
		f.sink.refusing(identitybroker.TypeDelegation)
		wantError(t, f.report("D41", "R41", f.helper.Agent, "R42", f.main.Agent, false), identitybroker.CodePermissionDenied, identitybroker.ReasonDenied)
	})

	t.Run("a close takes effect when its record is refused", func(t *testing.T) {
		f := newRecordFixture(t)
		s, err := f.open(openHost, testOwner, userToken(t, testOwner))
		if err != nil {
			t.Fatal(err)
		}
		f.sink.refusing(identitybroker.TypeSessionClose)
		if err := f.b.CloseSession(ctx, testWorkload, closeReq(s)); err != nil {
			t.Fatalf("CloseSession: %v", err)
		}
		if _, found := findSession(t, f.b, openHost); found {
			t.Fatal("a close whose record was refused left the session open")
		}
		if _, ok := identitybroker.SessionGrant(f.b, s.ID); ok {
			t.Fatal("a close whose record was refused kept the refresh token")
		}
		if failures := f.sink.reported(); len(failures) != 1 || failures[0].Type != identitybroker.TypeSessionClose {
			t.Fatalf("delivery failures = %+v, want the close's", failures)
		}
	})

	// closeOnSuccess has f's sink close session id once it accepts a success
	// record of type typ, as a close racing the record would.
	closeOnSuccess := func(f *recordFixture, typ string, id func() string) {
		f.sink.mu.Lock()
		defer f.sink.mu.Unlock()
		f.sink.refuse = func(rec identitybroker.Record) bool {
			if h := rec.Header(); h.Type == typ && h.Outcome == identitybroker.OutcomeSuccess {
				identitybroker.CloseSessionWithReason(f.b, id(), identitybroker.ReasonSessionClosed)
			}
			return false
		}
	}

	t.Run("a registration whose session closes while it is recorded is recorded refused", func(t *testing.T) {
		f := newRecordFixture(t)
		s, err := f.open(openHost, testOwner, userToken(t, testOwner))
		if err != nil {
			t.Fatal(err)
		}
		closeOnSuccess(f, identitybroker.TypeInstanceRegistered, func() string { return s.ID })
		reg, err := f.b.RegisterAgent(ctx, testWorkload, mainAgent(s))
		wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
		if !reg.Credential.IsZero() {
			t.Fatal("a registration whose session closed returned a credential")
		}
		regs := f.sink.Registrations()
		if len(regs) != 2 || regs[0].Outcome != identitybroker.OutcomeSuccess || regs[1].Reason != identitybroker.ReasonSessionClosed ||
			regs[1].Supersedes != regs[0].AuditID || regs[1].AuditID == regs[0].AuditID {
			t.Fatalf("registration records = %+v, want a success then a refusal superseding it", regs)
		}
	})

	t.Run("a delegation whose session closes while it is recorded is recorded refused", func(t *testing.T) {
		f := newDelegationFixture(t)
		closeOnSuccess(f.recordFixture, identitybroker.TypeDelegation, func() string { return f.s.ID })
		err := f.report("D41", "R41", f.main.Agent, "R42", f.helper.Agent, false)
		wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
		d := f.sink.Delegations()
		if len(d) != 2 || d[0].Outcome != identitybroker.OutcomeSuccess || d[1].Reason != identitybroker.ReasonSessionClosed ||
			d[1].Supersedes != d[0].AuditID || d[1].DelegatingRunRecorded {
			t.Fatalf("delegation records = %+v, want a success then a refusal superseding it", d)
		}
	})

	t.Run("a session closed while its open is recorded is not returned", func(t *testing.T) {
		f := newRecordFixture(t)
		var b *identitybroker.Broker
		f.sink.refuse = func(rec identitybroker.Record) bool {
			if o, ok := rec.(*identitybroker.SessionOpenRecord); ok && o.Outcome == identitybroker.OutcomeSuccess {
				identitybroker.CloseSessionWithReason(b, o.SessionID, identitybroker.ReasonSessionClosed)
			}
			return false
		}
		b = f.b
		_, err := f.open(openHost, testOwner, userToken(t, testOwner))
		wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
		opens := f.sink.Opens()
		if _, ok := identitybroker.SessionGrant(f.b, opens[0].SessionID); ok {
			t.Fatal("a session closed during its open's record kept a refresh token")
		}
		// The success is followed by a refusal, so the records show the open
		// was not honoured.
		if len(opens) != 2 || opens[0].Outcome != identitybroker.OutcomeSuccess || opens[1].Reason != identitybroker.ReasonSessionClosed ||
			opens[1].AuditID == opens[0].AuditID || opens[1].Supersedes != opens[0].AuditID || opens[1].User != nil ||
			opens[1].SessionID != opens[0].SessionID {
			t.Fatalf("open records = %+v, want a success then a session_closed refusal", opens)
		}
	})
}

// TestNoCredentialValueInAnyRecord runs every event and checks that no
// access token, refresh token, agent credential, ID-JAG, or gateway token
// value appears in any record or its subjects.
func TestNoCredentialValueInAnyRecord(t *testing.T) {
	ctx := context.Background()
	f := newRunFixture(t)
	f.delegate(t, "D42", "R43")
	if _, err := f.authorize(f.childCall("R43", "D42")); err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
	access := unsignedToken(t, testOwner)
	if _, err := f.b.OpenSession(ctx, testWorkload, identitybroker.OpenSessionRequest{
		HostSessionID: openHost, Incarnation: openIncarnation, Owner: testOwner, AccessToken: access,
	}); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	grant, _ := identitybroker.SessionGrant(f.b, f.s.ID)
	refused := unsignedToken(t, identitybroker.Owner{Issuer: testOwner.Issuer, Subject: "bob-117"})
	_, _ = f.b.OpenSession(ctx, testWorkload, identitybroker.OpenSessionRequest{
		HostSessionID: openHost, Incarnation: openIncarnation, Owner: testOwner, AccessToken: refused,
	})
	if err := f.b.CloseSession(ctx, testWorkload, identitybroker.CloseSessionRequest{SessionID: f.s.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}

	secrets := append(append([]identitybroker.Token{access, refused, grant.RefreshToken, f.cred, f.childCred},
		f.issuer.tokens()...), f.as2.tokens()...)
	var records []identitybroker.Record
	for _, r := range f.rec.Opens() {
		records = append(records, &r)
	}
	for _, r := range f.rec.Closes() {
		records = append(records, &r)
	}
	for _, r := range f.rec.Registrations() {
		records = append(records, &r)
	}
	for _, r := range f.rec.Delegations() {
		records = append(records, &r)
	}
	for _, r := range f.rec.IDJAGs() {
		records = append(records, &r)
	}
	for _, r := range f.rec.Decisions() {
		records = append(records, &r)
	}
	// Token's formatting redacts its value, so a Token held in a record
	// would not show in the text: no record holds one at all.
	if got := tokenFields(reflect.ValueOf(struct{ Leaked identitybroker.Token }{access}), "planted"); len(got) != 1 {
		t.Fatalf("tokenFields found %v in a planted Token, want it", got)
	}
	kinds := map[string]bool{}
	for _, rec := range records {
		if got := tokenFields(reflect.ValueOf(rec), rec.Header().Type); len(got) != 0 {
			t.Errorf("records hold Token fields: %v", got)
		}
		kinds[rec.Header().Type] = true
		text := fmt.Sprintf("%+v %+v", rec, rec.Subjects())
		for _, secret := range secrets {
			if secret.IsZero() {
				t.Fatal("a secret to look for is empty")
			}
			// A JWT's signature alone identifies it.
			parts := strings.Split(secret.Value(), ".")
			if strings.Contains(text, secret.Value()) || strings.Contains(text, parts[len(parts)-1]) && len(parts[len(parts)-1]) > 8 {
				t.Errorf("a %s record carries a credential value", rec.Header().Type)
			}
		}
	}
	for _, typ := range []string{identitybroker.TypeSessionOpen, identitybroker.TypeSessionClose, identitybroker.TypeInstanceRegistered,
		identitybroker.TypeDelegation, identitybroker.TypeIDJAG, identitybroker.TypeToolDecision} {
		if !kinds[typ] {
			t.Errorf("no %s record was checked", typ)
		}
	}
}

// tokenFields returns the path of every non-zero Token v holds.
func tokenFields(v reflect.Value, path string) []string {
	if v.Type() == reflect.TypeFor[identitybroker.Token]() {
		if !v.Interface().(identitybroker.Token).IsZero() {
			return []string{path}
		}
		return nil
	}
	var found []string
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			found = tokenFields(v.Elem(), path)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			found = append(found, tokenFields(v.Field(i), path+"."+v.Type().Field(i).Name)...)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			found = append(found, tokenFields(v.Index(i), fmt.Sprintf("%s[%d]", path, i))...)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			found = append(found, tokenFields(v.MapIndex(k), fmt.Sprintf("%s[%v]", path, k))...)
		}
	}
	return found
}

// TestSessionOpenRecordsTheUserAS1Accepted opens with a real AS-1 token:
// the record keeps the token's AS-1 issuer, subject, and client only once
// AS-1 accepted it.
func TestSessionOpenRecordsTheUserAS1Accepted(t *testing.T) {
	f := newOpenFixture(t)
	rec := identitybroker.NewMemoryRecorder()
	identitybroker.SetRecorder(f.b, rec)
	token := f.token(t, identityharness.Alice)
	var claims struct {
		Iss      string `json:"iss"`
		Sub      string `json:"sub"`
		ClientID string `json:"client_id"`
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.Split(token.Value(), ".")[1])
	if err != nil || json.Unmarshal(raw, &claims) != nil || claims.Iss == "" || claims.Sub == "" {
		t.Fatalf("AS-1's access token has no iss or sub: %v", err)
	}
	if _, err := f.open(openHost, f.owner(identityharness.Alice), token); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	open := rec.Opens()[0]
	want := identitybroker.User{Owner: f.owner(identityharness.Alice), Issuer: claims.Iss, Subject: claims.Sub, ClientID: claims.ClientID}
	if open.Kind != identitybroker.OpenConverted || open.User == nil || *open.User != want {
		t.Fatalf("open record user = %+v, want %+v", open.User, want)
	}

	// A token AS-1 refuses leaves no user in the record.
	f.as1.Faults.FailNextExchange(as1servertest.Fault{Error: "invalid_request", Reason: "subject_token_invalid"})
	if _, err := f.open("hostS43", f.owner(identityharness.Alice), f.token(t, identityharness.Alice)); err == nil {
		t.Fatal("a refused token opened the session")
	}
	refused := rec.Opens()[1]
	if refused.User != nil || refused.Outcome == identitybroker.OutcomeSuccess {
		t.Fatalf("refused open record = %+v, want no user", refused)
	}
}
