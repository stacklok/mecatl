package auditsink_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/toolhive-core/audit"

	ib "github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/auditsink"
)

var (
	at     = time.Date(2026, 11, 28, 9, 30, 0, 123, time.FixedZone("CET", 3600))
	origin = auditsink.Origin{Replica: "mecak8s-0", Deployment: "https://broker.example.com", TrustDomain: "agents.example.com"}
	owner  = ib.Owner{Issuer: "https://idp.example.com", Subject: "alice-204"}
	user   = ib.User{Owner: owner, Issuer: "https://as1.example.com", Subject: "u-alice-91", ClientID: "mecatui"}
	wl     = ib.Workload{Name: "system:serviceaccount:mecatl:mecak8s", Verified: true, Issuer: "https://kubernetes.default.svc"}
	agent  = ib.AgentRef{Subject: "spiffe://agents.example.com/agent/ag_7hq2x9"}
	parent = ib.AgentRef{Subject: "spiffe://agents.example.com/agent/ag_main01"}
)

// samples returns one record of each type, every field set.
func samples() []ib.Record {
	h := func(typ string) ib.RecordHeader {
		return ib.RecordHeader{AuditID: "rec_" + typ, Type: typ, At: at, Outcome: ib.OutcomeSuccess}
	}
	return []ib.Record{
		&ib.SessionOpenRecord{
			RecordHeader: h(ib.TypeSessionOpen), Workload: wl, HostSessionID: "A42", Incarnation: "inc-1",
			ReportedOwner: owner, SessionID: "bs_1", NewSession: true, Kind: ib.OpenConverted, TokenPresented: true,
			SourceJTI: "jti-src", User: &user, HardExpiry: at.Add(8 * time.Hour),
			Replaced: &ib.ReplacedGrant{OpenRecord: "rec_old", SourceJTI: "jti-old"},
		},
		&ib.SessionCloseRecord{
			RecordHeader: h(ib.TypeSessionClose), SessionID: "bs_1", HostSessionID: "A42", Workload: wl, Owner: owner,
			ClosingReason: ib.ReasonSessionClosed, Cause: ib.CloseRequested, SessionRecord: "rec_open", UserRecord: "rec_open", User: user,
		},
		&ib.RegistrationRecord{
			RecordHeader: h(ib.TypeInstanceRegistered), Workload: wl, SessionID: "bs_1", SessionRecord: "rec_open",
			HostSessionID: "A42-child", Owner: owner, Incarnation: "inc-2",
			Definition: ib.DefinitionRef{Source: "global", Name: "reviewer", Revision: "r1"},
			Agent:      agent, AgentID: "ag_7hq2x9", New: true, Parent: parent, ParentHostSessionID: "A42", ParentRecord: "rec_reg_main",
			CredentialExpiresAt: at.Add(time.Hour), UserRecord: "rec_open", User: user, LineageProvenance: ib.ProvenanceReportedByMecak8s,
		},
		&ib.DelegationRecord{
			RecordHeader: h(ib.TypeDelegation), Workload: wl, SessionID: "bs_1", DelegationID: "D42",
			DelegatingRun: "R41", DelegatingAgent: parent, DelegatingAgentID: "ag_main01", DelegatingInstance: "inc-1",
			DelegatingRecord: "rec_reg_main", ParentDelegationRecord: "rec_del_parent",
			ReceivingRun: "R42", ReceivingAgent: agent, ReceivingAgentID: "ag_7hq2x9", ReceivingRecord: "rec_reg",
			RunOwner: owner, UserRecord: "rec_open", User: user, Owner: owner, LineageProvenance: ib.ProvenanceReportedByMecak8s,
		},
		&ib.IDJAGRecord{
			RecordHeader: h(ib.TypeIDJAG), SessionID: "bs_1", Workload: wl, Agent: agent, AgentID: "ag_7hq2x9",
			RunID: "R42", DelegationID: "D42", RunOwner: owner, Owner: owner, UserRecord: "rec_open", User: user,
			AgentInstance: "inc-2", RegistrationRecord: "rec_reg", DelegationRecord: "rec_del",
			OperationID: "op-pr42-001", AttemptID: "att-1", IDJAG: ib.CredentialRef{Issuer: "https://as1.example.com", ID: "jag-pr42-001"},
			ExpiresAt: at.Add(time.Minute),
		},
		decision(),
	}
}

