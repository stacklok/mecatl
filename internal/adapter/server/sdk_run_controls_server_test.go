package server_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

func TestSDKRunControls_Scenario1_TransportRouteParity(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	grpcCalls := []func() error{
		func() error {
			_, err := client.ResolveRunAsk(t.Context(), &mecatlv1.ResolveRunAskRequest{})
			return err
		},
		func() error { _, err := client.CancelRun(t.Context(), &mecatlv1.CancelRunRequest{}); return err },
		func() error { _, err := client.SteerRun(t.Context(), &mecatlv1.SteerRunRequest{}); return err },
		func() error {
			_, err := client.CancelRunSteer(t.Context(), &mecatlv1.CancelRunSteerRequest{})
			return err
		},
	}
	for i, call := range grpcCalls {
		if code := status.Code(call()); code != codes.InvalidArgument {
			t.Errorf("gRPC strict control %d code = %v, want InvalidArgument", i, code)
		}
	}

	h := server.NewHTTPHandler(svc)
	for _, path := range []string{"resolve-ask", "cancel", "steer", "cancel-steer"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/sessions/s/controls/"+path, strings.NewReader(`{}`))
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST /controls/%s status = %d, want 400 (registered strict route)", path, rec.Code)
		}
	}
}

func TestSDKRunControls_Scenario1_FeatureRegistryParity(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	features := svc.CompatibilityInfo(t.Context()).GetFeatures()
	if !containsString(features, server.FeaturePromptFreeControls) {
		t.Fatalf("compatibility features = %v, want %q", features, server.FeaturePromptFreeControls)
	}

	root := repoRootFromTest(t)
	typed, err := os.ReadFile(root + "/sdk/typescript/src/server.ts")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(typed, []byte(`PromptFreeControls: "prompt_free_controls"`)) {
		t.Fatal("TypeScript ServerFeature is missing prompt_free_controls")
	}
}

