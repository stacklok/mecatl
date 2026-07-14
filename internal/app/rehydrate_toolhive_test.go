package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestToolhiveRehydrationWithProxyDownE2E is the issue #262 R1.3 falsifiable
// gate through the FULL composition (app.Build -> server.Service), offline,
// two-Build (the TestApproveAfterRestartE2E precedent):
//
//  1. built1: the ToolHive LLM gateway config is detected and the proxy
//     responds (a mock transport, never a real port). A session selects
//     provider "toolhive" explicitly; one turn runs successfully.
//  2. built1.Close() models the process exit.
//  3. built2: the SAME toolhiveConfigPath (so the "toolhive" intent is
//     detected again — register-on-intent means this is unconditional,
//     R1.1), but the probe transport now REFUSES (the proxy is down) and the
//     provider construction seam simulates a connection failure on the
//     ACTUAL request. StartRunContent must NOT reject the persisted
//     ProviderID="toolhive" selector with "unknown or unavailable provider"
//     (server.ErrInvalidArgument) — the whole point of register-on-intent is
//     that the entry still EXISTS; the failure surfaces only when the model
//     call itself is attempted, landing the session in StateFailed via the
//     ordinary stream-error path (mockllm.ErrorTurn), never a build/session
//     rejection.
func TestToolhiveRehydrationWithProxyDownE2E(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()
	cfgPath := writeToolhiveConfig(t, "https://upstream.example/gw")

	baseCfg := func() Config {
		return Config{
			Workspace:          workspace,
			NoSoul:             true,
			StoreDir:           storeDir,
			ToolhiveLLM:        true,
			toolhiveConfigPath: cfgPath,
			envDetector:        fakeEnv(nil), // hermetic: never read the real process environment
		}
	}

	// built1: the proxy is "up" — the probe succeeds and the (mocked) chat
	// request succeeds too.
	cfg1 := baseCfg()
	cfg1.liveModelHTTPClient = toolhiveModelsClient(t, toolhiveFixtureJSON)
	cfg1.providerConstructor = func(_ Config, id, _, _ string) port.LLMProvider {
		if id != providerToolhive {
			t.Fatalf("unexpected provider constructed: %q", id)
		}
		return mockllm.New(mockllm.TextTurn("pre-restart-done"))
	}
	built1, err := Build(ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	sess, err := built1.Service.CreateSessionWithProvider(ctx, workspace, session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: providerToolhive, ModelID: "claude-sonnet-4-6"})
	if err != nil {
		built1.Close()
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	run1, err := built1.Service.StartRun(ctx, sess.ID, "first toolhive turn")
	if err != nil {
		built1.Close()
		t.Fatalf("StartRun (pre-restart): %v", err)
	}
	runEvents(run1)
	built1.Close() // "process exit"

	// built2: the SAME config path (register-on-intent re-detects it) but the
	// proxy is now down — the probe transport refuses, and the chat request
	// simulates a genuine connection failure via mockllm.ErrorTurn.
	cfg2 := baseCfg()
	cfg2.liveModelHTTPClient = offlineHTTPClient()
	cfg2.providerConstructor = func(_ Config, id, _, _ string) port.LLMProvider {
		if id != providerToolhive {
			t.Fatalf("unexpected provider constructed: %q", id)
		}
		return mockllm.New(mockllm.ErrorTurn(errors.New("connection refused: proxy not running")))
	}
	built2, err := Build(ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2 (proxy down) must still succeed (register-on-intent): %v", err)
	}
	defer built2.Close()

	run2, err := built2.Service.StartRunContent(ctx, sess.ID, "post-restart toolhive turn", nil)
	if err != nil {
		if strings.Contains(err.Error(), "unknown or unavailable provider") {
			t.Fatalf("StartRunContent rejected the persisted toolhive selector as unknown/unavailable "+
				"(register-on-intent must survive a down proxy): %v", err)
		}
		if errors.Is(err, server.ErrInvalidArgument) {
			t.Fatalf("StartRunContent returned ErrInvalidArgument for a down-but-registered provider: %v", err)
		}
		// Any OTHER synchronous error is out of scope for this gate (it would be
		// a different composition failure); fail loudly so it gets attention.
		t.Fatalf("StartRunContent (post-restart, proxy down): unexpected error: %v", err)
	}

	events := runEvents(run2)
	sawFailedTerminal := false
	for _, ev := range events {
		if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopError {
			sawFailedTerminal = true
		}
	}
	if !sawFailedTerminal {
		t.Fatalf("expected a StopError terminal from the simulated connection failure, events: %+v", events)
	}

	reloaded, err := built2.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession post-run: %v", err)
	}
	if reloaded.State != session.StateFailed {
		t.Errorf("session state = %v, want %v (a request-time provider failure, not a rejection)", reloaded.State, session.StateFailed)
	}
}
