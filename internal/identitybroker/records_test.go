package identitybroker_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
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
	// queued waits for the broker's queued close records; nil waits for
	// none.
	queued func()
	// sent is the AuditID of each accepted record, in the order sent.
	sent []string
}

// sentOrder returns the AuditIDs of the accepted records in the order they
// were sent, once the broker has sent the queued ones.
func (s *sink) sentOrder() []string {
	if s.queued != nil {
		s.queued()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

// wait waits for the broker's queued closes and refusals.
func (s *sink) wait() {
	if s.queued != nil {
		s.queued()
	}
}

// Closes returns the close records, once the broker has sent the queued
// ones; Opens, Registrations, and Delegations likewise, as their refusals
// are queued.
func (s *sink) Closes() []identitybroker.SessionCloseRecord {
	s.wait()
	return s.MemoryRecorder.Closes()
}

func (s *sink) Decisions() []identitybroker.DecisionRecord {
	s.wait()
	return s.MemoryRecorder.Decisions()
}

func (s *sink) Opens() []identitybroker.SessionOpenRecord {
	s.wait()
	return s.MemoryRecorder.Opens()
}

func (s *sink) Registrations() []identitybroker.RegistrationRecord {
	s.wait()
	return s.MemoryRecorder.Registrations()
}

func (s *sink) Delegations() []identitybroker.DelegationRecord {
	s.wait()
	return s.MemoryRecorder.Delegations()
}

func newSink() *sink { return &sink{MemoryRecorder: identitybroker.NewMemoryRecorder()} }

func (s *sink) Record(ctx context.Context, rec identitybroker.Record) error {
	s.mu.Lock()
	refuse := s.refuse
	s.mu.Unlock()
	if refuse != nil && refuse(rec) {
		return errors.New("sink unavailable")
	}
	s.mu.Lock()
	s.sent = append(s.sent, rec.Header().AuditID)
	s.mu.Unlock()
	return s.MemoryRecorder.Record(ctx, rec)
}

// refusingSuccesses has s refuse the success records of type typ and accept
// every other record, as a sink that recovers before the refusal is sent.
func (s *sink) refusingSuccesses(typ string) {
	s.wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse = func(rec identitybroker.Record) bool {
		h := rec.Header()
		return h.Type == typ && h.Outcome == identitybroker.OutcomeSuccess
	}
}

// refusing has s refuse the records of type typ, or accept all with "".
func (s *sink) refusing(typ string) {
	// Records queued before the change are sent under the old rule.
	s.wait()
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
	if s.queued != nil {
		s.queued()
	}
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
	f.sink.queued = func() { identitybroker.WaitRecords(f.b) }
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
		identitybroker.SubjectUserClientID: as1Client, identitybroker.SubjectUserClientIssuer: userTokenIssuer,
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
		identitybroker.SubjectUserClientID, identitybroker.SubjectUserClientIssuer, identitybroker.SubjectWorkloadIssuer)
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
		open   func(t *testing.T, f *recordFixture) (identitybroker.Session, error)
		code   identitybroker.Code
		reason identitybroker.Reason
		// closeCause is why a session the open created closes; it defaults
		// to CloseOpenRefused.
		closeCause identitybroker.CloseCause
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
		}, code: identitybroker.CodeInternal, closeCause: identitybroker.CloseMismatchedAnswer},
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
			// A refused open never leaves a session it created; one it found
			// stays open.
			closes := f.sink.Closes()
			if !rec.NewSession {
				if len(closes) != 0 {
					t.Errorf("an open refused on an existing session closed it: %+v", closes)
				}
				return
			}
			cause := tt.closeCause
			if cause == "" {
				cause = identitybroker.CloseOpenRefused
			}
			if len(closes) != 1 || closes[0].SessionID != rec.SessionID || closes[0].Cause != cause || closes[0].SessionRecord != rec.AuditID {
				t.Errorf("close records = %+v, want one %s close of %s naming the refusal %s", closes, cause, rec.SessionID, rec.AuditID)
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
		// When the refused open's record reaches the sink, the close that
		// names it has not: one queue sends both, in order.
		var closeFirst bool
		f.sink.refuse = func(rec identitybroker.Record) bool {
			if o, ok := rec.(*identitybroker.SessionOpenRecord); ok && o.Outcome != identitybroker.OutcomeSuccess {
				closeFirst = len(f.sink.MemoryRecorder.Closes()) != 0
			}
			return false
		}
		if _, err := f.open(openHost, testOwner, userToken(t, testOwner)); err == nil {
			t.Fatal("an undecodable answer opened the session")
		}
		refusal := f.sink.Opens()[0]
		wantClose(t, f, identitybroker.Session{ID: refusal.SessionID}, identitybroker.CloseMismatchedAnswer)
		// The close names the refused open, so its record follows it.
		closed := f.sink.Closes()[0]
		order := f.sink.sentOrder()
		if closeFirst || closed.SessionRecord != refusal.AuditID || slices.Index(order, refusal.AuditID) > slices.Index(order, closed.AuditID) {
			t.Fatalf("records sent %v: close %+v, want it after the refusal %s it names", order, closed, refusal.AuditID)
		}
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
		identitybroker.WaitRecords(f.b)
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
func helperRequest(s identitybroker.Session, parent identitybroker.AgentRef, inc string) identitybroker.RegisterAgentRequest {
	return identitybroker.RegisterAgentRequest{
		SessionID: s.ID, HostSessionID: "hostS-" + inc, Owner: testOwner, Incarnation: inc,
		Definition: conformance.MainDefinition, Parent: parent, ParentHostSessionID: s.HostSessionID,
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
	helper, err := f.b.RegisterAgent(ctx, testWorkload, helperRequest(s, main.Agent, incHelper))
	if err != nil {
		t.Fatalf("RegisterAgent helper: %v", err)
	}
	member, err := f.b.RegisterAgent(ctx, testWorkload, helperRequest(s, main.Agent, incMember))
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
	if h.Agent != helper.Agent || h.Parent != main.Agent || h.ParentRecord != m.AuditID ||
		h.ParentHostSessionID != openHost || h.Incarnation != incHelper {
		t.Fatalf("helper's registration record = %+v, want main's as its parent's", h)
	}
	if tm.Agent != member.Agent || tm.Parent != main.Agent || tm.ParentRecord != m.AuditID || tm.Incarnation != incMember {
		t.Fatalf("second helper's registration record = %+v, want main's as its parent's", tm)
	}
	wantSubject(t, &h, identitybroker.SubjectAgentSPIFFEID, helper.Agent.Subject, identitybroker.ProvenanceVerified)
	wantSubject(t, &h, identitybroker.SubjectAgentInstanceRef, incHelper, identitybroker.ProvenanceReportedByMecak8s)
	wantSubject(t, &h, identitybroker.SubjectUserID, as1Subject, identitybroker.ProvenanceFromRecord(openRec.AuditID))

	// Registering again re-admits the helper; after a reopen it moves.
	if _, err := f.b.RegisterAgent(ctx, testWorkload, helperRequest(s, main.Agent, incHelper)); err != nil {
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
	req := helperRequest(s2, main2.Agent, incHelper)
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
	helper, err := f.b.RegisterAgent(context.Background(), testWorkload, helperRequest(s, main.Agent, incHelper))
	if err != nil {
		t.Fatal(err)
	}
	return delegationFixture{recordFixture: f, s: s, main: main, helper: helper}
}

func (f delegationFixture) report(id, from string, delegating identitybroker.AgentRef, to string, receiving identitybroker.AgentRef) error {
	return f.b.ReportDelegation(context.Background(), testWorkload, identitybroker.ReportDelegationRequest{
		SessionID: f.s.ID, DelegationID: id, DelegatingRun: from, DelegatingAgent: delegating,
		ReceivingRun: to, ReceivingAgent: receiving, Owner: testOwner,
	})
}

func TestDelegationReportsAreLineageEvents(t *testing.T) {
	f := newDelegationFixture(t)
	regs := f.sink.Registrations()
	mainRec, helperRec := regs[0], regs[1]

	// The first report from an unseen run records it as a root run.
	if err := f.report("D41", "R41", f.main.Agent, "R42", f.helper.Agent); err != nil {
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
		first.RunOwner != testOwner || first.Owner != testOwner || !first.DelegatingRunRecorded || first.ParentDelegationRecord != "" {
		t.Fatalf("delegation record = %+v", first)
	}
	wantSubject(t, &first, identitybroker.SubjectRunOwnerID, testOwner.Subject, identitybroker.ProvenanceReportedByMecak8s)
	wantSubject(t, &first, identitybroker.SubjectAgentSPIFFEID, f.main.Agent.Subject, identitybroker.ProvenanceReportedByMecak8s)
	wantSubject(t, &first, identitybroker.SubjectAgentInstanceRef, openIncarnation, identitybroker.ProvenanceReportedByMecak8s)

	// An identical repeat succeeds with no second record.
	if err := f.report("D41", "R41", f.main.Agent, "R42", f.helper.Agent); err != nil {
		t.Fatal(err)
	}
	if n := len(f.sink.Delegations()); n != 1 {
		t.Fatalf("an identical repeat made %d records, want 1", n)
	}

	// A delegation from the delegated run references the one it descends
	// from; the delegating run is known, so this report records none.
	member, err := f.b.RegisterAgent(context.Background(), testWorkload, helperRequest(f.s, f.main.Agent, incMember))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.report("D43", "R42", f.helper.Agent, "R43", member.Agent); err != nil {
		t.Fatal(err)
	}
	nested := f.sink.Delegations()[1]
	if nested.ParentDelegationRecord != first.AuditID || nested.DelegatingRunRecorded {
		t.Fatalf("nested delegation record = %+v, want it to reference %s", nested, first.AuditID)
	}

	// Another run of main, such as a team run, is new and recorded as a root run.
	if err := f.report("D44", "RT1", f.main.Agent, "R44", member.Agent); err != nil {
		t.Fatal(err)
	}
	team := f.sink.Delegations()[2]
	if !team.DelegatingRunRecorded || team.DelegatingRun != "RT1" || team.ParentDelegationRecord != "" {
		t.Fatalf("team delegation record = %+v, want its run recorded as a root run", team)
	}

	// Refusals, including a changed repeat, are recorded with their reasons.
	refusals := []struct {
		name   string
		report func() error
		code   identitybroker.Code
		reason identitybroker.Reason
	}{
		{"changed repeat", func() error { return f.report("D41", "R41", f.main.Agent, "R49", f.helper.Agent) }, identitybroker.CodeAlreadyExists, ""},
		{"receiving run already recorded", func() error { return f.report("D45", "R41", f.main.Agent, "R42", f.helper.Agent) },
			identitybroker.CodePermissionDenied, identitybroker.ReasonDenied},
		{"delegating run bound to another agent", func() error { return f.report("D46", "R41", f.helper.Agent, "R46", member.Agent) },
			identitybroker.CodePermissionDenied, identitybroker.ReasonDenied},
		{"unseen run of a non-root agent", func() error { return f.report("D47", "R-unseen", f.helper.Agent, "R47", member.Agent) },
			identitybroker.CodePermissionDenied, identitybroker.ReasonDenied},
		{"receiving agent not registered", func() error {
			return f.report("D48", "R41", f.main.Agent, "R48", identitybroker.AgentRef{Subject: "spiffe://agents.example.com/agent/ag_unknown"})
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
		wantError(t, err, identitybroker.CodeUnavailable, identitybroker.ReasonRecordNotAccepted)
		if _, found := findSession(t, f.b, openHost); found {
			t.Fatal("an unrecorded open left its session open")
		}
		// The success and the refusal that supersedes it were both refused;
		// the close names the refusal, whose failure is reported.
		failures := f.sink.reported()
		if len(failures) != 2 || failures[0].Type != identitybroker.TypeSessionOpen || failures[1].Type != identitybroker.TypeSessionOpen ||
			failures[0].AuditID == "" || failures[0].AuditID == failures[1].AuditID {
			t.Fatalf("delivery failures = %+v, want the open's and its refusal's", failures)
		}
		closes := f.sink.Closes()
		if len(closes) != 1 || closes[0].Cause != identitybroker.CloseOpenRefused || closes[0].SessionRecord != failures[1].AuditID {
			t.Fatalf("close records = %+v, want one naming the refusal %s", closes, failures[1].AuditID)
		}
	})

	t.Run("an undone open is recorded as refused, superseding its record", func(t *testing.T) {
		f := newRecordFixture(t)
		f.sink.refusingSuccesses(identitybroker.TypeSessionOpen)
		_, err := f.open(openHost, testOwner, userToken(t, testOwner))
		// The open converted the request's token, which is spent: it fails
		// as for token_used, so the client refreshes it.
		wantError(t, err, identitybroker.CodeUnauthenticated, identitybroker.ReasonRecordNotAccepted)
		failures := f.sink.reported()
		opens := f.sink.Opens()
		if len(failures) != 1 || len(opens) != 1 {
			t.Fatalf("delivery failures %+v and open records %+v, want the success refused and its refusal kept", failures, opens)
		}
		refusal := opens[0]
		if refusal.Supersedes != failures[0].AuditID || refusal.AuditID == failures[0].AuditID ||
			refusal.Outcome != identitybroker.OutcomeError || refusal.Reason != identitybroker.ReasonRecordNotAccepted || refusal.User != nil {
			t.Fatalf("refusal = %+v, want it to supersede %s", refusal, failures[0].AuditID)
		}
		if _, found := findSession(t, f.b, openHost); found {
			t.Fatal("an undone open left its session open")
		}
		closes := f.sink.Closes()
		if len(closes) != 1 || closes[0].Cause != identitybroker.CloseOpenRefused || closes[0].SessionRecord != refusal.AuditID {
			t.Fatalf("close records = %+v, want one naming the refusal %s", closes, refusal.AuditID)
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
		wantError(t, err, identitybroker.CodeUnauthenticated, identitybroker.ReasonRecordNotAccepted, held.RefreshToken)
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
		token := userToken(t, testOwner)
		_, err := f.open(openHost, testOwner, token)
		wantError(t, err, identitybroker.CodeUnauthenticated, identitybroker.ReasonRecordNotAccepted)
		id := f.sink.Closes()[0].SessionID
		if _, ok := identitybroker.SessionGrant(f.b, id); ok {
			t.Fatal("an unrecorded conversion stored its refresh token")
		}
		wantNoVerifiedOwner(t, f.b, id)
		// The broker still remembers the converted jti, so the spent token
		// is not presented to AS-1 again.
		if n := identitybroker.ConvertedTokens(f.b); n != 1 {
			t.Fatalf("%d converted tokens remembered, want 1", n)
		}
		f.sink.refusing("")
		_, err = f.open(openHost, testOwner, token)
		wantError(t, err, identitybroker.CodeUnauthenticated, identitybroker.ReasonTokenUsed)
		if n := f.as1.count(); n != 1 {
			t.Fatalf("%d exchanges, want the spent token presented once", n)
		}
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
		failures := f.sink.reported()
		if len(failures) != 1 || failures[0].Type != identitybroker.TypeSessionOpen {
			t.Fatalf("delivery failures = %+v, want the refused open's", failures)
		}
		closes := f.sink.Closes()
		if len(closes) != 1 || closes[0].Cause != identitybroker.CloseOpenRefused || closes[0].SessionRecord != failures[0].AuditID {
			t.Fatalf("close records = %+v, want one naming the refusal %s", closes, failures[0].AuditID)
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
		wantError(t, err, identitybroker.CodeUnavailable, identitybroker.ReasonRecordNotAccepted)
		if !reg.Credential.IsZero() {
			t.Fatal("an unrecorded registration returned a credential")
		}
		f.sink.refusing("")
		f.registerMain(t, s)
		if regs := f.sink.Registrations(); len(regs) != 1 || !regs[0].New {
			t.Fatalf("registration records = %+v, want the instance new once recorded", regs)
		}
	})

	t.Run("an undone registration is recorded as refused, superseding its record", func(t *testing.T) {
		f := newRecordFixture(t)
		s, err := f.open(openHost, testOwner, userToken(t, testOwner))
		if err != nil {
			t.Fatal(err)
		}
		f.sink.refusingSuccesses(identitybroker.TypeInstanceRegistered)
		_, err = f.b.RegisterAgent(ctx, testWorkload, mainAgent(s))
		wantError(t, err, identitybroker.CodeUnavailable, identitybroker.ReasonRecordNotAccepted)
		failures, regs := f.sink.reported(), f.sink.Registrations()
		if len(failures) != 1 || len(regs) != 1 || regs[0].Supersedes != failures[0].AuditID || regs[0].AuditID == failures[0].AuditID ||
			regs[0].Outcome != identitybroker.OutcomeError || regs[0].Reason != identitybroker.ReasonRecordNotAccepted || !regs[0].CredentialExpiresAt.IsZero() {
			t.Fatalf("failures %+v, registration records %+v; want one refusal superseding the unaccepted success", failures, regs)
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
		wantError(t, f.report("D41", "R41", f.main.Agent, "R42", f.helper.Agent), identitybroker.CodeUnavailable, identitybroker.ReasonRecordNotAccepted)
		// Nothing was stored, so the report is new again once recorded.
		f.sink.refusing("")
		if err := f.report("D41", "R41", f.main.Agent, "R42", f.helper.Agent); err != nil {
			t.Fatal(err)
		}
		if d := f.sink.Delegations(); len(d) != 1 || !d[0].DelegatingRunRecorded {
			t.Fatalf("delegation records = %+v, want the report recorded once, as new", d)
		}
	})

	t.Run("an undone delegation is recorded as refused, superseding its record", func(t *testing.T) {
		f := newDelegationFixture(t)
		f.sink.refusingSuccesses(identitybroker.TypeDelegation)
		wantError(t, f.report("D41", "R41", f.main.Agent, "R42", f.helper.Agent), identitybroker.CodeUnavailable, identitybroker.ReasonRecordNotAccepted)
		failures, d := f.sink.reported(), f.sink.Delegations()
		if len(failures) != 1 || len(d) != 1 || d[0].Supersedes != failures[0].AuditID || d[0].AuditID == failures[0].AuditID ||
			d[0].Outcome != identitybroker.OutcomeError || d[0].Reason != identitybroker.ReasonRecordNotAccepted || d[0].DelegatingRunRecorded {
			t.Fatalf("failures %+v, delegation records %+v; want one refusal superseding the unaccepted success", failures, d)
		}
	})

	t.Run("a refused delegation is returned when its record is refused", func(t *testing.T) {
		f := newDelegationFixture(t)
		f.sink.refusing(identitybroker.TypeDelegation)
		wantError(t, f.report("D41", "R41", f.helper.Agent, "R42", f.main.Agent), identitybroker.CodePermissionDenied, identitybroker.ReasonDenied)
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
		err := f.report("D41", "R41", f.main.Agent, "R42", f.helper.Agent)
		wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
		d := f.sink.Delegations()
		if len(d) != 2 || d[0].Outcome != identitybroker.OutcomeSuccess || d[1].Reason != identitybroker.ReasonSessionClosed ||
			d[1].Supersedes != d[0].AuditID || d[1].DelegatingRunRecorded {
			t.Fatalf("delegation records = %+v, want a success then a refusal superseding it", d)
		}
	})

	t.Run("a session closed while its open is recorded is not returned", func(t *testing.T) {
		f := newRecordFixture(t)
		// Only an existing session can close meanwhile: a new one is not in
		// the table until its open is recorded.
		s, err := f.open(openHost, testOwner, identitybroker.Token{})
		if err != nil {
			t.Fatal(err)
		}
		var b *identitybroker.Broker
		f.sink.refuse = func(rec identitybroker.Record) bool {
			if o, ok := rec.(*identitybroker.SessionOpenRecord); ok && o.Outcome == identitybroker.OutcomeSuccess && o.Kind == identitybroker.OpenConverted {
				identitybroker.CloseSessionWithReason(b, o.SessionID, identitybroker.ReasonSessionClosed)
			}
			return false
		}
		b = f.b
		_, err = f.open(openHost, testOwner, userToken(t, testOwner))
		wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
		if _, ok := identitybroker.SessionGrant(f.b, s.ID); ok {
			t.Fatal("a session closed during its open's record kept a refresh token")
		}
		// The success is followed by a refusal, so the records show the open
		// was not honoured.
		opens := f.sink.Opens()[1:]
		if len(opens) != 2 || opens[0].Outcome != identitybroker.OutcomeSuccess || opens[1].Reason != identitybroker.ReasonSessionClosed ||
			opens[1].AuditID == opens[0].AuditID || opens[1].Supersedes != opens[0].AuditID || opens[1].User != nil ||
			opens[1].SessionID != s.ID || opens[0].SessionID != s.ID {
			t.Fatalf("open records = %+v, want a success then a session_closed refusal", opens)
		}
	})

	t.Run("a new session cannot be reached while its open is recorded", func(t *testing.T) {
		f := newRecordFixture(t)
		var b *identitybroker.Broker
		found := make(chan bool, 1)
		var closeErr, regErr, reportErr error
		f.sink.refuse = func(rec identitybroker.Record) bool {
			o, ok := rec.(*identitybroker.SessionOpenRecord)
			if !ok || o.Outcome != identitybroker.OutcomeSuccess {
				return false
			}
			// A lookup waits for the open, which holds its host session's
			// open lock while it is recorded.
			go func() {
				_, ok, _ := b.FindSession(ctx, testWorkload, identitybroker.FindSessionRequest{RootHostSessionID: openHost})
				found <- ok
			}()
			waitForHostLockRefs(t, b, openHost, 2)
			pending := identitybroker.Session{ID: o.SessionID, HostSessionID: openHost, Owner: testOwner, Incarnation: openIncarnation}
			_, regErr = b.RegisterAgent(ctx, testWorkload, mainAgent(pending))
			reportErr = b.ReportDelegation(ctx, testWorkload, identitybroker.ReportDelegationRequest{
				SessionID: o.SessionID, DelegationID: "D41", DelegatingRun: "R41", DelegatingAgent: identitybroker.AgentRef{Subject: "spiffe://agents.example.com/agent/ag_x"},
				ReceivingRun: "R42", ReceivingAgent: identitybroker.AgentRef{Subject: "spiffe://agents.example.com/agent/ag_y"}, Owner: testOwner,
			})
			closeErr = b.CloseSession(ctx, testWorkload, closeReq(pending))
			// The sink then refuses the record, so the open is undone.
			return true
		}
		b = f.b
		_, err := f.open(openHost, testOwner, identitybroker.Token{})
		wantError(t, err, identitybroker.CodeUnavailable, identitybroker.ReasonRecordNotAccepted)
		if receive(t, found) {
			t.Error("FindSession found a new session whose open was undone")
		}
		for name, err := range map[string]error{"RegisterAgent": regErr, "ReportDelegation": reportErr, "CloseSession": closeErr} {
			var e *identitybroker.Error
			if !errors.As(err, &e) || e.Code != identitybroker.CodePermissionDenied {
				t.Errorf("%s during the open's record = %v, want the unknown-session refusal", name, err)
			}
		}
		// Nothing refers to the open the sink did not accept.
		if regs, d := f.sink.Registrations(), f.sink.Delegations(); len(regs) != 1 || regs[0].SessionRecord != "" || len(d) != 1 || d[0].UserRecord != "" {
			t.Fatalf("registration records %+v and delegation records %+v, want refusals naming no open record", regs, d)
		}
	})

	t.Run("a close racing a new session's open takes effect", func(t *testing.T) {
		// As when mecak8s deletes the host session while its open is
		// recorded: the find waits for the open, and the close then reaches
		// the session it opened.
		f := newRecordFixture(t)
		var b *identitybroker.Broker
		closed := make(chan error, 1)
		var once sync.Once
		f.sink.refuse = func(rec identitybroker.Record) bool {
			o, ok := rec.(*identitybroker.SessionOpenRecord)
			if !ok || o.Outcome != identitybroker.OutcomeSuccess {
				return false
			}
			once.Do(func() {
				go func() {
					s, ok, err := b.FindSession(ctx, testWorkload, identitybroker.FindSessionRequest{RootHostSessionID: openHost})
					if err == nil && ok {
						err = b.CloseSession(ctx, testWorkload, closeReq(s))
					} else if err == nil {
						err = errors.New("the find missed the session its open was recording")
					}
					closed <- err
				}()
				waitForHostLockRefs(t, b, openHost, 2)
			})
			return false
		}
		b = f.b
		s, err := f.open(openHost, testOwner, userToken(t, testOwner))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if err := receive(t, closed); err != nil {
			t.Fatalf("find-then-close during the open's record: %v", err)
		}
		if _, found := findSession(t, f.b, openHost); found {
			t.Fatal("the session opened while a close raced it stayed open")
		}
		if _, ok := identitybroker.SessionGrant(f.b, s.ID); ok {
			t.Fatal("the closed session kept the refresh token its open stored")
		}
		if closes := f.sink.Closes(); len(closes) != 1 || closes[0].SessionID != s.ID || closes[0].Cause != identitybroker.CloseRequested {
			t.Fatalf("close records = %+v, want the requested close of %s", closes, s.ID)
		}
	})

	t.Run("a close of the host session closes a new session its open is recording", func(t *testing.T) {
		// As when mecak8s deletes the host session while its open is
		// recorded, however long the open takes: the close does not wait for
		// the open, which then fails and stores nothing. The close runs
		// inside the open's record, so waiting for the open would deadlock.
		f := newRecordFixture(t)
		var b *identitybroker.Broker
		var closeErr error
		var once sync.Once
		f.sink.refuse = func(rec identitybroker.Record) bool {
			if o, ok := rec.(*identitybroker.SessionOpenRecord); ok && o.Outcome == identitybroker.OutcomeSuccess {
				once.Do(func() {
					closeErr = b.CloseSession(ctx, testWorkload, identitybroker.CloseSessionRequest{RootHostSessionID: openHost, Reason: identitybroker.ReasonSessionClosed})
				})
			}
			return false
		}
		b = f.b
		_, err := f.open(openHost, testOwner, userToken(t, testOwner))
		wantError(t, err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
		if closeErr != nil {
			t.Fatalf("closing the host session during its open: %v", closeErr)
		}
		if _, found := findSession(t, f.b, openHost); found {
			t.Fatal("the session opened while its host session was closed stayed open")
		}
		opens := f.sink.Opens()
		if len(opens) != 2 || opens[0].Outcome != identitybroker.OutcomeSuccess || !opens[0].NewSession ||
			opens[1].Supersedes != opens[0].AuditID || opens[1].Outcome != identitybroker.OutcomeDenied || opens[1].Reason != identitybroker.ReasonSessionClosed {
			t.Fatalf("open records = %+v, want a success then a denied refusal superseding it", opens)
		}
		if _, ok := identitybroker.SessionGrant(f.b, opens[0].SessionID); ok {
			t.Fatal("the closed session kept the refresh token its open got")
		}
		closes := f.sink.Closes()
		if len(closes) != 1 || closes[0].SessionID != opens[0].SessionID || closes[0].Cause != identitybroker.CloseRequested || closes[0].SessionRecord != opens[0].AuditID {
			t.Fatalf("close records = %+v, want the requested close of %s naming its open", closes, opens[0].SessionID)
		}
		// The host session opens again as usual.
		if _, err := f.open(openHost, testOwner, userToken(t, testOwner)); err != nil {
			t.Fatalf("open after the host session's close: %v", err)
		}
	})

	t.Run("a close of a host session with no broker session succeeds", func(t *testing.T) {
		f := newRecordFixture(t)
		if err := f.b.CloseSession(ctx, testWorkload, identitybroker.CloseSessionRequest{RootHostSessionID: "hostS99", Reason: identitybroker.ReasonSessionClosed}); err != nil {
			t.Fatalf("CloseSession(host with none): %v", err)
		}
		if closes := f.sink.Closes(); len(closes) != 0 {
			t.Fatalf("close records = %+v, want none", closes)
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
	identitybroker.WaitRecords(f.b)
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
	identitybroker.WaitRecords(f.b)
	refused := rec.Opens()[1]
	if refused.User != nil || refused.Outcome == identitybroker.OutcomeSuccess {
		t.Fatalf("refused open record = %+v, want no user", refused)
	}
}

// TestSubjectKeysAreTheProfiles pins the Subjects keys to audit.md's
// AuditEvent profile, which #2566 maps the records onto by name. The user's
// token client has keys of its own, apart from the general client_id.
func TestSubjectKeysAreTheProfiles(t *testing.T) {
	want := map[string]string{
		identitybroker.SubjectUserIssuer: "user_iss", identitybroker.SubjectUserID: "user_id",
		identitybroker.SubjectOwnerIssuer: "owner_iss", identitybroker.SubjectOwnerID: "owner_id",
		identitybroker.SubjectUserClientID: "user_client_id", identitybroker.SubjectUserClientIssuer: "user_client_iss",
		identitybroker.SubjectWorkloadIssuer: "workload_iss", identitybroker.SubjectWorkloadSubject: "workload_sub",
		identitybroker.SubjectAgentSPIFFEID: "agent_spiffe_id", identitybroker.SubjectAgentActorID: "agent_actor_id",
		identitybroker.SubjectAgentInstanceRef: "agent_instance_ref",
		identitybroker.SubjectRunOwnerIssuer:   "run_owner_iss", identitybroker.SubjectRunOwnerID: "run_owner_id",
	}
	for got, profile := range want {
		if got != profile {
			t.Errorf("Subjects key %q, want the profile's %q", got, profile)
		}
	}
}

// TestADelegationReportedAgainRefersToItsEarlierRecord pins that a
// delegation reported again into a reopened broker session, as the holder
// does after a reopen, refers to the record of its earlier report.
func TestADelegationReportedAgainRefersToItsEarlierRecord(t *testing.T) {
	ctx := context.Background()
	f := newDelegationFixture(t)
	if err := f.report("D41", "R41", f.main.Agent, "R42", f.helper.Agent); err != nil {
		t.Fatal(err)
	}
	first := f.sink.Delegations()[0]
	if first.PreviousRecord != "" {
		t.Fatalf("first report's record = %+v, want no previous record", first)
	}
	// A changed repeat in the same session is refused and refers to no
	// earlier session's record.
	wantError(t, f.report("D41", "R41", f.main.Agent, "R49", f.helper.Agent), identitybroker.CodeAlreadyExists, "")
	if d := f.sink.Delegations(); len(d) != 2 || d[1].PreviousRecord != "" {
		t.Fatalf("delegation records = %+v, want the refused repeat to name no previous record", d)
	}
	// The session closes on its own, as when its grant is lost, and the root
	// reopens.
	identitybroker.CloseSessionWithReason(f.b, f.s.ID, identitybroker.ReasonSessionClosed)
	s2, err := f.open(openHost, testOwner, userToken(t, testOwner))
	if err != nil {
		t.Fatal(err)
	}
	main2 := f.registerMain(t, s2)
	helper2, err := f.b.RegisterAgent(ctx, testWorkload, helperRequest(s2, main2.Agent, incHelper))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.b.ReportDelegation(ctx, testWorkload, identitybroker.ReportDelegationRequest{
		SessionID: s2.ID, DelegationID: "D41", DelegatingRun: "R41", DelegatingAgent: main2.Agent,
		ReceivingRun: "R42", ReceivingAgent: helper2.Agent, Owner: testOwner,
	}); err != nil {
		t.Fatalf("report again: %v", err)
	}
	d := f.sink.Delegations()
	if len(d) != 3 || d[2].SessionID != s2.ID || d[2].PreviousRecord != first.AuditID || d[2].AuditID == first.AuditID {
		t.Fatalf("delegation records = %+v, want the third to refer to %s", d, first.AuditID)
	}

	// Closing an earlier broker session of the root, already closed, as
	// mecak8s does for a create that lost a race, leaves the current
	// session's reports.
	if err := f.b.CloseSession(ctx, testWorkload, identitybroker.CloseSessionRequest{SessionID: f.s.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	if n := identitybroker.PreviousReports(f.b, openHost); n != 1 {
		t.Fatalf("%d reports kept after closing an earlier session, want 1", n)
	}
	// mecak8s closing the root's current session itself, as for an earlier
	// incarnation, forgets the root's reports: nothing reports them again.
	if err := f.b.CloseSession(ctx, testWorkload, identitybroker.CloseSessionRequest{SessionID: s2.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	if n := identitybroker.PreviousReports(f.b, openHost); n != 0 {
		t.Fatalf("%d reports kept for the closed root, want none", n)
	}

	// So does closing the host session, as on a delete, when it has no open
	// broker session left, here after its session closed on its own.
	s3, err := f.open(openHost, testOwner, userToken(t, testOwner))
	if err != nil {
		t.Fatal(err)
	}
	main3 := f.registerMain(t, s3)
	helper3, err := f.b.RegisterAgent(ctx, testWorkload, helperRequest(s3, main3.Agent, incHelper))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.b.ReportDelegation(ctx, testWorkload, identitybroker.ReportDelegationRequest{
		SessionID: s3.ID, DelegationID: "D41", DelegatingRun: "R41", DelegatingAgent: main3.Agent,
		ReceivingRun: "R42", ReceivingAgent: helper3.Agent, Owner: testOwner,
	}); err != nil {
		t.Fatalf("report in a third session: %v", err)
	}
	identitybroker.CloseSessionWithReason(f.b, s3.ID, identitybroker.ReasonSessionClosed)
	if n := identitybroker.PreviousReports(f.b, openHost); n != 1 {
		t.Fatalf("%d reports kept after the session closed on its own, want 1", n)
	}
	// Another workload's close of the host session forgets nothing.
	other := identitybroker.Workload{Name: "system:serviceaccount:other:other"}
	if err := f.b.CloseSession(ctx, other, identitybroker.CloseSessionRequest{RootHostSessionID: openHost, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	if n := identitybroker.PreviousReports(f.b, openHost); n != 1 {
		t.Fatalf("%d reports kept after another workload closed the host session, want 1", n)
	}
	if err := f.b.CloseSession(ctx, testWorkload, identitybroker.CloseSessionRequest{RootHostSessionID: openHost, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	if n := identitybroker.PreviousReports(f.b, openHost); n != 0 {
		t.Fatalf("%d reports kept for the deleted host session, want none", n)
	}
}

// TestAnInvalidRequestIsRecordedAsRefused pins that an open, registration,
// or delegation report refused because it failed validation is recorded with
// its reason and the calling workload, and with none of the request's
// unvalidated fields.
func TestAnInvalidRequestIsRecordedAsRefused(t *testing.T) {
	ctx := context.Background()
	f := newRecordFixture(t)
	const junk = "not an id"
	_, err := f.b.OpenSession(ctx, testWorkload, identitybroker.OpenSessionRequest{HostSessionID: junk, Incarnation: openIncarnation, Owner: testOwner})
	wantError(t, err, identitybroker.CodeInvalidArgument, "")
	_, err = f.b.RegisterAgent(ctx, testWorkload, identitybroker.RegisterAgentRequest{SessionID: junk})
	wantError(t, err, identitybroker.CodeInvalidArgument, "")
	err = f.b.ReportDelegation(ctx, testWorkload, identitybroker.ReportDelegationRequest{SessionID: junk})
	wantError(t, err, identitybroker.CodeInvalidArgument, "")
	opens, regs, d := f.sink.Opens(), f.sink.Registrations(), f.sink.Delegations()
	if len(opens) != 1 || len(regs) != 1 || len(d) != 1 {
		t.Fatalf("records: %d opens, %d registrations, %d delegations; want one refusal of each", len(opens), len(regs), len(d))
	}
	for _, rec := range []identitybroker.Record{&opens[0], &regs[0], &d[0]} {
		h := rec.Header()
		if h.Outcome != identitybroker.OutcomeDenied || h.Code != identitybroker.CodeInvalidArgument || !strings.HasPrefix(h.AuditID, "rec_") {
			t.Errorf("%s record header = %+v, want an INVALID_ARGUMENT refusal", h.Type, h)
		}
		if text := fmt.Sprintf("%+v", rec); strings.Contains(text, junk) {
			t.Errorf("%s record carries the request's unvalidated fields: %s", h.Type, text)
		}
		wantSubject(t, rec, identitybroker.SubjectWorkloadSubject, testWorkload.Name, identitybroker.ProvenanceReportedByMecak8s)
	}
}

// TestAVerifiedWorkloadIsQualifiedByItsIssuer pins that a verified workload's
// subjects carry the issuer its authenticator verified, and that an
// unverified workload's claimed issuer is never recorded.
func TestAVerifiedWorkloadIsQualifiedByItsIssuer(t *testing.T) {
	const issuer = "https://kubernetes.default.svc"
	verified := &identitybroker.SessionCloseRecord{Workload: identitybroker.Workload{Name: testWorkload.Name, Verified: true, Issuer: issuer}}
	wantSubject(t, verified, identitybroker.SubjectWorkloadIssuer, issuer, identitybroker.ProvenanceVerified)
	wantSubject(t, verified, identitybroker.SubjectWorkloadSubject, testWorkload.Name, identitybroker.ProvenanceVerified)
	claimed := &identitybroker.SessionCloseRecord{Workload: identitybroker.Workload{Name: testWorkload.Name, Issuer: issuer}}
	wantUnavailable(t, claimed, identitybroker.SubjectWorkloadIssuer)
	wantSubject(t, claimed, identitybroker.SubjectWorkloadSubject, testWorkload.Name, identitybroker.ProvenanceReportedByMecak8s)
}

// TestLineageIsMarkedAsReportedByMecak8s pins that registration and
// delegation records mark their lineage facts as reported by mecak8s, as
// audit.md "Lineage" requires, whether accepted or refused.
func TestLineageIsMarkedAsReportedByMecak8s(t *testing.T) {
	f := newDelegationFixture(t)
	if err := f.report("D41", "R41", f.main.Agent, "R42", f.helper.Agent); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.report("D45", "R41", f.main.Agent, "R42", f.helper.Agent), identitybroker.CodePermissionDenied, identitybroker.ReasonDenied)
	for _, r := range f.sink.Registrations() {
		if r.LineageProvenance != identitybroker.ProvenanceReportedByMecak8s {
			t.Errorf("registration record %s lineage provenance = %q", r.AuditID, r.LineageProvenance)
		}
	}
	for _, d := range f.sink.Delegations() {
		if d.LineageProvenance != identitybroker.ProvenanceReportedByMecak8s {
			t.Errorf("delegation record %s lineage provenance = %q", d.AuditID, d.LineageProvenance)
		}
	}
}

// TestACloseDoesNotWaitOnTheSink pins that a close takes effect and returns
// while the sink is stalled on its record, which is sent apart from the call
// and still reaches the sink.
func TestACloseDoesNotWaitOnTheSink(t *testing.T) {
	ctx := context.Background()
	f := newRecordFixture(t)
	s, err := f.open(openHost, testOwner, userToken(t, testOwner))
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	f.sink.mu.Lock()
	f.sink.refuse = func(rec identitybroker.Record) bool {
		if rec.Header().Type == identitybroker.TypeSessionClose {
			<-release
		}
		return false
	}
	f.sink.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		done <- f.b.CloseSession(ctx, testWorkload, identitybroker.CloseSessionRequest{SessionID: s.ID, Reason: identitybroker.ReasonSessionClosed})
	}()
	if err := receive(t, done); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	if _, found := findSession(t, f.b, openHost); found {
		t.Fatal("the close did not take effect before its record was sent")
	}
	close(release)
	if closes := f.sink.Closes(); len(closes) != 1 || closes[0].SessionID != s.ID {
		t.Fatalf("close records = %+v, want the close once the sink recovers", closes)
	}
}

// deadlineRecorder is a MemoryRecorder that notes each record sent without
// a deadline within ten seconds.
type deadlineRecorder struct {
	*identitybroker.MemoryRecorder
	mu      sync.Mutex
	without []string
}

func (r *deadlineRecorder) Record(ctx context.Context, rec identitybroker.Record) error {
	if d, ok := ctx.Deadline(); !ok || time.Until(d) > 10*time.Second {
		r.mu.Lock()
		r.without = append(r.without, rec.Header().Type)
		r.mu.Unlock()
	}
	return r.MemoryRecorder.Record(ctx, rec)
}

// TestEveryRecordIsSentWithADeadline pins that the broker bounds every send
// to the sink, waited or queued, so a slow sink fails a waited record as not
// accepted instead of holding the call.
func TestEveryRecordIsSentWithADeadline(t *testing.T) {
	ctx := context.Background()
	f := newRecordFixture(t)
	rec := &deadlineRecorder{MemoryRecorder: identitybroker.NewMemoryRecorder()}
	identitybroker.SetRecorder(f.b, rec)
	s, err := f.open(openHost, testOwner, userToken(t, testOwner))
	if err != nil {
		t.Fatal(err)
	}
	f.registerMain(t, s)
	if _, err := f.open(openHost, identitybroker.Owner{Issuer: testOwner.Issuer, Subject: "bob-117"}, identitybroker.Token{}); err == nil {
		t.Fatal("an open for another owner succeeded")
	}
	if err := f.b.CloseSession(ctx, testWorkload, identitybroker.CloseSessionRequest{SessionID: s.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	identitybroker.WaitRecords(f.b)
	if n := len(rec.Opens()) + len(rec.Registrations()) + len(rec.Closes()); n != 4 {
		t.Fatalf("%d records, want an open, a refused open, a registration, and a close", n)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.without) != 0 {
		t.Fatalf("records sent without a bounded deadline: %v", rec.without)
	}
}

// TestARefusalDoesNotWaitOnTheSink pins that a refusal is returned while the
// sink is stalled on its record, without holding the per-key lock it was
// refused under: the next registration of the same incarnation goes ahead.
func TestARefusalDoesNotWaitOnTheSink(t *testing.T) {
	ctx := context.Background()
	f := newRecordFixture(t)
	s, err := f.open(openHost, testOwner, userToken(t, testOwner))
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	f.sink.mu.Lock()
	f.sink.refuse = func(rec identitybroker.Record) bool {
		if h := rec.Header(); h.Outcome != identitybroker.OutcomeSuccess && h.Type != identitybroker.TypeSessionClose {
			<-release
		}
		return false
	}
	f.sink.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		other := identitybroker.Owner{Issuer: testOwner.Issuer, Subject: "bob-117"}
		_, openErr := f.open(openHost, other, identitybroker.Token{})
		req := mainAgent(s)
		req.Owner = other
		_, regErr := f.b.RegisterAgent(ctx, testWorkload, req)
		// A denied call is a refusal too.
		_, callErr := f.b.AuthorizeCall(ctx, testWorkload, callRequest(s.ID, identitybroker.NewToken("not-a-credential")))
		if openErr == nil || regErr == nil || callErr == nil {
			done <- errors.New("a refusal succeeded")
			return
		}
		// The same incarnation registers while its refusal's record waits.
		_, err := f.b.RegisterAgent(ctx, testWorkload, mainAgent(s))
		done <- err
	}()
	if err := receive(t, done); err != nil {
		t.Fatalf("refusals and a registration with the sink stalled: %v", err)
	}
	close(release)
	identitybroker.WaitRecords(f.b)
	regs := f.sink.Registrations()
	if len(regs) != 2 || regs[0].Outcome != identitybroker.OutcomeSuccess || regs[1].Outcome != identitybroker.OutcomeDenied {
		t.Fatalf("registration records = %+v, want the success, then the refusal once the sink recovered", regs)
	}
	if d := f.sink.Decisions(); len(d) != 1 || d[0].Outcome != identitybroker.OutcomeDenied {
		t.Fatalf("decision records = %+v, want the call's refusal once the sink recovered", d)
	}
}