func decision() *ib.DecisionRecord {
	return &ib.DecisionRecord{
		RecordHeader: ib.RecordHeader{AuditID: "rec_decision", Type: ib.TypeToolDecision, At: at, Outcome: ib.OutcomeSuccess},
		SessionID:    "bs_1", Workload: wl, Owner: owner, OwnerVerified: true, UserRecord: "rec_open", User: user,
		Agent: agent, AgentID: "ag_7hq2x9", AgentInstance: "inc-2", RegistrationRecord: "rec_reg",
		RunID: "R42", DelegationID: "D42", DelegationRecord: "rec_del", RunOwner: owner,
		OperationID: "op-pr42-001", AttemptID: "att-1", Method: "tools/call", Server: "github", Tool: "pull_request_read",
		ArgumentsSHA256: "abc123", Ceiling: "github:[pull_request_read]",
		IDJAG:          ib.CredentialRef{Issuer: "https://as1.example.com", ID: "jag-pr42-001"},
		GatewayToken:   ib.CredentialRef{Issuer: "https://as2.example.com", ID: "access-pr42-001"},
		CallEntryCheck: ib.CallEntryEqual,
	}
}

// lines returns the records written to buf, each decoded.
func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("record %q is not JSON: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func record(t *testing.T, s *auditsink.Sink, rec ib.Record) {
	t.Helper()
	if err := s.Record(context.Background(), rec); err != nil {
		t.Fatalf("Record(%s) = %v", rec.Header().Type, err)
	}
}

// TestEveryRecordTypeHasAProfile fails when the broker gains a record type
// the sink does not write: samples must hold one of each type that
// implements Record in the broker's package.
func TestEveryRecordTypeHasAProfile(t *testing.T) {
	declared := recordTypes(t)
	var sampled []string
	for _, rec := range samples() {
		name := strings.TrimPrefix(fmt.Sprintf("%T", rec), "*identitybroker.")
		sampled = append(sampled, name)
		if _, err := auditsink.Event(rec, origin); err != nil {
			t.Errorf("Event(%s) = %v", name, err)
		}
	}
	slices.Sort(sampled)
	if !slices.Equal(declared, sampled) {
		t.Fatalf("record types %v, samples %v: give the new type a profile and a sample", declared, sampled)
	}
}

// recordTypes returns the types in the broker's package with a record()
// method, which only Record implementations have.
func recordTypes(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "..")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	fset := token.NewFileSet()
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			// Record's marker method takes nothing; the broker's own
			// record method sends one.
			if !ok || fn.Recv == nil || fn.Name.Name != "record" || fn.Type.Params.NumFields() != 0 {
				continue
			}
			typ := fn.Recv.List[0].Type
			if star, ok := typ.(*ast.StarExpr); ok {
				typ = star.X
			}
			names = append(names, typ.(*ast.Ident).Name)
		}
	}
	slices.Sort(names)
	return names
}

