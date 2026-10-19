package app

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// session_scoped_agent_identity_scenario1_guardrail_test.go pins AC1.7/AC1.8
// (ADR 0353 Task E, docs/acceptance/session-scoped-agent-identity.md): an
// agent-bound session's contextual guardrail review (engine/agent/dispatch.go's
// reviewAction for PreToolUse, prepareInboundAssessment/assessInbound for
// PostToolUse — ADR 0363) inspects the EFFECTIVE (post-hook-mutation) payload
// for BOTH phases, exactly like any other main session — because an
// agent-bound session's engine bottoms out in ordinary main-session Deps
// (AC1.6), it inherits this review unchanged. No production code change is
// expected here: these are PINNING regression tests against the real,
// already-working mechanism, guarded so a future refactor cannot silently
// exclude agent-bound sessions from it.
//
// Both tests build a FULL app.Build -> server.Service agent-bound session (the
// same composition TestSessionScopedAgentIdentity_Scenario1_OrdinaryMainSessionBehavior
// uses) whose def scopes real `hooks:` (PreToolUse/PostToolUse shell commands,
// via internal/app/agentdefs.go's defHookRunner + internal/adapter/hookexec) and
// whose Config wires a real GuardrailsRules-configured contextual reviewer
// (internal/app/guardrails.go's buildGuardrailsActionReviewer) driven by a
// content-aware fake LLMProvider that ALSO stands in for the deployment's
// single mock provider entry, so both the session's own model calls and the
// guardrail checker's calls flow through one deterministic, offline seam.

// reviewCapturingProvider is a content-aware fake port.LLMProvider standing in
// for the SINGLE mock provider entry a Config{MockProvider: ...} Build wires —
// the ordinary main-session engine's model calls AND the guardrail checker
// engine's calls (internal/app/guardrails.go's buildGuardrailsReviewer) share
// this exact instance (both resolve through the same provider registry
// entry), so one fake can drive and observe both.
//
// A non-checker call (the agent-bound session's own model) replays the
// scripted mainTurns in order, exactly like mockllm.Provider. A checker call
// (identified by the reviewer's own fixed prompt prefix — see
// checkerPromptText) is answered directly here: it records the exact prompt
// text it received (so a test can assert precisely what content the reviewer
// inspected) and returns "prohibited" when the prompt contains flagMarker,
// "acceptable" otherwise. An empty flagMarker never flags.
type reviewCapturingProvider struct {
	mu         sync.Mutex
	mainTurns  []mockllm.Turn
	mainCursor int
	flagMarker string
	reviews    []string
}

func newReviewCapturingProvider(flagMarker string, mainTurns ...mockllm.Turn) *reviewCapturingProvider {
	return &reviewCapturingProvider{mainTurns: mainTurns, flagMarker: flagMarker}
}

func (*reviewCapturingProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

// checkerPromptText reports the exact user-prompt text and true when req is a
// guardrail-checker call: internal/app/contextual_reviewer.go's
// buildContextualReviewPrompt always starts the checker's ONE user message
// with the literal "Review ID:" — a shape the agent-bound session's own model
// turns (plain tool-call/text scripts) never produce.
func checkerPromptText(req port.LLMRequest) (string, bool) {
	for _, msg := range req.Messages {
		if msg.Role == session.RoleUser && strings.HasPrefix(msg.Text, "Review ID:") {
			return msg.Text, true
		}
	}
	return "", false
}

func (p *reviewCapturingProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	var turn mockllm.Turn
	if text, ok := checkerPromptText(req); ok {
		p.mu.Lock()
		p.reviews = append(p.reviews, text)
		p.mu.Unlock()
		assessment, concerns := "acceptable", "[]"
		if p.flagMarker != "" && strings.Contains(text, p.flagMarker) {
			assessment = "prohibited"
			concerns = `[{"ref":"c1","category":"authority_crossing","rationale":"mutated content matched the configured guardrail marker","source_ref":"call"}]`
		}
		turn = mockllm.TextTurn(fmt.Sprintf(`{"assessment":%q,"concerns":%s,"evidence":[],"missing_evidence":[]}`, assessment, concerns))
	} else {
		p.mu.Lock()
		if p.mainCursor < len(p.mainTurns) {
			turn = p.mainTurns[p.mainCursor]
			p.mainCursor++
		}
		p.mu.Unlock()
	}
	return func(yield func(port.Chunk, error) bool) {
		for _, c := range turn.Chunks {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if !yield(c, nil) {
				return
			}
		}
		if turn.Err != nil {
			yield(port.Chunk{}, turn.Err)
		}
	}, nil
}

// reviewContains reports whether any captured checker prompt contains substr.
func (p *reviewCapturingProvider) reviewContains(substr string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range p.reviews {
		if strings.Contains(r, substr) {
			return true
		}
	}
	return false
}

func (p *reviewCapturingProvider) reviewCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reviews)
}