func TestSDKRunControls_Scenario1_RehydratedResolveIsBounded(t *testing.T) {
	dir := t.TempDir()
	store1, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	var before atomic.Int64
	svc1 := newAskingService(t, store1, permstore.New(), &before,
		mockllm.New(mockllm.ToolCallTurn(call("w1", "Write", `{"path":"a.go"}`))), false)
	sess, err := svc1.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	askID := driveServiceToAwaiting(t, svc1, sess.ID)
	parked, err := svc1.GetSession(t.Context(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	runID := parked.RunID()

	store2, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	var after atomic.Int64
	svc2 := newAskingService(t, store2, permstore.New(), &after, mockllm.New(mockllm.TextTurn("done")), true)
	client, cleanup := dialGRPC(t, svc2)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ack, err := client.ResolveRunAsk(ctx, &mecatlv1.ResolveRunAskRequest{
		SessionId: string(sess.ID), ExpectedRunId: runID, AskId: askID,
		Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE,
	})
	if err != nil {
		t.Fatalf("ResolveRunAsk: %v", err)
	}
	if ack.GetRunId() != runID || ack.GetAskId() != askID {
		t.Fatalf("ack = %+v, want exact run/ask correlation", ack)
	}
	waitSessionState(t, svc2, sess.ID, session.StateCompleted)
	if got := after.Load(); got != 1 {
		t.Fatalf("rehydrated tool executions = %d, want exactly 1", got)
	}
}

func TestSDKRunControls_Scenario1_RehydratedResolveTransfersContextOwnership(t *testing.T) {
	// This repeats the real restart seam with an immediately-cancelled caller: the
	// accepted run must finish under Service ownership after the unary ack returns.
	dir := t.TempDir()
	store1, _ := jsonlstore.New(dir)
	var before atomic.Int64
	svc1 := newAskingService(t, store1, permstore.New(), &before,
		mockllm.New(mockllm.ToolCallTurn(call("w1", "Write", `{"path":"a.go"}`))), false)
	sess, err := svc1.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	askID := driveServiceToAwaiting(t, svc1, sess.ID)
	parked, err := svc1.GetSession(t.Context(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	runID := parked.RunID()
	store2, _ := jsonlstore.New(dir)
	var after atomic.Int64
	svc2 := newAskingService(t, store2, permstore.New(), &after, mockllm.New(mockllm.TextTurn("done")), true)
	client, cleanup := dialGRPC(t, svc2)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	ack, err := client.ResolveRunAsk(ctx, &mecatlv1.ResolveRunAskRequest{SessionId: string(sess.ID), ExpectedRunId: runID, AskId: askID, Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE})
	if err != nil || ack.GetRunId() != runID {
		t.Fatalf("ResolveRunAsk = (%+v, %v)", ack, err)
	}
	cancel()
	waitSessionState(t, svc2, sess.ID, session.StateCompleted)
	if after.Load() != 1 {
		t.Fatalf("tool executions after caller cancellation = %d, want 1", after.Load())
	}
}

func TestSDKRunControls_Scenario1_StrictHTTPDecode(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	h := server.NewHTTPHandler(svc)
	tests := []struct{ path, body string }{
		{"resolve-ask", `{}`},
		{"resolve-ask", `{"expected_run_id":"run","ask_id":"ask","verdict":"ALLOW_ONCE"}`},
		{"resolve-ask", `{"expected_run_id":"run","ask_id":"ask","verdict":1}`},
		{"cancel", `{"expected_run_id":"run","session_id":"ambiguous"}`},
		{"cancel", `{"expected_run_id":"run"} {}`},
		{"steer", `{"expected_run_id":"run","parts":[{"kind":"video","mime_type":"video/mp4","data":"AA=="}]}`},
		{"cancel-steer", `{"expected_run_id":"run","message_id":"` + strings.Repeat("x", 65) + `"}`},
	}
	for _, tc := range tests {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/sessions/session/controls/"+tc.path, strings.NewReader(tc.body))
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s: status = %d, want 400; body=%s", tc.path, tc.body, rec.Code, rec.Body.String())
		}
	}
}

func TestSDKRunControls_Scenario3_ResolveOrdinaryAsk(t *testing.T) {
	store := jsonlstoreMust(t)
	var ran atomic.Int64
	secondRequestEntered := make(chan struct{})
	releaseSecondRequest := make(chan struct{})
	var requestCount atomic.Int64
	release := sync.OnceFunc(func() { close(releaseSecondRequest) })
	t.Cleanup(release)
	svc := newAskingService(t, store, permstore.New(), &ran,
		mockllm.NewWith([]mockllm.Option{
			mockllm.WithRequestObserver(func(port.LLMRequest) {
				if requestCount.Add(1) != 2 {
					return
				}
				close(secondRequestEntered)
				<-releaseSecondRequest
			}),
		}, mockllm.ToolCallTurn(call("w1", "Write", `{"path":"a.go"}`)), mockllm.TextTurn("done")), true)
	sess, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.StartRun(t.Context(), sess.ID, "go")
	if err != nil {
		t.Fatal(err)
	}
	for ev := range run.Events() {
		if ev.Type != session.EvPermissionAsk || ev.Ask == nil {
			continue
		}
		ack, resolveErr := svc.ResolveRunAsk(t.Context(), sess.ID, run.RunID(), ev.Ask.AskID, session.VerdictAllowOnce)
		if resolveErr != nil {
			t.Fatalf("ResolveRunAsk: %v", resolveErr)
		}
		if ack.RunID != run.RunID() || ack.AskID != ev.Ask.AskID {
			t.Fatalf("ack = %+v", ack)
		}
		select {
		case <-secondRequestEntered:
		case <-time.After(time.Second):
			t.Fatal("resolved run did not reach its second model request")
		}
		if live, ok := svc.LookupRun(sess.ID); !ok || live != run {
			t.Fatalf("registered run = (%p, %t), want exact live run %p", live, ok, run)
		}
		if _, secondErr := svc.ResolveRunAsk(t.Context(), sess.ID, run.RunID(), ev.Ask.AskID, session.VerdictAllowOnce); !errors.Is(secondErr, server.ErrAskNotPending) {
			t.Fatalf("second resolve = %v, want ErrAskNotPending", secondErr)
		}
		release()
	}
	svc.FinishRun(sess.ID, run)
	if ran.Load() != 1 {
		t.Fatalf("tool executions = %d, want 1", ran.Load())
	}

	planSvc := planApprovalService(t, mockllm.New(mockllm.ToolCallTurn(call("p1", "PresentPlan", `{"plan":"one"}`))), allowRules())
	planSession, err := planSvc.CreateSession(t.Context(), session.ModePlan, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	planRun, err := planSvc.StartRun(t.Context(), planSession.ID, "plan")
	if err != nil {
		t.Fatal(err)
	}
	for ev := range planRun.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			if _, resolveErr := planSvc.ResolveRunAsk(t.Context(), planSession.ID, planRun.RunID(), ev.Ask.AskID, session.VerdictAllowOnce); !errors.Is(resolveErr, server.ErrPlanResolutionRequired) {
				t.Fatalf("plan resolve = %v, want ErrPlanResolutionRequired", resolveErr)
			}
			planRun.Cancel()
		}
	}
	planSvc.FinishRun(planSession.ID, planRun)
}

func TestADR_0346_StaleControlCannotMutateSuccessor(t *testing.T) {
	successorEntered := make(chan struct{})
	releaseSuccessor := make(chan struct{})
	var requestCount atomic.Int64
	svc := newService(t, mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(port.LLMRequest) {
			if requestCount.Add(1) != 2 {
				return
			}
			close(successorEntered)
			<-releaseSuccessor
		}),
	}, mockllm.TextTurn("first"), mockllm.TextTurn("successor")), allowRules())
	sess, _ := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	first, _ := svc.StartRun(t.Context(), sess.ID, "one")
	stale := first.RunID()
	drainRun(t, first)
	svc.FinishRun(sess.ID, first)
	second, err := svc.StartRun(t.Context(), sess.ID, "two")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		second.Cancel()
		close(releaseSuccessor)
		drainRun(t, second)
		svc.FinishRun(sess.ID, second)
	}()
	select {
	case <-successorEntered:
	case <-time.After(time.Second):
		t.Fatal("successor run did not reach its blocked model request")
	}
	for name, err := range map[string]error{
		"resolve-ask": func() error {
			_, e := svc.ResolveRunAsk(t.Context(), sess.ID, stale, "old-ask", session.VerdictAllowOnce)
			return e
		}(),
		"cancel":       func() error { _, e := svc.CancelRun(t.Context(), sess.ID, stale); return e }(),
		"steer":        func() error { _, e := svc.SteerRun(t.Context(), sess.ID, stale, "late", nil, "m"); return e }(),
		"cancel-steer": func() error { _, e := svc.CancelRunSteer(t.Context(), sess.ID, stale, "m"); return e }(),
	} {
		if !errors.Is(err, server.ErrStaleRunControl) || strings.Contains(err.Error(), second.RunID()) {
			t.Errorf("%s stale error = %v, want non-disclosing ErrStaleRunControl", name, err)
		}
	}

	for _, tc := range []struct {
		name      string
		terminate func(*session.Session) error
	}{
		{name: "ended", terminate: (*session.Session).Complete},
		{name: "cancelled", terminate: (*session.Session).Cancel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memstore.New()
			stored, ask := makePersistedControlSession(t, session.SessionID("stale-"+tc.name))
			if _, err := stored.ResumeWith(); err != nil {
				t.Fatal(err)
			}
			if err := tc.terminate(stored); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(t.Context(), stored); err != nil {
				t.Fatal(err)
			}
			var terminalRan atomic.Int64
			terminalSvc := newControlLifecycleService(t, store, mockllm.New(), &terminalRan, nil, nil)
			t.Cleanup(terminalSvc.Close)
			ack, err := terminalSvc.ResolveRunAsk(t.Context(), stored.ID, stored.RunID(), ask.AskID, session.VerdictAllowOnce)
			if !errors.Is(err, server.ErrStaleRunControl) || ack != (server.RunAskAcknowledgement{}) {
				t.Fatalf("ResolveRunAsk on %s target = (%+v, %v), want zero acknowledgement and ErrStaleRunControl", tc.name, ack, err)
			}
			if terminalRan.Load() != 0 {
				t.Fatalf("ResolveRunAsk on %s target started a continuation", tc.name)
			}
		})
	}

	t.Run("persisted-awaiting-only", func(t *testing.T) {
		store := memstore.New()
		parked, ask := makePersistedControlSession(t, "persisted-awaiting-controls")
		if err := store.Save(t.Context(), parked); err != nil {
			t.Fatal(err)
		}
		var persistedRan atomic.Int64
		persistedSvc := newControlLifecycleService(t, store, mockllm.New(), &persistedRan, nil, nil)
		t.Cleanup(persistedSvc.Close)
		if _, live := persistedSvc.LookupRun(parked.ID); live {
			t.Fatal("persisted awaiting fixture unexpectedly has a live run")
		}
		for _, tc := range []struct {
			name string
			want error
			call func() (bool, error)
		}{
			{name: "cancel", want: server.ErrNoActiveRun, call: func() (bool, error) {
				ack, err := persistedSvc.CancelRun(t.Context(), parked.ID, parked.RunID())
				return ack == (server.RunControlAcknowledgement{}), err
			}},
			{name: "steer", want: server.ErrStaleRunControl, call: func() (bool, error) {
				ack, err := persistedSvc.SteerRun(t.Context(), parked.ID, parked.RunID(), "late", nil, "steer-id")
				return ack == (server.RunSteerAcknowledgement{}), err
			}},
			{name: "cancel-steer", want: server.ErrStaleRunControl, call: func() (bool, error) {
				ack, err := persistedSvc.CancelRunSteer(t.Context(), parked.ID, parked.RunID(), "steer-id")
				return ack == (server.RunSteerAcknowledgement{}), err
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				zeroAck, err := tc.call()
				if !errors.Is(err, tc.want) || !zeroAck {
					t.Fatalf("persisted awaiting %s = (zero acknowledgement: %t, %v), want zero acknowledgement and %v", tc.name, zeroAck, err, tc.want)
				}
			})
		}
		got, err := store.Load(t.Context(), parked.ID)
		if err != nil {
			t.Fatal(err)
		}
		pending, ok := got.PendingAsk()
		if got.State != session.StateAwaiting || !ok || pending.AskID != ask.AskID {
			t.Fatalf("persisted awaiting target mutated: state=%q pending=%+v ok=%t", got.State, pending, ok)
		}
		if persistedRan.Load() != 0 {
			t.Fatal("persisted awaiting controls started a continuation")
		}
	})
}

