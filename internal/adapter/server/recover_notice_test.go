package server_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// permanentTestError implements port.PermanentError for test use.
type permanentTestError struct{ msg string }

func (e *permanentTestError) Error() string { return e.msg }
func (*permanentTestError) Permanent() bool { return true }

var _ port.PermanentError = (*permanentTestError)(nil)

// TestRecoverNoticeEmittedOnPermanentFailure asserts that when a session's last
// run failed on a PERMANENT provider error:
//   - loadAndReopen stores a recover notice;
//   - RecoverNotice returns the notice after loadAndReopen (consumed by the relay);
//   - the run still launches (Recover unblocked);
//   - a follow-up prompt emits NO repeat notice.
func TestRecoverNoticeEmittedOnPermanentFailure(t *testing.T) {
	permErr := &permanentTestError{msg: "invalid_encrypted_content"}
	llm := mockllm.New(
		mockllm.ErrorTurn(permErr),           // turn 1: permanent failure
		mockllm.TextTurn("recovered answer"), // turn 2: succeeds after recovery
		mockllm.TextTurn("second prompt ok"), // turn 3: follow-up prompt
	)
	svc := newService(t, llm, allowRules())

	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{MaxTurns: 1})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// ---- Run 1: permanent failure ----
	run1, err := svc.StartRunContent(context.Background(), sess.ID, "do the thing", nil)
	if err != nil {
		t.Fatalf("StartRunContent #1: %v", err)
	}
	var stop1 session.StopReason
	for ev := range run1.Events() {
		if ev.Type == session.EvResult && ev.Result != nil {
			stop1 = ev.Result.Stop
			if !ev.Result.Permanent {
				t.Fatal("ResultPayload.Permanent = false after permanent provider rejection")
			}
		}
	}
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run1)

	if stop1 != session.StopError {
		t.Fatalf("Run #1 stop = %q, want StopError", stop1)
	}

	// Verify the session is durably in StateFailed with permanence.
	loaded, _ := svc.GetSession(context.Background(), sess.ID)
	if loaded.State != session.StateFailed {
		t.Fatalf("persisted state = %q, want StateFailed", loaded.State)
	}
	if !loaded.FailurePermanence() {
		t.Fatal("FailurePermanence() = false after permanent-error run")
	}

	// ---- Run 2: re-entry, notice is stored by loadAndReopen ----
	// StartRunContent calls loadAndReopen which detects the permanent failure
	// and stores the notice. After StartRunContent returns, RecoverNotice()
	// consumes it — exactly what the relay (grpc/http) does to inject the
	// EvRecoverNotice event before the main event loop.
	run2, err := svc.StartRunContent(context.Background(), sess.ID, "retry", nil)
	if err != nil {
		t.Fatalf("StartRunContent #2 (permanent-failure re-entry): %v", err)
	}

	// The notice must be available now (stored by loadAndReopen during StartRunContent).
	notice := svc.RecoverNotice(sess.ID)
	if notice == "" {
		t.Fatal("RecoverNotice returned empty after loadAndReopen of permanently-failed session")
	}

	// The run still launches (Recover unblocked).
	var sawTurnStart bool
	for ev := range run2.Events() {
		if ev.Type == session.EvTurnStart {
			sawTurnStart = true
		}
	}
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run2)

	if !sawTurnStart {
		t.Fatal("Recover blocked the run — Recover must stay honest (retry POSSIBLE)")
	}

	// ---- Run 3: follow-up prompt, no repeat notice ----
	run3, err := svc.StartRunContent(context.Background(), sess.ID, "one more", nil)
	if err != nil {
		t.Fatalf("StartRunContent #3 (follow-up): %v", err)
	}
	// Notice must have been consumed by the previous call.
	if notice2 := svc.RecoverNotice(sess.ID); notice2 != "" {
		t.Fatal("RecoverNotice returned non-empty on follow-up — must be once-per-recovery")
	}
	for range run3.Events() {
	}
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run3)
}