// TestJSONAndSlogFieldsMatch pins that each field means the same in the
// AuditEvent's JSON and in the logger's slog output, which name some fields
// differently (implementation.md, "Audit profile checks").
func TestJSONAndSlogFieldsMatch(t *testing.T) {
	for _, rec := range samples() {
		t.Run(rec.Header().Type, func(t *testing.T) {
			ev, err := auditsink.Event(rec, origin)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(ev)
			if err != nil {
				t.Fatal(err)
			}
			var j map[string]any
			if err := json.Unmarshal(raw, &j); err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			record(t, auditsink.New(&buf, origin), rec)
			got := lines(t, &buf)
			if len(got) != 1 {
				t.Fatalf("wrote %d records, want 1", len(got))
			}
			s := got[0]
			if s["level"] != "AUDIT" || s["msg"] != "audit_event" {
				t.Errorf("level %v msg %v, want toolhive-core's AUDIT audit_event", s["level"], s["msg"])
			}
			meta := j["metadata"].(map[string]any)
			pairs := []struct {
				name        string
				json, slogv any
			}{
				{"audit ID", meta["auditId"], s["audit_id"]},
				{"type", j["type"], s["type"]},
				{"logged at", j["loggedAt"], s["logged_at"]},
				{"outcome", j["outcome"], s["outcome"]},
				{"component", j["component"], s["component"]},
				{"source", j["source"], s["source"]},
				{"subjects", j["subjects"], s["subjects"]},
				{"target", j["target"], s["target"]},
				{"extra", meta["extra"], s["metadata"].(map[string]any)["extra"]},
				{"data", j["data"], s["data"]},
			}
			for _, p := range pairs {
				if !jsonEqual(t, p.json, p.slogv) {
					t.Errorf("%s: JSON %v, slog %v", p.name, p.json, p.slogv)
				}
			}
			if _, ok := s["delegation"]; ok {
				t.Error("a broker record carries a delegation chain; only a recorder that validated an act chain fills it")
			}
			extra := meta["extra"].(map[string]any)
			if extra[auditsink.ExtraOccurredAt] != at.Format(time.RFC3339Nano) {
				t.Errorf("occurred_at %v, want %s with its offset", extra[auditsink.ExtraOccurredAt], at.Format(time.RFC3339Nano))
			}
			if j["loggedAt"] != at.UTC().Format(time.RFC3339Nano) {
				t.Errorf("loggedAt %v, want the record's time in UTC", j["loggedAt"])
			}
		})
	}
}

// jsonEqual reports whether a and b are the same JSON value, a null member
// meaning the same as an absent one: the JSON form leaves out an empty
// source extra that slog writes as null.
func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	a, b = dropNulls(a), dropNulls(b)
	ja, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	jb, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(ja, jb)
}

func dropNulls(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := map[string]any{}
	for k, e := range m {
		if e != nil {
			out[k] = dropNulls(e)
		}
	}
	return out
}

// TestResendKeepsAuditIDAndLoggedAt pins that sending a record again writes
// the same AuditID and LoggedAt, so ingestion can remove the duplicate.
func TestResendKeepsAuditIDAndLoggedAt(t *testing.T) {
	var buf bytes.Buffer
	s := auditsink.New(&buf, origin)
	rec := decision()
	record(t, s, rec)
	time.Sleep(2 * time.Millisecond)
	record(t, s, rec)
	got := lines(t, &buf)
	if len(got) != 2 {
		t.Fatalf("wrote %d records, want 2", len(got))
	}
	for _, k := range []string{"audit_id", "logged_at"} {
		if got[0][k] != got[1][k] || got[0][k] == nil {
			t.Errorf("%s: first %v, re-sent %v", k, got[0][k], got[1][k])
		}
	}
	if got[0]["audit_id"] != rec.AuditID {
		t.Errorf("audit_id %v, want the broker's %s", got[0]["audit_id"], rec.AuditID)
	}
}

