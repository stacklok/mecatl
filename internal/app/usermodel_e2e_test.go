package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/memory"
)

// drainRun consumes a run's events to completion, returning the terminal result
// text. Any permission ask is auto-allowed.
func drainRun(run interface {
	Events() <-chan session.Event
	Approve(string, session.ApprovalVerdict) error
}) string {
	var final string
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			final = ev.Result.Text
		}
	}
	return final
}

// TestUserModelE2E is the headline (R8) Phase-2a proof:
//
//   - "Session A" writes a fact through the REAL RememberUser tool the catalog wires
//     (memory.NewUserModelTools over the configured user-model dir) — exercising the
//     enforced "user/" prefix, the write-time injection scan, and persistence.
//   - The same composition wiring (buildInstructionAssembler → prompt.UserModelAssembler
//     over a SEPARATE memory.Store opened on the SAME dir) is wired into a main engine
//     whose provider OBSERVES the request, and a run asserts the turn-0 REQUEST carries
//     the <user-model> fence with the saved fact — proving the cross-session store
//     round-trips and the assembler injects it on a subsequent session's turn 0.
//   - A THIRD section drives the FULL composition (app.Build → server.Service) over the
//     same user-model dir and asserts the per-session engine does NOT PERSIST the fence
//     into the conversation — the composition-level ephemeral guard (a composition-only
//     re-persist regression, e.g. recordPrompt re-acquiring the fragments, would be
//     caught here even though the Build mock provider is not request-observable).
//
// As of ADR 0043 the turn-0 instruction fragments are EPHEMERAL: they are prepended
// to the LLMRequest per-run, NEVER persisted into Conversation.Messages. So the proof
// is the fence on the REQUEST the provider received (observed via mockllm's request
// observer), and the test ALSO asserts it never lands in the persisted conversation
// (both at the engine layer AND through the full Service path).
//
// (The canned mock provider cannot be scripted to emit a tool call, so session A's
// WRITE is driven via the tool directly — the same tool.Execute the model would
// invoke. The agent-driven RememberUser write is covered end to end in the agent
// reviewer unit test and the adapter tool tests.)
//
// All offline: mock provider, real temp dirs.
func TestUserModelE2E(t *testing.T) {
	ctx := context.Background()

	workspace := t.TempDir()
	userModelDir := t.TempDir()

	// --- Session A's effect: write a fact through the real RememberUser tool. ----
	storeA, err := memory.New(userModelDir)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	var remember tool.Tool
	for _, tl := range memory.NewUserModelTools(storeA) {
		if tl.Spec().Name == memory.RememberUserToolName {
			remember = tl
		}
	}
	rememberArgs, _ := json.Marshal(map[string]any{
		"key":         "comm-style",
		"value":       "Préfère 日本語 and terse, direct answers — no preamble.",
		"description": "communication style",
	})
	res, err := remember.Execute(ctx, session.NewToolCall("c1", memory.RememberUserToolName, rememberArgs), memEnvironment("/ws"))
	if err != nil || res.IsError {
		t.Fatalf("RememberUser write: err=%v isError=%v content=%q", err, res.IsError, res.Content)
	}
	for _, tc := range []struct {
		name        string
		key         string
		value       string
		description string
	}{
		{name: "compound value name", key: "openrouter_api_key", value: "secretvalue0123456789abc"},
		{name: "compound description name", key: "provider", value: "openrouter", description: "aws-secret-access-key: descriptionsecret0123456789"},
	} {
		args, _ := json.Marshal(map[string]any{"key": tc.key, "value": tc.value, "description": tc.description})
		got, executeErr := remember.Execute(ctx, session.NewToolCall(session.ToolCallID("secret-"+tc.name), memory.RememberUserToolName, args), memEnvironment("/ws"))
		if executeErr != nil || !got.IsError {
			t.Fatalf("RememberUser %s: err=%v isError=%v content=%q", tc.name, executeErr, got.IsError, got.Content)
		}
	}
	benignArgs, _ := json.Marshal(map[string]any{
		"key": "token-budget", "value": "benignvalue0123456789abc", "description": "token budget identifier",
	})
	if got, executeErr := remember.Execute(ctx, session.NewToolCall("benign", memory.RememberUserToolName, benignArgs), memEnvironment("/ws")); executeErr != nil || got.IsError {
		t.Fatalf("RememberUser benign compound name: err=%v isError=%v content=%q", executeErr, got.IsError, got.Content)
	}

	// Simulate pre-validation legacy records by editing the original flat-file shape
	// directly. The final profile boundary must omit both a compound secret key and
	// a compound secret label in a description.
	memoryPath := filepath.Join(userModelDir, "memory.json")
	persistedBytes, err := os.ReadFile(memoryPath)
	if err != nil {
		t.Fatalf("read legacy memory fixture: %v", err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(persistedBytes, &persisted); err != nil {
		t.Fatalf("decode legacy memory fixture: %v", err)
	}
	entries, ok := persisted["entries"].(map[string]any)
	if !ok {
		t.Fatalf("legacy memory fixture has no entries object: %#v", persisted)
	}
	entries["user/legacy_service_token"] = map[string]any{"value": "legacysecret0123456789abc", "updated_at": time.Now().UTC()}
	entries["user/legacy-provider"] = map[string]any{"value": "provider", "description": "client.credentials: legacydescription0123456789", "updated_at": time.Now().UTC()}
	persistedBytes, err = json.Marshal(persisted)
	if err != nil {
		t.Fatalf("encode legacy memory fixture: %v", err)
	}
	if err := os.WriteFile(memoryPath, persistedBytes, 0o600); err != nil {
		t.Fatalf("write legacy memory fixture: %v", err)
	}

	// Persistence to disk: the write is durable under the dir (storeA released its
	// lock after the op). The cross-PROCESS round-trip is then proven below by the
	// SEPARATE store opened over the SAME dir.
	if _, ok, _ := storeA.Recall(ctx, "user/comm-style"); !ok {
		t.Fatalf("RememberUser did not persist to the user-model store")
	}
	if _, ok, _ := storeA.Recall(ctx, "user/token-budget"); !ok {
		t.Fatal("benign compound-name memory was not persisted")
	}
	for _, key := range []string{"user/openrouter_api_key", "user/provider"} {
		if _, ok, _ := storeA.Recall(ctx, key); ok {
			t.Fatalf("rejected compound-name secret %q was persisted", key)
		}
	}

	// --- Session B: the SAME composition wiring (buildInstructionAssembler over a
	// fresh store on the SAME dir) injected into a request-observing main engine. The
	// turn-0 REQUEST must carry the <user-model> fence with the saved fact, and the
	// fact must NOT be persisted into the conversation (ephemeral, ADR 0043).
	storeB, err := memory.New(userModelDir)
	if err != nil {
		t.Fatalf("memory.New (session B store): %v", err)
	}
	// soulSrc + project memStore nil — isolate the user-model block (matches the old
	// NoSoul:true). The cast mirrors composition (the adapter satisfies the port).
	asm := buildInstructionAssembler(nil, nil, nil, storeB, false)

	obs := &observedReq{}
	prov := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(obs.observer())}, mockllm.TextTurn("done"))
	eng := agent.NewEngine(agent.Deps{
		LLM:                   prov,
		Catalog:               tool.NewCatalog(),
		Instructions:          asm,
		OperatorProfileSource: storeB,
	})

	sess := session.New("sB", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 3}, time.Now())
	run := eng.Run(ctx, sess, memEnvironment("/ws"), agent.RunRequest{Text: "hello"})
	for ev := range run.Events() {
		_ = ev
	}

	// The full Unicode fact rides the volatile system suffix, not a user fragment.
	func() {
		obs.mu.Lock()
		defer obs.mu.Unlock()
		var foundInReq bool
		for _, system := range obs.systems {
			for _, secret := range []string{"legacy_service_token", "legacysecret0123456789abc", "legacy-provider", "legacydescription0123456789"} {
				if strings.Contains(system, secret) {
					t.Fatalf("legacy secret-shaped memory %q reached request system suffix: %q", secret, system)
				}
			}
			if strings.Contains(system, "<operator-profile-data") && strings.Contains(system, "comm-style") && strings.Contains(system, "Préfère 日本語 and terse, direct answers — no preamble.") && strings.Contains(system, "token-budget") && strings.Contains(system, "benignvalue0123456789abc") {
				foundInReq = true
				break
			}
		}
		if !foundInReq {
			t.Fatalf("request system suffix is missing the operator profile with the saved fact:\n%+v", obs.systems)
		}
		for _, userMessage := range obs.userMsgs {
			if strings.Contains(userMessage, "comm-style") {
				t.Fatalf("operator profile leaked into a user-message fragment: %q", userMessage)
			}
		}
	}()
	// Ephemeral: the fence must NOT be persisted into the conversation.
	for _, m := range sess.Conversation.Messages {
		if strings.Contains(m.Text, "<user-model>") || strings.Contains(m.Text, "<operator-profile-data") || strings.Contains(m.Text, "Préfère 日本語") {
			t.Fatalf("operator profile must NOT be persisted into the conversation:\n%+v", messageTexts(sess.Conversation.Messages))
		}
	}

	// --- Section 3: the FULL composition (app.Build → server.Service) over the same
	// user-model dir. The composition wires the SAME UserModelAssembler; after a run,
	// the per-session engine's PERSISTED conversation must carry ZERO turn-0 fragments
	// — the composition-level ephemeral guard. (The Build mock provider is not
	// request-observable, so the request-side prepend proof stays at the engine layer
	// above; here we guard the persistence side end to end through the Service.)
	built, err := Build(ctx, Config{
		Workspace:    workspace,
		UseMock:      true,
		UserModelDir: userModelDir,
		NoSoul:       true, // isolate the user-model block from the soul
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	svc := built.Service
	svcSess, err := svc.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	svcRun, err := svc.StartRun(ctx, svcSess.ID, "hello")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	drainRun(svcRun)

	got, err := svc.GetSession(ctx, svcSess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	for _, m := range got.Conversation.Messages {
		if strings.Contains(m.Text, "<user-model>") || strings.Contains(m.Text, "<operator-profile-data") || strings.Contains(m.Text, "Préfère 日本語") {
			t.Fatalf("composition path persisted operator-profile data:\n%+v", messageTexts(got.Conversation.Messages))
		}
	}
}

func messageTexts(msgs []session.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, string(m.Role)+": "+m.Text)
	}
	return out
}