// TestNoRecoverNoticeOnTransientFailure asserts that a session that failed on a
// TRANSIENT error does NOT produce a recover notice.
func TestNoRecoverNoticeOnTransientFailure(t *testing.T) {
	llm := mockllm.New(
		mockllm.ErrorTurn(fmt.Errorf("upstream 503: service unavailable")), // transient
		mockllm.TextTurn("recovered"),
	)
	svc := newService(t, llm, allowRules())

	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{MaxTurns: 1})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Run 1: transient failure.
	run1, err := svc.StartRunContent(context.Background(), sess.ID, "do the thing", nil)
	if err != nil {
		t.Fatalf("StartRunContent #1: %v", err)
	}
	for range run1.Events() {
	}
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run1)

	loaded, _ := svc.GetSession(context.Background(), sess.ID)
	if loaded.State != session.StateFailed {
		t.Fatalf("persisted state = %q, want StateFailed", loaded.State)
	}
	if loaded.FailurePermanence() {
		t.Fatal("FailurePermanence() = true for transient error, want false")
	}

	// RecoverNotice before StartRunContent — not yet stored (loadAndReopen hasn't run).
	if notice := svc.RecoverNotice(sess.ID); notice != "" {
		t.Fatal("RecoverNotice returned non-empty before re-entry")
	}

	// StartRunContent for re-entry.
	run2, err := svc.StartRunContent(context.Background(), sess.ID, "retry", nil)
	if err != nil {
		t.Fatalf("StartRunContent #2: %v", err)
	}
	// After loadAndReopen, there should be NO notice (transient failure).
	if notice := svc.RecoverNotice(sess.ID); notice != "" {
		t.Fatal("RecoverNotice returned non-empty on transient-failure re-entry")
	}
	for range run2.Events() {
	}
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run2)
}

// TestRecoverNoticeCrossesGRPCWire asserts that the recover_notice event is
// visible on the gRPC Converse wire as the FIRST event from the relay when
// re-entering a permanently-failed session.
func TestRecoverNoticeCrossesGRPCWire(t *testing.T) {
	permErr := &permanentTestError{msg: "invalid_encrypted_content"}
	llm := mockllm.New(
		mockllm.ErrorTurn(permErr),           // turn 1: permanent failure
		mockllm.TextTurn("recovered answer"), // turn 2: succeeds after recovery
	)
	svc := newService(t, llm, allowRules())

	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{MaxTurns: 1})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Run 1: permanent failure via StartRunContent, then persist.
	run1, err := svc.StartRunContent(context.Background(), sess.ID, "do the thing", nil)
	if err != nil {
		t.Fatalf("StartRunContent #1: %v", err)
	}
	for range run1.Events() {
	}
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run1)

	// Now test the gRPC wire path.
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run 2 via Converse — the relay should call RecoverNotice and emit the event.
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(sess.ID), Text: "retry"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}
	_ = stream.CloseSend()
	events := recvAll(t, stream)

	var sawRecoverNotice bool
	var firstEventAfterInit string
	for _, ev := range events {
		if ev.GetType() == "recover_notice" {
			sawRecoverNotice = true
			if ev.GetText() == "" {
				t.Error("EvRecoverNotice has empty text on the wire")
			}
		}
		// Track the first non-init event type after session.init.
		if firstEventAfterInit == "" && ev.GetType() != "session.init" {
			firstEventAfterInit = ev.GetType()
		}
	}
	if !sawRecoverNotice {
		t.Fatalf("EvRecoverNotice not emitted on the gRPC wire after permanent-failure recovery, events: %v", typesOf(events))
	}
	// The recover_notice MUST be the FIRST non-init event from the relay — it is
	// injected before the main event loop drains, so any other ordering is a
	// regression of the injection contract.
	if firstEventAfterInit != "recover_notice" {
		t.Fatalf("first non-init event = %q, want %q (recover_notice must lead the stream); events: %v",
			firstEventAfterInit, "recover_notice", typesOf(events))
	}
}