func TestADR_0346_StrictSteerNeverPromotes(t *testing.T) {
	svc := newService(t, mockllm.New(mockllm.TextTurn("first"), mockllm.TextTurn("must not run")), allowRules())
	sess, _ := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	run, _ := svc.StartRun(t.Context(), sess.ID, "one")
	stale := run.RunID()
	drainRun(t, run)
	svc.FinishRun(sess.ID, run)
	ack, err := svc.SteerRun(t.Context(), sess.ID, stale, "late", nil, "m1")
	if !errors.Is(err, server.ErrStaleRunControl) || ack.RunID != "" {
		t.Fatalf("strict late steer = (%+v, %v), want stale without successor", ack, err)
	}
	if _, ok := svc.LookupRun(sess.ID); ok {
		t.Fatal("strict steer promoted a successor")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func waitSessionState(t *testing.T, svc *server.Service, id session.SessionID, want session.State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sess, err := svc.GetSession(context.Background(), id)
		if err == nil && sess.State == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	sess, err := svc.GetSession(context.Background(), id)
	t.Fatalf("session state = %v (%v), want %v", func() any {
		if sess == nil {
			return nil
		}
		return sess.State
	}(), err, want)
}

func repoRootFromTest(t *testing.T) string {
	t.Helper()
	return "../../.."
}
