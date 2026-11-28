package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
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

// gatedWriter is a test audit destination that can fail records of one type
// or hold every write until released.
type gatedWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	failType string
	// gate, when set, holds each write until it is closed; held reports
	// that a write is waiting on it.
	gate chan struct{}
	held chan struct{}
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	gate, held, failType := w.gate, w.held, w.failType
	w.mu.Unlock()
	if gate != nil {
		select {
		case held <- struct{}{}:
		default:
		}
		<-gate
	}
	if failType != "" && bytes.Contains(p, []byte(`"type":"`+failType+`"`)) {
		return 0, errors.New("collector unavailable")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *gatedWriter) set(failType string, gate chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failType, w.gate, w.held = failType, gate, make(chan struct{}, 1)
}

func (w *gatedWriter) contains(s string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return bytes.Contains(w.buf.Bytes(), []byte(s))
}

// TestNoGatewayTokenUntilTheDecisionIsWritten pins the write order through
// the real toolhive-core logger: a decision record the destination fails
// returns no gateway token, and a destination that stalls, as a lossless
// one does when its collector is unavailable, holds the token, and so the
// dispatch, until it accepts the decision.
func TestNoGatewayTokenUntilTheDecisionIsWritten(t *testing.T) {
	dest := &gatedWriter{}
	g := buildWithRealAS1(t, true, embeddedEntry(gatewayTestServer), func(cfg *Config) { cfg.identityBrokerAuditWriter = dest })
	s, cred, _ := g.open(t)

	dest.set(identitybroker.TypeToolDecision, nil)
	auth, err := g.built.IdentityBroker.AuthorizeCall(t.Context(), g.call(s, cred))
	wantBrokerError(t, err, identitybroker.CodeUnavailable, identitybroker.ReasonRecordNotAccepted)
	if auth.GatewayToken.Value() != "" {
		t.Fatal("a call whose decision was not written got a gateway token")
	}

	gate := make(chan struct{})
	dest.set("", gate)
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
	<-dest.held
	select {
	case r := <-done:
		t.Fatalf("AuthorizeCall returned %v while the destination held its records", r.err)
	case <-time.After(200 * time.Millisecond):
	}
	close(gate)
	r := <-done
	if r.err != nil || r.auth.GatewayToken.Value() == "" {
		t.Fatalf("AuthorizeCall after the destination accepted = %v, want a gateway token", r.err)
	}
	if !dest.contains(`"mecatl.attempt_id":"`+r.auth.AttemptID+`"`) || !dest.contains(`"type":"`+identitybroker.TypeToolDecision+`"`) {
		t.Fatal("the gateway token was returned without its decision written")
	}
}