// TestProvenanceMarks pins the marks of a missing identity, a reported-only
// identity, and an unauthenticated denial, and that an unverified identity
// never appears as user_id.
func TestProvenanceMarks(t *testing.T) {
	missing := decision()
	missing.UserRecord, missing.User, missing.OwnerVerified = "", ib.User{}, false
	denied := &ib.DecisionRecord{
		RecordHeader: ib.RecordHeader{AuditID: "rec_denied", Type: ib.TypeToolDecision, At: at, Outcome: ib.OutcomeDenied, Code: ib.CodeUnauthenticated},
		Workload:     ib.Workload{Name: "claims-to-be-mecak8s"}, Owner: owner, OperationID: "op-1",
	}
	cases := []struct {
		name string
		rec  ib.Record
		want map[string]string
	}{
		{"verified", decision(), map[string]string{
			ib.SubjectUserID: "record:rec_open", ib.SubjectOwnerID: "record:rec_open", ib.SubjectWorkloadSubject: "verified",
			ib.SubjectAgentSPIFFEID: "verified", ib.SubjectAgentInstanceRef: "credential-verified", ib.SubjectRunOwnerID: "reported:mecak8s",
		}},
		{"missing user", missing, map[string]string{
			ib.SubjectUserID: "unavailable", ib.SubjectUserIssuer: "unavailable", ib.SubjectOwnerID: "reported:mecak8s",
		}},
		{"unauthenticated denial", denied, map[string]string{
			ib.SubjectUserID: "unavailable", ib.SubjectOwnerID: "reported:mecak8s",
			ib.SubjectWorkloadSubject: "reported:mecak8s", ib.SubjectWorkloadIssuer: "unavailable",
			ib.SubjectAgentSPIFFEID: "unavailable", ib.SubjectAgentActorID: "unavailable", ib.SubjectAgentInstanceRef: "unavailable",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, err := auditsink.Event(c.rec, origin)
			if err != nil {
				t.Fatal(err)
			}
			prov := ev.Metadata.Extra[auditsink.ExtraProvenance].(map[string]string)
			for k, want := range c.want {
				if prov[k] != want {
					t.Errorf("provenance[%s] = %q, want %q", k, prov[k], want)
				}
			}
			for k := range c.rec.Subjects() {
				if _, ok := prov[k]; !ok {
					t.Errorf("subject %s has no provenance mark", k)
				}
			}
			if prov[ib.SubjectUserID] != "verified" && !strings.HasPrefix(prov[ib.SubjectUserID], "record:") {
				if v, ok := ev.Subjects[ib.SubjectUserID]; ok {
					t.Errorf("unverified user_id %q in Subjects", v)
				}
			}
		})
	}
}

// TestOutcomesOutsideTheTypesSetAreRejected pins audit.md's fixed outcome
// sets: the sink refuses, and writes nothing for, any other outcome.
func TestOutcomesOutsideTheTypesSetAreRejected(t *testing.T) {
	for _, rec := range samples() {
		allowed := []ib.DecisionOutcome{ib.OutcomeSuccess, ib.OutcomeDenied, ib.OutcomeError}
		if rec.Header().Type == ib.TypeSessionClose {
			allowed = []ib.DecisionOutcome{ib.OutcomeSuccess}
		}
		for _, o := range []ib.DecisionOutcome{ib.OutcomeSuccess, ib.OutcomeDenied, ib.OutcomeError, "failure", "unknown", ""} {
			withOutcome(rec, o)
			var buf bytes.Buffer
			err := auditsink.New(&buf, origin).Record(context.Background(), rec)
			if slices.Contains(allowed, o) {
				if err != nil {
					t.Errorf("%s with %q: %v", rec.Header().Type, o, err)
				}
				continue
			}
			if !errors.Is(err, auditsink.ErrNotWritten) || buf.Len() != 0 {
				t.Errorf("%s with %q: err %v, wrote %d bytes; want refused and nothing written", rec.Header().Type, o, err, buf.Len())
			}
		}
	}
}

// withOutcome sets rec's outcome.
func withOutcome(rec ib.Record, o ib.DecisionOutcome) {
	switch r := rec.(type) {
	case *ib.SessionOpenRecord:
		r.Outcome = o
	case *ib.SessionCloseRecord:
		r.Outcome = o
	case *ib.RegistrationRecord:
		r.Outcome = o
	case *ib.DelegationRecord:
		r.Outcome = o
	case *ib.IDJAGRecord:
		r.Outcome = o
	case *ib.DecisionRecord:
		r.Outcome = o
	}
}