// writeHookedAgentDefFile writes an agent-def markdown file scoping a Read-only
// tool allowlist and `hooks:` whose PreToolUse/PostToolUse always rewrite,
// respectively, the call's path argument and the tool's result content to the
// given fixed marker strings — regardless of what the model actually
// requested or what Read actually produced. This isolates "did the hook's
// mutation reach the guardrail" from "did the hook correctly parse its input",
// which is not this task's concern.
func writeHookedAgentDefFile(t *testing.T, dir, name, preMutatedPath, postMutatedContent string) {
	t.Helper()
	preMutation, err := json.Marshal(map[string]any{"mutated": map[string]string{"path": preMutatedPath}})
	if err != nil {
		t.Fatalf("marshal PreToolUse mutation: %v", err)
	}
	postMutation, err := json.Marshal(map[string]any{"mutated": map[string]any{"content": postMutatedContent, "is_error": false}})
	if err != nil {
		t.Fatalf("marshal PostToolUse mutation: %v", err)
	}
	def := "---\n" +
		"name: " + name + "\n" +
		"description: " + name + " specialist\n" +
		"tools: [Read]\n" +
		"hooks:\n" +
		"  PreToolUse: |\n" +
		"    printf '%s' " + shellSingleQuote(string(preMutation)) + "\n" +
		"  PostToolUse: |\n" +
		"    printf '%s' " + shellSingleQuote(string(postMutation)) + "\n" +
		"---\n" +
		"You are a fixture agent for a contextual-guardrail pinning regression test.\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(def), 0o644); err != nil {
		t.Fatalf("write def %s: %v", name, err)
	}
}

// shellSingleQuote wraps s in POSIX single quotes, escaping any embedded single
// quote (none of our fixed JSON marker payloads contain one today, but this
// keeps the fixture writer correct if that ever changes).
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// --- AC1.7 -------------------------------------------------------------------

// TestSessionScopedAgentIdentity_Scenario1_ContextualGuardrailInspectsEffectivePayload
// pins AC1.7: an agent-bound session's contextual guardrail review inspects the
// EFFECTIVE payload for BOTH phases — pre.effective (the PreToolUse hook's
// rewritten call args) for the ACTION job, and the post-postHook result (the
// PostToolUse hook's rewritten content) for the INBOUND job — exactly as any
// other main session's review does. The checker never flags either payload
// here (flagMarker is empty): this is the simpler positive-path confirmation
// that the mechanism is reached and sees the mutated bytes, distinct from
// AC1.8's flagging/bypass-attempt case below.
func TestSessionScopedAgentIdentity_Scenario1_ContextualGuardrailInspectsEffectivePayload(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	agentsDir := t.TempDir()
	const (
		preMarker  = "EFFECTIVE_PRE_MARKER_AC17"
		postMarker = "EFFECTIVE_POST_MARKER_AC17"
	)
	writeHookedAgentDefFile(t, agentsDir, "inspector", preMarker, postMarker)
	// The PreToolUse hook rewrites Read's path argument to preMarker — make
	// that a REAL, readable file (rather than a bare marker string) so Read
	// actually succeeds. A nonexistent path here previously drove an unrelated
	// tool-retry-on-error loop (three identical ACTION reviews, no INBOUND
	// review ever reached) that has nothing to do with what this test pins.
	if err := os.WriteFile(filepath.Join(ws, preMarker), []byte("effective-file-contents"), 0o644); err != nil {
		t.Fatalf("write real file at the mutated path: %v", err)
	}

	originalArgs, err := json.Marshal(map[string]string{"path": "requested-original.txt"})
	if err != nil {
		t.Fatalf("marshal original args: %v", err)
	}
	provider := newReviewCapturingProvider("", // never flags
		mockllm.ToolCallTurn(session.NewToolCall("r1", "Read", json.RawMessage(originalArgs))),
		mockllm.TextTurn("done"),
	)
	built, err := buildIsolated(t, ctx, Config{
		Workspace: ws, NoSoul: true, Model: "gpt-5", AgentsDirs: []string{agentsDir},
		MockProvider:    provider,
		GuardrailsModel: "checker-model",
		GuardrailsRules: []GuardrailRule{{Match: "Read", Mode: "block"}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault, server.WithAgentDefinitionName("inspector"))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	run, err := built.Service.StartRun(ctx, sess.ID, "read the file")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			t.Fatalf("unexpected permission ask (acceptable assessments must not pause the run): %+v", ev.Ask)
		}
	}
	built.Service.FinishRun(sess.ID, run)

	if provider.reviewCount() != 2 {
		for i, r := range provider.reviews {
			t.Logf("review[%d] = %s", i, r)
		}
		t.Fatalf("guardrail review calls = %d, want 2 (one ACTION review, one INBOUND review)", provider.reviewCount())
	}
	if !provider.reviewContains(preMarker) {
		t.Fatal("ACTION review never saw the PreToolUse hook's mutated (effective) path — the review is not inspecting pre.effective")
	}
	if provider.reviewContains("requested-original.txt") {
		t.Fatal("ACTION review saw the ORIGINAL pre-mutation path — it must inspect ONLY the effective call, never the model's original request")
	}
	if !provider.reviewContains(postMarker) {
		t.Fatal("INBOUND review never saw the PostToolUse hook's mutated (effective) result content — the review is not inspecting the post-hook result")
	}
}

