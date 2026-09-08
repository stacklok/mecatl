package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/memory"
)

func TestUserFacingChildRolesInjectFreshVolatileOperatorProfile(t *testing.T) {
	for _, role := range []string{"task", "parallel", "member:lead"} {
		t.Run(role, func(t *testing.T) {
			store := memmemory.New()
			if err := store.RememberEntry(context.Background(), tool.MemoryEntry{Key: "user/output/language", Value: "Prefer French by default."}); err != nil {
				t.Fatal(err)
			}
			var requests []port.LLMRequest
			provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { requests = append(requests, req) })}, mockllm.TextTurn("done"), mockllm.TextTurn("done"))
			cfg := Config{Model: "m", operatorProfileSource: store}
			eng := newChildEngineForProvider(cfg, role, provider, "m", fixedDefaultWindow, tool.NewCatalog(), prompt.Config{}, nil)

			runChildProfileTurn(t, eng, "child-1", "Answer in English, exactly.")
			if err := store.RememberEntry(context.Background(), tool.MemoryEntry{Key: "user/output/language", Value: "Prefer Japanese by default."}); err != nil {
				t.Fatal(err)
			}
			runChildProfileTurn(t, eng, "child-2", "Keep this prompt verbatim.")
			if len(requests) != 2 || !strings.Contains(requests[0].System.VolatileSuffix, "Prefer French") || !strings.Contains(requests[1].System.VolatileSuffix, "Prefer Japanese") {
				t.Fatalf("profile did not refresh per child request: %#v", requests)
			}
			if got := requests[0].Messages[len(requests[0].Messages)-1]; got.Role != session.RoleUser || got.Text != "Answer in English, exactly." {
				t.Fatalf("genuine user prompt lost final-message precedence: %+v", got)
			}
			for _, message := range requests[0].Messages {
				if strings.Contains(message.Text, "Prefer French") || strings.Contains(message.Text, "operator-profile-data") {
					t.Fatalf("profile became a synthetic history turn: %+v", message)
				}
			}
		})
	}
}

func TestInternalPurposeChildRolesExcludeOperatorProfile(t *testing.T) {
	store := memmemory.New()
	cfg := Config{operatorProfileSource: store}
	for _, role := range []string{"guardrail-checker", "model-router", "parallel-judge", "ask-reviewer", "usermodel-review"} {
		if got := childOperatorProfileSource(cfg, role); got != nil {
			t.Errorf("internal role %q inherited operator profile", role)
		}
	}
}

func TestOperatorProfileSourceIsCallerScoped(t *testing.T) {
	store := memory.NewCallerStore(memmemory.New(), false)
	alice := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "issuer", Subject: "alice"})
	bob := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "issuer", Subject: "bob"})
	if err := store.RememberEntry(alice, tool.MemoryEntry{Key: "user/preference", Value: "alice-profile"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RememberEntry(bob, tool.MemoryEntry{Key: "user/preference", Value: "bob-profile"}); err != nil {
		t.Fatal(err)
	}

	var requests []port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { requests = append(requests, req) })}, mockllm.TextTurn("done"), mockllm.TextTurn("done"))
	eng := agent.NewEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), OperatorProfileSource: store})
	for i, ctx := range []context.Context{alice, bob} {
		sess := session.New(session.SessionID(fmt.Sprintf("profile-%d", i)), session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 2}, time.Unix(0, 0))
		for range eng.Run(ctx, sess, memEnvironment("/ws"), agent.RunRequest{Text: "hello"}).Events() {
		}
	}
	if len(requests) != 2 || !strings.Contains(requests[0].System.VolatileSuffix, "alice-profile") || strings.Contains(requests[0].System.VolatileSuffix, "bob-profile") ||
		!strings.Contains(requests[1].System.VolatileSuffix, "bob-profile") || strings.Contains(requests[1].System.VolatileSuffix, "alice-profile") {
		t.Fatalf("caller profiles crossed boundaries: %#v", requests)
	}
}

func runChildProfileTurn(t *testing.T, eng *agent.Engine, id, text string) {
	t.Helper()
	sess := session.New(session.SessionID(id), session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 2}, time.Unix(0, 0))
	run := eng.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: text})
	for range run.Events() {
	}
	for _, message := range sess.Conversation.Messages {
		if strings.Contains(message.Text, "operator-profile-data") {
			t.Fatalf("operator profile persisted in child history: %+v", message)
		}
	}
}