// dataKeys are the keys each type's Data may hold, and Target's keys.
var dataKeys = map[string][]string{
	ib.TypeSessionOpen:        {"version", "code", "reason", "session_id", "host_session_id", "incarnation", "new_session", "session_record", "kind", "token_presented", "source_jti", "hard_expiry", "replaced"},
	ib.TypeSessionClose:       {"version", "code", "reason", "session_id", "host_session_id", "closing_reason", "cause", "session_record"},
	ib.TypeInstanceRegistered: {"version", "code", "reason", "session_id", "definition", "new", "moved", "credential_expires_at", "lineage"},
	ib.TypeDelegation:         {"version", "code", "reason", "session_id", "lineage"},
	ib.TypeIDJAG:              {"version", "code", "reason", "session_id", "id_jag", "expires_at", "lineage"},
	ib.TypeToolDecision:       {"version", "code", "reason", "session_id", "rejected_attempt_id", "arguments_sha256", "ceiling_evaluated", "ceiling", "id_jag", "gateway_token", "gateway_token_unreferenced", "call_entry_check", "lineage"},
}

// subjectKeys are the Subjects keys each type may carry, and lineageKeys
// the keys of each type's Data.lineage.
var (
	userKeys        = []string{"owner_iss", "owner_id", "user_iss", "user_id", "user_client_id", "user_client_iss", "workload_iss", "workload_sub"}
	agentKeys       = append(slices.Clone(userKeys), "agent_spiffe_id", "agent_actor_id", "agent_instance_ref")
	runOwnerKeys    = append(slices.Clone(agentKeys), "run_owner_iss", "run_owner_id")
	callLineageKeys = []string{"run", "delegation", "delegation_record", "registration_record"}
	subjectKeys     = map[string][]string{
		ib.TypeSessionOpen: userKeys, ib.TypeSessionClose: userKeys, ib.TypeInstanceRegistered: agentKeys,
		ib.TypeDelegation: runOwnerKeys, ib.TypeIDJAG: runOwnerKeys, ib.TypeToolDecision: runOwnerKeys,
	}
	lineageKeys = map[string][]string{
		ib.TypeInstanceRegistered: {"provenance", "session_record", "host_session_id", "parent_spiffe_id", "parent_host_session_id", "parent_record", "previous_record"},
		ib.TypeDelegation:         {"provenance", "delegation", "delegating", "delegating_run_recorded", "receiving", "parent_delegation_record", "previous_record"},
		ib.TypeIDJAG:              callLineageKeys,
		ib.TypeToolDecision:       callLineageKeys,
	}
)

// TestDataIsVersionedAndAllowlisted pins that Data is JSON naming its
// structure's version, and that Data, Subjects, and Target hold only the
// keys allowed for the type.
func TestDataIsVersionedAndAllowlisted(t *testing.T) {
	for _, rec := range samples() {
		typ := rec.Header().Type
		ev, err := auditsink.Event(rec, origin)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		if err := json.Unmarshal(*ev.Data, &data); err != nil {
			t.Fatalf("%s Data is not JSON: %v", typ, err)
		}
		if data["version"] != typ+"/1" {
			t.Errorf("%s Data version %v", typ, data["version"])
		}
		for k := range data {
			if !slices.Contains(dataKeys[typ], k) {
				t.Errorf("%s Data key %q is not allowlisted", typ, k)
			}
		}
		for k := range ev.Metadata.Extra[auditsink.ExtraProvenance].(map[string]string) {
			if !slices.Contains(subjectKeys[typ], k) {
				t.Errorf("%s Subjects key %q is not allowlisted", typ, k)
			}
		}
		if lineage, ok := data["lineage"].(map[string]any); ok {
			for k := range lineage {
				if !slices.Contains(lineageKeys[typ], k) {
					t.Errorf("%s Data.lineage key %q is not allowlisted", typ, k)
				}
			}
		}
		for k := range ev.Target {
			if typ != ib.TypeToolDecision || !slices.Contains([]string{"type", "name", "method", "server"}, k) {
				t.Errorf("%s Target key %q is not allowlisted", typ, k)
			}
		}
		if ev.Metadata.Extra[auditsink.ExtraProfile] != auditsink.Profile {
			t.Errorf("%s profile %v", typ, ev.Metadata.Extra[auditsink.ExtraProfile])
		}
	}
}