// --- AC1.8 -------------------------------------------------------------------

// TestSessionScopedAgentIdentity_Scenario1_HookMutationCannotBypassGuardrail
// pins AC1.8: a def hook that mutates either phase's payload is not exempt
// from contextual guardrail review — here BOTH the PreToolUse hook's rewritten
// call and the PostToolUse hook's rewritten result carry a marker a configured
// guardrail rule flags, and both are, in fact, flagged (surfaced as an
// interactive guardrail ask rather than silently let through) — proving a def
// hook cannot launder a call/result past the operator guardrail by mutating it
// into something a native reviewer would have accepted pre-mutation.
//
// The content-aware fake reviewer's verdict is CONDITIONAL on the mutated
// marker actually reaching it (see reviewCapturingProvider.Stream): unlike a
// reviewer that always says "prohibited" regardless of content, a bypass
// regression (reviewing the ORIGINAL, unmutated payload instead of the
// effective one) would make this test's assessments come back "acceptable"
// and the asserted asks would never fire — so this test is sensitive to
// exactly the regression it pins, not vacuously green. (Confirmed by hand:
// temporarily blanking flagMarker's use — i.e. scripting the reviewer to
// always answer "acceptable" — turns both assertions below red, as expected.)
func TestSessionScopedAgentIdentity_Scenario1_HookMutationCannotBypassGuardrail(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	agentsDir := t.TempDir()
	const flagMarker = "AC18_FLAG_MARKER"
	preMarker := flagMarker + "_PATH"
	postMarker := flagMarker + "_RESULT"
	writeHookedAgentDefFile(t, agentsDir, "escalator", preMarker, postMarker)

	provider := newReviewCapturingProvider(flagMarker,
		mockllm.ToolCallTurn(session.NewToolCall("r1", "Read", json.RawMessage(`{"path":"benign.txt"}`))),
		mockllm.TextTurn("done"),
	)
	built, err := buildIsolated(t, ctx, Config{
		Workspace: ws, NoSoul: true, Model: "gpt-5", AgentsDirs: []string{agentsDir},
		MockProvider:    provider,
		Interactive:     true, // surface guardrail findings as an ask rather than a headless deny
		GuardrailsModel: "checker-model",
		GuardrailsRules: []GuardrailRule{{Match: "Read", Mode: "block"}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	sess, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault, server.WithAgentDefinitionName("escalator"))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	run, err := built.Service.StartRun(ctx, sess.ID, "read the benign file")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var actionAsks, releaseAsks int
	for ev := range run.Events() {
		if ev.Type != session.EvPermissionAsk || ev.Ask == nil || ev.Ask.Guardrail == nil {
			continue
		}
		switch ev.Ask.Guardrail.Kind {
		case session.GuardrailApprovalAction:
			actionAsks++
		case session.GuardrailApprovalResultRelease:
			releaseAsks++
		}
		if _, err := built.Service.ResolveApprovalRun(ctx, sess.ID, agent.ApprovalResolution{
			AskID: ev.Ask.AskID, ReviewID: ev.Ask.Guardrail.ReviewID, Kind: ev.Ask.Guardrail.Kind,
			Verdict: session.VerdictAllowOnce,
		}, ""); err != nil {
			t.Fatalf("resolve guardrail ask: %v", err)
		}
	}
	built.Service.FinishRun(sess.ID, run)

	if actionAsks != 1 {
		t.Fatalf("guardrail ACTION asks = %d, want 1 — the PreToolUse-mutated call must be flagged, not silently allowed through", actionAsks)
	}
	if releaseAsks != 1 {
		t.Fatalf("guardrail result-release asks = %d, want 1 — the PostToolUse-mutated result must be flagged, not silently released", releaseAsks)
	}
	if !provider.reviewContains(preMarker) {
		t.Fatal("ACTION review never saw the PreToolUse hook's mutated (effective) path")
	}
	if !provider.reviewContains(postMarker) {
		for i, r := range provider.reviews {
			t.Logf("review[%d] = %s", i, r)
		}
		t.Fatal("INBOUND review never saw the PostToolUse hook's mutated (effective) result content")
	}
}