// TestRecoverNoticeCrossesHTTPWire asserts the recover_notice event is visible on
// the HTTP/SSE wire as the FIRST data frame from relayRunSSE when re-entering a
// permanently-failed session (mirroring the gRPC Converse test above — the two
// relays have SEPARATE notice-injection paths, so both must be covered).
func TestRecoverNoticeCrossesHTTPWire(t *testing.T) {
	permErr := &permanentTestError{msg: "invalid_encrypted_content"}
	llm := mockllm.New(
		mockllm.ErrorTurn(permErr),           // turn 1: permanent failure
		mockllm.TextTurn("recovered answer"), // turn 2: succeeds after recovery
	)
	svc := newService(t, llm, allowRules())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	id := createHTTPSession(t, srv)

	// Run 1: permanent failure via the Service API (the HTTP relay deregisters
	// the run on response close, which would make a subsequent svc.Persist a
	// no-op; the Service API keeps the run registered so Persist lands the
	// StateFailed+permanence snapshot). Persist + FinishRun, mirroring the gRPC
	// test's setup.
	run1, err := svc.StartRunContent(context.Background(), session.SessionID(id), "do the thing", nil)
	if err != nil {
		t.Fatalf("StartRunContent #1: %v", err)
	}
	for range run1.Events() {
	}
	svc.Persist(context.Background(), session.SessionID(id))
	svc.FinishRun(session.SessionID(id), run1)

	// Run 2 via HTTP prompt — relayRunSSE should inject the EvRecoverNotice as the
	// FIRST data frame (before the main event loop drains).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		srv.URL+"/v1/sessions/"+id+"/prompt", strings.NewReader(`{"text":"retry"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST prompt #2: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	events := parseSSE(t, bufio.NewReader(resp.Body))

	var sawRecoverNotice bool
	var firstEvent string
	for _, ev := range events {
		if ev.GetType() == "recover_notice" {
			sawRecoverNotice = true
			if ev.GetText() == "" {
				t.Error("EvRecoverNotice has empty text on the HTTP wire")
			}
		}
		if firstEvent == "" {
			firstEvent = ev.GetType()
		}
	}
	if !sawRecoverNotice {
		t.Fatalf("EvRecoverNotice not emitted on the HTTP wire after permanent-failure recovery, events: %v", typesOf(events))
	}
	if firstEvent != "recover_notice" {
		t.Fatalf("first HTTP SSE data frame = %q, want recover_notice; events: %v",
			firstEvent, typesOf(events))
	}
}

// TestRecoverNoticeSweptOnCloseSession asserts CloseSession clears any stored
// recover-notice for the session (a session closed without re-entry after a
// permanent failure would otherwise leak one entry in the recoverNotices map).
func TestRecoverNoticeSweptOnCloseSession(t *testing.T) {
	permErr := &permanentTestError{msg: "invalid_encrypted_content"}
	llm := mockllm.New(
		mockllm.ErrorTurn(permErr),
		mockllm.TextTurn("recovered"),
	)
	svc := newService(t, llm, allowRules())

	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{MaxTurns: 1})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Run 1: permanent failure.
	run1, err := svc.StartRunContent(context.Background(), sess.ID, "do the thing", nil)
	if err != nil {
		t.Fatalf("StartRunContent #1: %v", err)
	}
	for range run1.Events() {
	}
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run1)

	// Re-entry stores the notice.
	run2, err := svc.StartRunContent(context.Background(), sess.ID, "retry", nil)
	if err != nil {
		t.Fatalf("StartRunContent #2: %v", err)
	}
	if notice := svc.RecoverNotice(sess.ID); notice == "" {
		t.Fatal("precondition: RecoverNotice empty after loadAndReopen of permanent-failure re-entry")
	}
	for range run2.Events() {
	}
	svc.Persist(context.Background(), sess.ID)
	svc.FinishRun(sess.ID, run2)

	// Close the session without further re-entry — CloseSession must sweep the
	// stored notice so it does not leak.
	svc.CloseSession(sess.ID)
	if notice := svc.RecoverNotice(sess.ID); notice != "" {
		t.Fatalf("RecoverNotice returned %q after CloseSession, want empty (map entry must be swept)", notice)
	}
}