// TestDecisionCarriesItsCorrelationAndLineage pins the decision's operation
// and attempt IDs, its target, and its lineage references.
func TestDecisionCarriesItsCorrelationAndLineage(t *testing.T) {
	ev, err := auditsink.Event(decision(), origin)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Metadata.Extra[auditsink.ExtraOperationID] != "op-pr42-001" || ev.Metadata.Extra[auditsink.ExtraAttemptID] != "att-1" {
		t.Errorf("correlation %v", ev.Metadata.Extra)
	}
	want := map[string]string{"type": "tool", "name": "pull_request_read", "method": "tools/call", "server": "github"}
	if !maps.Equal(ev.Target, want) {
		t.Errorf("target %v, want %v", ev.Target, want)
	}
	var data struct {
		Lineage map[string]string `json:"lineage"`
		Token   map[string]string `json:"gateway_token"`
	}
	if err := json.Unmarshal(*ev.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Lineage["run"] != "R42" || data.Lineage["delegation_record"] != "rec_del" || data.Lineage["registration_record"] != "rec_reg" {
		t.Errorf("lineage %v", data.Lineage)
	}
	if data.Token["iss"] != "https://as2.example.com" || data.Token["jti"] != "access-pr42-001" {
		t.Errorf("gateway token reference %v", data.Token)
	}
}

// TestRegistrationRecordsItsLineage pins the registration lineage event:
// the instance's ag_ identifier and SPIFFE ID, and its creation parent's
// registration AuditID, marked as reported by mecak8s.
func TestRegistrationRecordsItsLineage(t *testing.T) {
	ev, err := auditsink.Event(samples()[2], origin)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "mecatl_instance_registered" || ev.Subjects["agent_actor_id"] != "ag_7hq2x9" || ev.Subjects["agent_spiffe_id"] != agent.Subject {
		t.Errorf("type %s subjects %v", ev.Type, ev.Subjects)
	}
	var data struct {
		Lineage map[string]any `json:"lineage"`
	}
	if err := json.Unmarshal(*ev.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Lineage["parent_record"] != "rec_reg_main" || data.Lineage["parent_spiffe_id"] != parent.Subject || data.Lineage["provenance"] != "reported:mecak8s" {
		t.Errorf("lineage %v", data.Lineage)
	}
}

// TestSupersedingRefusal pins that a refusal following an earlier record
// names it, with its own outcome, code, and reason.
func TestSupersedingRefusal(t *testing.T) {
	rec := decision()
	rec.AuditID, rec.Supersedes = "rec_refusal", "rec_decision"
	rec.Outcome, rec.Code, rec.Reason = ib.OutcomeError, ib.CodeUnavailable, ib.ReasonRecordNotAccepted
	ev, err := auditsink.Event(rec, origin)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Metadata.Extra[auditsink.ExtraSupersedes] != "rec_decision" || ev.Outcome != audit.OutcomeError {
		t.Errorf("extra %v outcome %s", ev.Metadata.Extra, ev.Outcome)
	}
	var data map[string]any
	_ = json.Unmarshal(*ev.Data, &data)
	if data["code"] != "UNAVAILABLE" || data["reason"] != "record_not_accepted" {
		t.Errorf("data %v", data)
	}
}

// failingWriter fails every write.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

// shortWriter accepts only part of each write, without an error.
type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

// syncingWriter records writes and fails Sync with err.
type syncingWriter struct {
	bytes.Buffer
	syncs int
	err   error
}

func (w *syncingWriter) Sync() error { w.syncs++; return w.err }

// TestAFailedWriteIsReported pins that the sink reports a destination's
// failure, which toolhive-core's logger drops: a write error, a short write,
// and a failed sync.
func TestAFailedWriteIsReported(t *testing.T) {
	boom := errors.New("disk full")
	for name, w := range map[string]io.Writer{
		"write error": failingWriter{err: boom},
		"short write": shortWriter{},
		"sync error":  &syncingWriter{err: boom},
	} {
		t.Run(name, func(t *testing.T) {
			err := auditsink.New(w, origin).Record(context.Background(), decision())
			if !errors.Is(err, auditsink.ErrNotWritten) {
				t.Fatalf("Record = %v, want ErrNotWritten", err)
			}
		})
	}
	w := &syncingWriter{}
	record(t, auditsink.New(w, origin), decision())
	if w.syncs != 1 || w.Len() == 0 {
		t.Errorf("syncs %d, wrote %d bytes; want the record written and synced once", w.syncs, w.Len())
	}
	// A failure does not stick: the next record is written.
	flaky := &flakyWriter{failures: 1}
	s := auditsink.New(flaky, origin)
	if err := s.Record(context.Background(), decision()); err == nil {
		t.Fatal("first write succeeded")
	}
	record(t, s, decision())
}

// flakyWriter fails its first failures writes.
type flakyWriter struct {
	failures int
	bytes.Buffer
}

func (w *flakyWriter) Write(p []byte) (int, error) {
	if w.failures > 0 {
		w.failures--
		return 0, errors.New("collector restarting")
	}
	return w.Buffer.Write(p)
}

// blockingWriter blocks every write until release is closed.
type blockingWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	buf     bytes.Buffer
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// TestAStuckDestinationFailsWithinTheDeadline pins that a record the
// destination does not accept in time is reported as not written, and that
// a record waiting behind it fails within its own deadline, so a lossless
// sink that stops accepting blocks the broker's events instead of dropping
// records. The stuck write completes later, as an unconfirmed record.
func TestAStuckDestinationFailsWithinTheDeadline(t *testing.T) {
	w := &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
	s := auditsink.New(w, origin)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.Record(ctx, decision()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stuck write: %v, want the deadline", err)
	}
	<-w.started
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if err := s.Record(ctx2, decision()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("record behind a stuck write: %v, want the deadline", err)
	}
	close(w.release)
	record(t, s, decision())
	w.mu.Lock()
	defer w.mu.Unlock()
	if n := strings.Count(w.buf.String(), "\n"); n != 2 {
		t.Errorf("destination holds %d records, want the unconfirmed one and the last", n)
	}
}

// TestConcurrentRecordsAreWholeLines pins that concurrent records never
// interleave: each is one JSON line.
func TestConcurrentRecordsAreWholeLines(t *testing.T) {
	var buf bytes.Buffer
	s := auditsink.New(&buf, origin)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() { record(t, s, decision()) })
	}
	wg.Wait()
	if got := lines(t, &buf); len(got) != 32 {
		t.Fatalf("wrote %d records, want 32", len(got))
	}
}

// TestAToolListingTargetsItsServer pins that a tools/list decision targets
// the server it lists, not a tool.
func TestAToolListingTargetsItsServer(t *testing.T) {
	rec := decision()
	rec.Method, rec.Tool = ib.MethodToolsList, ""
	ev, err := auditsink.Event(rec, origin)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"type": "server", "method": "tools/list", "server": "github"}
	if !maps.Equal(ev.Target, want) {
		t.Fatalf("target %v, want %v", ev.Target, want)
	}
}

// TestAnEndedContextWritesNothing pins that a record whose context already
// ended is reported as not written and is not written.
func TestAnEndedContextWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	s := auditsink.New(&buf, origin)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 20 {
		if err := s.Record(ctx, decision()); !errors.Is(err, context.Canceled) {
			t.Fatalf("Record = %v, want the cancellation", err)
		}
	}
	if buf.Len() != 0 {
		t.Fatalf("wrote %d bytes for records whose context had ended", buf.Len())
	}
}
