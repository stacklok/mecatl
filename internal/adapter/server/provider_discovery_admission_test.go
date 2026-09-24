package server_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestProviderModelDiscovery_Scenario3_ServiceRunEntryPaths(t *testing.T) {
	for _, name := range []string{"new-default", "explicit", "mode-selected", "completed", "cancelled", "failed", "orphaned-running", "failed-step-retry", "prepared-retry", "restart-approval"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := memstore.New()
			clock := &fixedClock{t: time.Unix(100, 0)}
			leases := memlease.New(clock, time.Hour)
			var inference atomic.Int32
			var executed atomic.Int64
			compactor := &countingServiceCompactor{}
			newEngine := func(model string) *agent.Engine {
				cat := tool.NewCatalog()
				cat.MustRegister(&writeAskTool{ran: &executed})
				return agent.NewEngine(agent.Deps{
					LLM:     mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(port.LLMRequest) { inference.Add(1) })}, mockllm.TextTurn("admitted")),
					Catalog: cat, Policy: permpolicy.NewPolicy(nil, nil), Model: model, Store: store,
					Compactor: compactor, TokenCounter: agent.HeuristicTokenCounter{}, ContextWindow: func() int { return 128000 },
				})
			}
			cfg := server.Config{
				Engine: newEngine("default-model"), Store: store,
				DefaultResolvedModel: server.ResolvedModel{ProviderID: "default-provider", ModelID: "default-model"},
				ModeNeedsEngine:      func(mode session.PermissionMode) bool { return mode == session.ModePlan },
				SessionEngine: func(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, mode session.PermissionMode) (server.SessionEngineResult, error) {
					provider, model := sel.ProviderID, sel.ModelID
					if mode == session.ModePlan {
						provider, model = "plan-provider", "plan-model"
					}
					return server.SessionEngineResult{Engine: newEngine(model), ProviderID: provider, ModelID: model, BuiltForMode: mode}, nil
				},
			}
			warm, err := newPlacementTestService(cfg)
			if err != nil {
				t.Fatal(err)
			}
			sel := server.ProviderSelector{}
			if name == "explicit" || name == "failed-step-retry" || name == "prepared-retry" || name == "restart-approval" {
				sel = server.ProviderSelector{ProviderID: "selected-provider", ModelID: "selected-model"}
			}
			created, err := warm.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{MaxTurns: 3}, sel)
			if err != nil {
				t.Fatal(err)
			}
			if name == "mode-selected" {
				if _, err := warm.SetMode(ctx, created.ID, session.ModePlan); err != nil {
					t.Fatal(err)
				}
			}
			seed, err := store.Load(ctx, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if name != "new-default" {
				if err := seed.RecordUserPrompt("genuine instruction preserved", nil); err != nil {
					t.Fatal(err)
				}
				if err := seed.BeginTurn(); err != nil {
					t.Fatal(err)
				}
				var calls []session.ToolCall
				if name == "cancelled" || name == "failed" || name == "orphaned-running" || name == "restart-approval" {
					calls = []session.ToolCall{session.NewToolCall("pending-call", "Write", []byte(`{}`))}
				}
				if err := seed.RecordAssistant(session.NewAssistantMessage("genuine assistant history", "", calls)); err != nil {
					t.Fatal(err)
				}
				var transition error
				switch name {
				case "cancelled":
					transition = seed.Cancel()
				case "failed", "failed-step-retry", "prepared-retry":
					transition = seed.Fail()
					if transition == nil {
						transition = seed.RecordFailureMetadata(session.RetryMetadata{Disposition: session.RetryDispositionRetryable, Progress: session.StreamProgressPrecommit})
					}
					if name == "prepared-retry" && transition == nil {
						transition = seed.PrepareFailedStepRetry()
					}
				case "restart-approval":
					transition = seed.PauseForApproval(session.PendingAsk{AskID: "persisted-ask", Tool: "Write", Call: "pending-call", Origin: session.ApprovalOriginPermission})
				case "orphaned-running":
				default:
					transition = seed.Complete()
				}
				if transition != nil {
					t.Fatal(transition)
				}
				if err := store.Save(ctx, seed); err != nil {
					t.Fatal(err)
				}
			}
			warm.Close()
			history := session.CloneMessages(seed.Conversation.Messages)
			failure := seed.FailureMetadata()
			pendingRetry, hadRetry := seed.FailedStepRetryPending()
			pendingAsk, hadAsk := seed.PendingAsk()
			wantProvider, wantModel := "default-provider", "default-model"
			if sel.ProviderID != "" {
				wantProvider, wantModel = sel.ProviderID, sel.ModelID
			}
			if name == "mode-selected" {
				wantProvider, wantModel = "plan-provider", "plan-model"
			}
			var admittedProvider, admittedModel string
			reject := true
			cfg.AwaitContextWindow = func(_ context.Context, provider, model string) error {
				admittedProvider, admittedModel = provider, model
				if reject {
					return server.ErrContextWindowUnavailable
				}
				return nil
			}
			cfg.SessionLease, cfg.LeaseOwner, cfg.LeaseTTL, cfg.LeaseRenewInterval = leases, "admission-owner", time.Hour, time.Hour
			cold, err := newPlacementTestService(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer cold.Close()
			start := func() (*agent.Run, error) {
				switch name {
				case "failed-step-retry", "prepared-retry":
					return cold.RetryFailedRun(ctx, seed.ID)
				case "restart-approval":
					return cold.ApproveRun(ctx, seed.ID, "persisted-ask", session.VerdictAllowOnce, "")
				default:
					return cold.StartRun(ctx, seed.ID, "new submission")
				}
			}
			run, admissionErr := start()
			if run != nil {
				for range run.Events() {
				}
				cold.FinishRun(seed.ID, run)
			}
			if !errors.Is(admissionErr, server.ErrContextWindowUnavailable) || run != nil {
				t.Errorf("admission returned run=%t err=%v", run != nil, admissionErr)
			}
			if admittedProvider != wantProvider || admittedModel != wantModel {
				t.Errorf("gated %q/%q, want %q/%q", admittedProvider, admittedModel, wantProvider, wantModel)
			}
			after, err := cold.GetSession(ctx, seed.ID)
			if err != nil {
				t.Fatal(err)
			}
			if inference.Load() != 0 || compactor.calls != 0 || executed.Load() != 0 {
				t.Errorf("forbidden effects: inference=%d compactions=%d executions=%d", inference.Load(), compactor.calls, executed.Load())
			}
			if len(after.Conversation.Messages) < len(history) || (len(history) > 0 && !reflect.DeepEqual(after.Conversation.Messages[:len(history)], history)) {
				t.Fatal("admission removed or rewrote genuine history")
			}
			for _, m := range after.Conversation.Messages {
				if m.Role == session.RoleUser && m.Text == "new submission" {
					t.Error("rejected prompt was recorded")
				}
			}
			switch name {
			case "failed-step-retry", "prepared-retry":
				gotRetry, hasRetry := after.FailedStepRetryPending()
				if after.State != seed.State || after.FailureMetadata() != failure || gotRetry != pendingRetry || hasRetry != hadRetry {
					t.Errorf("retry state consumed: state=%s failure=%+v retry=%+v/%t", after.State, after.FailureMetadata(), gotRetry, hasRetry)
				}
			case "restart-approval":
				gotAsk, hasAsk := after.PendingAsk()
				if after.State != session.StateAwaiting || hasAsk != hadAsk || !reflect.DeepEqual(gotAsk, pendingAsk) {
					t.Errorf("approval consumed: state=%s pending=%+v", after.State, gotAsk)
				}
			default:
				if after.State != session.StateIdle {
					t.Errorf("terminal recovery state=%s, want idle", after.State)
				}
				if err := session.ValidateToolPairing(after.Conversation.Messages); err != nil {
					t.Errorf("history recovery left orphan: %v", err)
				}
				wantLen := len(history)
				if name == "cancelled" || name == "failed" || name == "orphaned-running" {
					wantLen++
				}
				if len(after.Conversation.Messages) != wantLen {
					t.Errorf("recovery messages=%d, want %d", len(after.Conversation.Messages), wantLen)
				}
			}
			if _, ok := cold.LookupRun(seed.ID); ok {
				t.Error("rejected admission retained provisional run")
			}
			if _, err := leases.Acquire(ctx, seed.ID, "competitor"); !errors.Is(err, port.ErrLeaseHeld) {
				t.Errorf("rejected admission released session-lifetime lease: %v", err)
			}
			reject = false
			run, err = start()
			if err != nil {
				t.Fatal(err)
			}
			if text := drainServerRun(run); text != "admitted" {
				t.Errorf("recovered run result=%q", text)
			}
			cold.FinishRun(seed.ID, run)
			if inference.Load() != 1 {
				t.Errorf("admitted inference=%d, want 1", inference.Load())
			}
			if name == "restart-approval" && executed.Load() != 1 {
				t.Errorf("resumed pending execution=%d, want 1", executed.Load())
			}
			cold.Close()
			if _, err := leases.Acquire(ctx, seed.ID, "competitor"); err != nil {
				t.Errorf("Close did not release lease: %v", err)
			}
		})
	}
}
