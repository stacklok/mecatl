package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
	"github.com/stacklok/mecatl/provider/openai"
)

func TestServerProviderRecovery_Scenario4_ShorterAuxiliaryAndScheduleDeadlines(t *testing.T) {
	t.Run("scheduled fire", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		var calls atomic.Int32
		probe, stopped, firstFailed := make(chan struct{}), make(chan struct{}), make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			if calls.Add(1) == 1 {
				close(firstFailed)
				writeRecoveryFailure(w)
				return
			}
			close(probe)
			select {
			case <-r.Context().Done():
				close(stopped)
			case <-ctx.Done():
			}
		}))
		defer func() { cancel(); srv.Close() }()
		cfg := recoveryAppConfig(t, srv.URL)
		cfg.LLMBreakerCooldown = 20 * time.Millisecond
		built, err := buildIsolated(t, ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer built.Close()
		store, err := jsonlstore.New(cfg.StoreDir)
		if err != nil {
			t.Fatal(err)
		}
		schedules := store.ScheduleStore()
		const name = "recover-with-deadline"
		fireStart := time.Now()
		if defaultFireTimeout != 30*time.Minute {
			t.Fatalf("default fire timeout=%v", defaultFireTimeout)
		}
		if err := schedules.Save(ctx, port.Schedule{
			Spec: port.ScheduleSpec{Name: name, Prompt: "recover", FireTimeout: 2 * time.Second,
				EnvironmentRef: configuredLocalPlacementRef(cfg.Workspace), PlacementScope: string(defaultPlacementScope),
				Trigger: port.TriggerSpec{OneShot: fireStart}},
			State: port.ScheduleState{Enabled: true, NextFireAt: fireStart},
		}); err != nil {
			t.Fatal(err)
		}
		sched := scheduler.New(scheduler.Config{Store: schedules, Clock: wallclock.Clock{}, Fire: makeFireFunc(built.Service, schedules, defaultFireTimeout, nil)})
		defer sched.Stop()
		fireDone := make(chan port.ScheduleFire, 1)
		fireErr := make(chan error, 1)
		go func() {
			fire, err := sched.FireNow(ctx, name, fireStart)
			if err != nil {
				fireErr <- err
				return
			}
			fireDone <- fire
		}()
		awaitRecovery(ctx, t, firstFailed, "scheduled provider failure")
		awaitRecovery(ctx, t, probe, "recovering scheduled provider probe")
		awaitRecovery(ctx, t, stopped, "schedule timeout canceling provider")
		var fire port.ScheduleFire
		select {
		case fire = <-fireDone:
		case err := <-fireErr:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal("scheduled fire did not finish after its injected deadline")
		}
		if calls.Load() != 2 || fire.Stop != session.StopTimeout {
			t.Fatalf("scheduled recovery calls=%d stop=%s error=%s", calls.Load(), fire.Stop, fire.Err)
		}
		recorded, err := schedules.LoadFire(ctx, fire.ID)
		if err != nil {
			t.Fatal(err)
		}
		if recorded.Stop != session.StopTimeout || !recorded.StartedAt.Equal(fireStart) || !recorded.Deadline.Equal(fireStart.Add(2*time.Second)) {
			t.Fatalf("deadline slid from fire start or terminal was lost: %+v", recorded)
		}
	})
	for _, policy := range []string{"warn", "fail"} {
		for _, shorter := range []bool{false, true} {
			name := policy + "/reviewer deadline"
			if shorter {
				name = policy + "/shorter caller"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if shorter {
					ctx, cancel = context.WithTimeout(t.Context(), 300*time.Millisecond)
					defer cancel()
				}
				var calls atomic.Int32
				probe, stopped := make(chan struct{}), make(chan struct{})
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					if calls.Add(1) == 1 {
						writeRecoveryFailure(w)
						return
					}
					close(probe)
					<-r.Context().Done()
					close(stopped)
				}))
				defer func() { cancel(); srv.Close() }()
				cfg := recoveryAppConfig(t, srv.URL)
				cfg.LLMBreakerCooldown = 10 * time.Millisecond
				cfg.GuardrailsModel = cfg.Model
				cfg.GuardrailsOnCheckerDown = policy
				cfg.GuardrailsRules = []GuardrailRule{{Match: "Write", Phases: []string{"pre"}, Mode: "block"}}
				deadlineSeen := make(chan time.Time, 4)
				client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					deadline, ok := r.Context().Deadline()
					if !ok {
						t.Error("composed guardrail request has no caller deadline")
					}
					deadlineSeen <- deadline
					return srv.Client().Transport.RoundTrip(r)
				})}
				entry := newOpenAICompatEntry(cfg, providerOpenAI, "test", srv.URL+"/v1", openai.WithHTTPClient(client))
				reviewer := buildGuardrailsActionReviewer(cfg, regForTest(entry.provider, providerOpenAI, cfg.Model), entry.provider, providerOpenAI, nil)
				reviewer.(*guardrailActionReviewer).base.(*contextualToolReviewer).deadline = 600 * time.Millisecond
				req := reviewRequestWithoutEvidence()
				req.Job = agent.ReviewJobAction
				req.Event = governance.HookEvent{Phase: governance.PhasePreToolUse, SessionID: "session-1", CallID: "write-1", Tool: "Write", Input: []byte(`{"path":"x","content":"x"}`)}
				req.EffectiveCall = session.NewToolCall("write-1", "Write", req.Event.Input)
				req.Caller.Capabilities = []string{"Write"}
				type reviewOutcome struct {
					result agent.ToolReviewResult
					err    error
				}
				done := make(chan reviewOutcome, 1)
				started := time.Now()
				go func() {
					out, _, err := reviewer.Review(ctx, req, nil)
					done <- reviewOutcome{result: out, err: err}
				}()
				select {
				case deadline := <-deadlineSeen:
					if shorter {
						want, _ := ctx.Deadline()
						if !deadline.Equal(want) {
							t.Errorf("caller deadline=%v want %v", deadline, want)
						}
					} else if delta := deadline.Sub(started); delta < 500*time.Millisecond || delta > 650*time.Millisecond {
						t.Errorf("auxiliary deadline=%v want injected 600ms", delta)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("guardrail did not call provider")
				}
				guard, stopGuard := context.WithTimeout(t.Context(), 3*time.Second)
				defer stopGuard()
				awaitRecovery(guard, t, probe, "guardrail half-open recovery probe")
				select {
				case out := <-done:
					wantCode := agent.ReviewFailureTimeout
					if shorter {
						wantCode = ""
						if !errors.Is(out.err, context.DeadlineExceeded) {
							t.Fatalf("caller deadline error=%v", out.err)
						}
					}
					if out.err == nil || out.result.Assessment != agent.ReviewUnresolved || reviewFailureCodeForTest(out.err) != wantCode {
						t.Fatalf("guardrail deadline result=%+v err=%v", out.result, out.err)
					}
					applies, enforce := reviewer.(agent.ReviewPolicyProvider).GuardrailReviewPolicy("Write", agent.ReviewJobAction, true)
					if !applies || enforce != (policy == "fail") {
						t.Errorf("guardrail down policy=%s applies=%v enforce=%v", policy, applies, enforce)
					}
				case <-guard.Done():
					cancel()
					t.Fatal("guardrail recovery bypassed caller deadline")
				}
				awaitRecovery(guard, t, stopped, "guardrail canceled HTTP call")
				if calls.Load() != 2 {
					t.Fatalf("guardrail actual calls=%d want failure + probe", calls.Load())
				}
			})
		}
	}
}

