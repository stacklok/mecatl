package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/auditsink"
)

// auditEvent is one audit event as the toolhive-core logger wrote it.
type auditEvent map[string]any

// extra returns the event's Metadata.Extra value for key, as text.
func (e auditEvent) extra(key string) string {
	meta, _ := e["metadata"].(map[string]any)
	extra, _ := meta["extra"].(map[string]any)
	s, _ := extra[key].(string)
	return s
}

// str returns the event's top-level text field key.
func (e auditEvent) str(key string) string {
	s, _ := e[key].(string)
	return s
}

// subject returns the event's subject key.
func (e auditEvent) subject(key string) string {
	subjects, _ := e["subjects"].(map[string]any)
	s, _ := subjects[key].(string)
	return s
}

// data returns the event's Data.
func (e auditEvent) data() map[string]any {
	d, _ := e["data"].(map[string]any)
	return d
}

// readAudit returns the events in the audit file path of type typ, or of
// every type when typ is empty, oldest first.
func readAudit(t *testing.T, path, typ string) []auditEvent {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the audit file: %v", err)
	}
	var out []auditEvent
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var ev auditEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("audit line %q is not JSON: %v", sc.Text(), err)
		}
		if typ == "" || ev.str("type") == typ {
			out = append(out, ev)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestIdentityBrokerRecordsReachTheAuditFile pins that startup gives the
// identity broker the toolhive-core audit sink: its records reach the audit
// file as profile events, and no diagnostic repeats them.
func TestIdentityBrokerRecordsReachTheAuditFile(t *testing.T) {
	diag := &recordingDiag{}
	g := buildWithRealAS1(t, false, nil, func(cfg *Config) { cfg.Diagnostics = diag })
	s, _, _ := g.open(t)
	opens := readAudit(t, g.auditFile, identitybroker.TypeSessionOpen)
	if len(opens) != 1 {
		t.Fatalf("session open events = %d, want 1", len(opens))
	}
	open := opens[0]
	if open.str("component") != auditsink.Component || open.str("outcome") != "success" || open.str("level") != "AUDIT" ||
		open.extra(auditsink.ExtraProfile) != auditsink.Profile || open.data()["session_id"] != s.ID {
		t.Fatalf("session open event = %v", open)
	}
	msgs, _ := diag.snapshot()
	for _, m := range msgs {
		if m == "identity broker record" {
			t.Fatal("a diagnostic repeats a broker record")
		}
	}
}

// TestShutdownSendsTheIdentityBrokersQueuedRecords pins that Built.Close
// drains the identity broker's queued close records into its sink, so none
// queued at shutdown is lost unreported, and an end-to-end test can read them
// after Close.
func TestShutdownSendsTheIdentityBrokersQueuedRecords(t *testing.T) {
	g := buildWithRealAS1(t, false, nil, nil)
	s, _, _ := g.open(t)
	if err := g.built.IdentityBroker.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: s.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	g.built.Close()
	closes := readAudit(t, g.auditFile, identitybroker.TypeSessionClose)
	if len(closes) != 1 || closes[0].data()["session_id"] != s.ID {
		t.Fatalf("close events after shutdown = %v, want the close of %s", closes, s.ID)
	}
}

// gatedWriter is a test audit destination that fails the success records
// of one type, or holds the records of one type until a gate opens.
type gatedWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	failType string
	holdType string
	// gate holds each write of holdType until it is closed; held receives
	// each such write when it starts waiting.
	gate chan struct{}
	held chan []byte
}