type checkerDownCountingTool struct {
	stubTool
	executions atomic.Int32
}

func (t *checkerDownCountingTool) Execute(ctx context.Context, call session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	t.executions.Add(1)
	return t.stubTool.Execute(ctx, call, env)
}

func TestServerProviderRecovery_Scenario4_CheckerDownPolicyAtToolDispatch(t *testing.T) {
	for _, policy := range []string{"warn", "fail"} {
		t.Run(policy, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if requests.Add(1) == 1 {
					writeRecoveryFailure(w)
					return
				}
				<-r.Context().Done()
			}))
			defer srv.Close()
			cfg := recoveryAppConfig(t, srv.URL)
			cfg.GuardrailsModel = cfg.Model
			cfg.GuardrailsOnCheckerDown = policy
			cfg.GuardrailsRules = []GuardrailRule{{Match: "Grep", Phases: []string{"pre"}, Mode: "block"}}
			cfg.LLMBreakerCooldown = 10 * time.Millisecond
			entry := newOpenAICompatEntry(cfg, providerOpenAI, "test", srv.URL+"/v1")
			reviewer := buildGuardrailsActionReviewer(cfg, regForTest(entry.provider, providerOpenAI, cfg.Model), entry.provider, providerOpenAI, nil)
			reviewer.(*guardrailActionReviewer).base.(*contextualToolReviewer).deadline = 250 * time.Millisecond
			catalog := tool.NewCatalog()
			checkedTool := &checkerDownCountingTool{stubTool: stubTool{name: "Grep"}}
			catalog.MustRegister(checkedTool)
			deps := agent.Deps{
				LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("checked", "Grep", []byte(`{}`))), mockllm.TextTurn("done")),
				Catalog: catalog, Model: "parent-model",
				Policy: permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil),
			}
			attachGuardrailReviewer(&deps, reviewer, nil)
			sess := session.New("deadline-dispatch", session.ModeDefault,
				session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"},
				session.Limits{}, time.Unix(1, 0))
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			seen, timeoutEvidence, warnings := false, 0, 0
			for ev := range agent.NewEngine(deps).Run(ctx, sess, memEnvironment("/ws"), agent.RunRequest{Text: "go"}).Events() {
				if ev.Hook != nil && ev.Hook.Guardrail != nil {
					if ev.Hook.Guardrail.ReasonCode == string(agent.ReviewFailureTimeout) {
						timeoutEvidence++
					}
					if ev.Hook.Guardrail.Disposition == "pass_advisory" {
						warnings++
					}
				}
				if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "checked" {
					seen = true
					if ev.ToolResult.IsError != (policy == "fail") {
						t.Fatalf("checker-down policy %s dispatched result=%+v", policy, ev.ToolResult)
					}
				}
			}
			wantExecutions, wantWarnings := int32(0), 0
			if policy == "warn" {
				wantExecutions, wantWarnings = 1, 1
			}
			if !seen || requests.Load() != 2 || checkedTool.executions.Load() != wantExecutions || warnings != wantWarnings || timeoutEvidence != 1 {
				t.Fatalf("checker-down policy %s result=%v requests=%d executions=%d warnings=%d timeout_evidence=%d", policy, seen, requests.Load(), checkedTool.executions.Load(), warnings, timeoutEvidence)
			}
		})
	}
}

func TestServerProviderRecovery_Scenario5_TUINoTerminalAutoRetryAndScheduleNoRearm(t *testing.T) {
	for _, succeed := range []bool{true, false} {
		name := "successful recovery"
		if !succeed {
			name = "explicit one_shot_retry after StopError"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
			defer cancel()
			var calls atomic.Int32
			probe, release := make(chan struct{}), make(chan struct{})
			var sessionID atomic.Value
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				id := r.Header.Get("X-Mecatl-Session-ID")
				if n := calls.Add(1); n == 1 {
					sessionID.Store(id)
					writeRecoveryFailure(w)
					return
				}
				if id != sessionID.Load() {
					t.Errorf("scheduled recovery replaced session %v with %s", sessionID.Load(), id)
				}
				close(probe)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				if !succeed {
					writeRecoveryFailure(w)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				writeRecoveryTextTurn(w, "scheduled recovered")
			}))
			defer func() { cancel(); srv.Close() }()
			cfg := recoveryAppConfig(t, srv.URL)
			cfg.LLMMaxAttempts = 2
			built, err := buildIsolated(t, ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			store, err := jsonlstore.New(cfg.StoreDir)
			if err != nil {
				t.Fatal(err)
			}
			schedules := store.ScheduleStore()
			const scheduleName = "recover-once"
			now := time.Now()
			if err := schedules.Save(ctx, port.Schedule{
				Spec: port.ScheduleSpec{Name: scheduleName, Prompt: "recover once", OneShotRetry: true, OneShotMaxRetries: 1,
					EnvironmentRef: configuredLocalPlacementRef(cfg.Workspace), PlacementScope: string(defaultPlacementScope),
					Trigger: port.TriggerSpec{OneShot: now}},
				State: port.ScheduleState{Enabled: true, NextFireAt: now},
			}); err != nil {
				t.Fatal(err)
			}
			sched := scheduler.New(scheduler.Config{Store: schedules, Clock: wallclock.Clock{}, Fire: makeFireFunc(built.Service, schedules, defaultFireTimeout, nil)})
			defer sched.Stop()
			done := make(chan port.ScheduleFire, 1)
			go func() {
				fire, err := sched.FireNow(ctx, scheduleName, now)
				if err != nil {
					t.Errorf("FireNow: %v", err)
				}
				done <- fire
			}()
			awaitRecovery(ctx, t, probe, "scheduled recovering probe")
			sched.RunOnceForTest(ctx) // exercise the real rearm scan while recovery is live.
			active, err := schedules.Load(ctx, scheduleName)
			if err != nil {
				t.Fatal(err)
			}
			fires, err := schedules.ListFires(ctx, scheduleName)
			if err != nil {
				t.Fatal(err)
			}
			if active.State.FireCount != 1 || active.State.OneShotRetryCount != 0 || active.State.Enabled || len(fires) != 1 || fires[0].Stop != "" || string(fires[0].SessionID) != sessionID.Load() {
				t.Fatalf("active recovery rearmed or replaced fire: state=%+v fires=%+v", active.State, fires)
			}
			close(release)
			var fire port.ScheduleFire
			select {
			case fire = <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			wantStop := session.StopEndTurn
			if !succeed {
				wantStop = session.StopError
			}
			if fire.Stop != wantStop || fire.SessionID != fires[0].SessionID {
				t.Fatalf("terminal changed fire: %+v", fire)
			}
			sched.RunOnceForTest(ctx)
			finished, err := schedules.Load(ctx, scheduleName)
			if err != nil {
				t.Fatal(err)
			}
			wantRetries := 0
			if !succeed {
				wantRetries = 1
			}
			if finished.State.Enabled != !succeed || finished.State.OneShotRetryCount != wantRetries || finished.State.FireCount != 1 {
				t.Fatalf("terminal rearm policy=%+v", finished.State)
			}
			rows, err := store.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || calls.Load() != 2 {
				t.Fatalf("fire/session duplication: sessions=%d calls=%d", len(rows), calls.Load())
			}
		})
	}
}