func typeIs(p []byte, typ string) bool {
	return typ != "" && bytes.Contains(p, []byte(`"type":"`+typ+`"`))
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	gate, held, failType, holdType := w.gate, w.held, w.failType, w.holdType
	w.mu.Unlock()
	if typeIs(p, holdType) {
		held <- bytes.Clone(p)
		<-gate
	}
	// Only successes fail, so the refusal superseding one is written.
	if typeIs(p, failType) && bytes.Contains(p, []byte(`"outcome":"success"`)) {
		return 0, errors.New("collector unavailable")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *gatedWriter) set(failType, holdType string, gate chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failType, w.holdType, w.gate, w.held = failType, holdType, gate, make(chan []byte, 8)
}

// events returns the events written so far.
func (w *gatedWriter) events(t *testing.T) []auditEvent {
	t.Helper()
	w.mu.Lock()
	raw := bytes.Clone(w.buf.Bytes())
	w.mu.Unlock()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return readAudit(t, path, "")
}

// TestNoGatewayTokenUntilTheDecisionIsWritten pins the write order through
// the real toolhive-core logger: a decision record the destination fails
// returns no gateway token, and a destination that stalls on the decision,
// as a lossless one does when its collector is unavailable, holds the token,
// and so the dispatch, until it accepts that decision.
func TestNoGatewayTokenUntilTheDecisionIsWritten(t *testing.T) {
	dest := &gatedWriter{}
	g := buildWithRealAS1(t, true, embeddedEntry(gatewayTestServer), func(cfg *Config) { cfg.identityBrokerAuditWriter = dest })
	s, cred, _ := g.open(t)

	dest.set(identitybroker.TypeToolDecision, "", nil)
	auth, err := g.built.IdentityBroker.AuthorizeCall(t.Context(), g.call(s, cred))
	wantBrokerError(t, err, identitybroker.CodeUnavailable, identitybroker.ReasonRecordNotAccepted)
	if auth.GatewayToken.Value() != "" {
		t.Fatal("a call whose decision was not written got a gateway token")
	}
	// The refusal superseding the decision is queued; wait for it, so the
	// held phase below holds only the next call's decision.
	deadline := time.Now().Add(10 * time.Second)
	for !slices.ContainsFunc(dest.events(t), func(ev auditEvent) bool {
		return ev.str("type") == identitybroker.TypeToolDecision && ev.extra(auditsink.ExtraSupersedes) != ""
	}) {
		if time.Now().After(deadline) {
			t.Fatal("the refusal superseding the unwritten decision was never written")
		}
		time.Sleep(5 * time.Millisecond)
	}

	gate := make(chan struct{})
	dest.set("", identitybroker.TypeToolDecision, gate)
	type result struct {
		auth identitybroker.Authorization
		err  error
	}
	done := make(chan result, 1)
	call := g.call(s, cred)
	call.OperationID = "op-held"
	go func() {
		a, err := g.built.IdentityBroker.AuthorizeCall(t.Context(), call)
		done <- result{a, err}
	}()
	held := <-dest.held
	if !bytes.Contains(held, []byte(`"mecatl.operation_id":"op-held"`)) || !bytes.Contains(held, []byte(`"outcome":"success"`)) {
		t.Fatalf("held write %s, want the allowing decision of op-held", held)
	}
	select {
	case r := <-done:
		t.Fatalf("AuthorizeCall returned %v while the destination held its decision", r.err)
	case <-time.After(200 * time.Millisecond):
	}
	close(gate)
	r := <-done
	if r.err != nil || r.auth.GatewayToken.Value() == "" {
		t.Fatalf("AuthorizeCall after the destination accepted = %v, want a gateway token", r.err)
	}
	var written bool
	for _, ev := range dest.events(t) {
		if ev.str("type") == identitybroker.TypeToolDecision && ev.str("outcome") == "success" &&
			ev.extra(auditsink.ExtraAttemptID) == r.auth.AttemptID && ev.extra(auditsink.ExtraOperationID) == "op-held" {
			written = true
		}
	}
	if !written {
		t.Fatal("the gateway token was returned without its decision written")
	}
}

// TestIdentityBrokerAuditDestinations pins the audit destination: standard
// output by default, where a record arrives without a sync, which fails on a
// pipe; an absolute file path; and a relative one refused. It is not
// parallel: it replaces os.Stdout.
func TestIdentityBrokerAuditDestinations(t *testing.T) {
	config := identitybroker.Config{TrustDomain: "agents.example.com", Audience: "https://broker.example.com"}
	rec := &identitybroker.DecisionRecord{
		RecordHeader: identitybroker.RecordHeader{AuditID: "rec_stdout", Type: identitybroker.TypeToolDecision, At: time.Now(), Outcome: identitybroker.OutcomeDenied},
		Owner:        identitybroker.Owner{Issuer: "https://idp.example.com", Subject: "alice-204"},
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	sink, closeAudit, err := openIdentityBrokerAudit(Config{}, config)
	if err == nil {
		err = sink.Record(t.Context(), rec)
		closeAudit()
	}
	os.Stdout = stdout
	_ = w.Close()
	if err != nil {
		t.Fatalf("record to standard output: %v", err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"audit_id":"rec_stdout"`)) || !bytes.Contains(out.Bytes(), []byte(`"mecatl.deployment":"https://broker.example.com"`)) {
		t.Fatalf("standard output = %q, want the record", out.String())
	}

	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink, closeAudit, err = openIdentityBrokerAudit(Config{IdentityBrokerAuditFile: path}, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Record(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	closeAudit()
	if got := readAudit(t, path, identitybroker.TypeToolDecision); len(got) != 1 || got[0].str("audit_id") != "rec_stdout" {
		t.Fatalf("audit file = %v, want the record", got)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("audit file mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}

	if _, _, err := openIdentityBrokerAudit(Config{IdentityBrokerAuditFile: "audit.jsonl"}, config); !errors.Is(err, errIdentityBrokerAuditFileNotAbsolute) {
		t.Fatalf("relative audit file = %v, want refused", err)
	}
}
