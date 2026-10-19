package server

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/session"
)

// explicitAutoFactory models an operator default of "high": an omitted selector
// resolves to "high", while an explicit "auto" resolves to the empty effective
// value that the session persists. It records the selectors it was handed.
func explicitAutoFactory(seen *[]string) func(context.Context, ProviderSelector, SessionProfile, session.PermissionMode, session.Limits, string) (SessionEngineResult, error) {
	return func(_ context.Context, sel ProviderSelector, _ SessionProfile, _ session.PermissionMode, _ session.Limits, _ string) (SessionEngineResult, error) {
		*seen = append(*seen, sel.ReasoningEffort)
		res := agentDefinitionEngineResult("reviewer")
		res.Limits = &session.Limits{MaxTurns: 10}
		res.ProviderID, res.ModelID = "provider", "model"
		switch sel.ReasoningEffort {
		case "":
			res.ReasoningEffort = "high"
		case "auto":
			res.ReasoningEffort = ""
		default:
			res.ReasoningEffort = sel.ReasoningEffort
		}
		return res, nil
	}
}

func TestAgentBoundExplicitAutoEffortSurvivesRuntimeRebuild(t *testing.T) {
	store := memstore.New()
	svc := newAgentDefRetryService(t, store)
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "owner"})
	revision := uint64(1)
	svc.cfg.OperationRevision = func(context.Context) (uint64, bool) { return revision, true }
	var seen []string
	build := explicitAutoFactory(&seen)
	svc.cfg.AgentDefSessionEngine = func(c context.Context, sel ProviderSelector, p SessionProfile, m session.PermissionMode, l session.Limits, n string) (SessionEngineResult, error) {
		res, err := build(c, sel, p, m, l, n)
		res.RuntimeRevision = revision
		return res, err
	}
	sess, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, ProviderSelector{ReasoningEffort: "auto"}, ProfileDefault, WithAgentDefinitionName("reviewer"))
	if err != nil {
		t.Fatal(err)
	}
	if sess.ReasoningEffort != "" {
		t.Fatalf("created effective effort = %q, want the empty effective value for explicit auto", sess.ReasoningEffort)
	}
	revision = 2
	if _, _, _, err := svc.engineAndEnvironmentFor(ctx, sess); err != nil {
		t.Fatalf("compatible runtime rebuild rejected an unchanged effort: %v", err)
	}
	if got := seen[len(seen)-1]; got != "auto" {
		t.Fatalf("rebuild selector effort = %q, want explicit auto (empty would inherit the operator default)", got)
	}
	if sess.ReasoningEffort != "" {
		t.Fatalf("rebuild changed persisted effort to %q", sess.ReasoningEffort)
	}
}

func TestAgentBoundSuccessorsKeepExplicitAutoEffort(t *testing.T) {
	for _, clear := range []bool{false, true} {
		name := "fork"
		if clear {
			name = "clear"
		}
		t.Run(name, func(t *testing.T) {
			ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "owner"})
			store := memstore.New()
			var seen []string
			creator := newAgentDefRetryService(t, store)
			creator.cfg.AgentDefSessionEngine = explicitAutoFactory(&seen)
			source, err := creator.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, ProviderSelector{ReasoningEffort: "auto"}, ProfileDefault, WithSessionID("source"), WithAgentDefinitionName("reviewer"))
			if err != nil {
				t.Fatal(err)
			}
			creator.Close()

			svc := newAgentDefRetryService(t, store)
			svc.cfg.NewID = func() session.SessionID { return "successor" }
			seen = nil
			svc.cfg.AgentDefSessionEngine = explicitAutoFactory(&seen)
			var id session.SessionID
			if clear {
				id, err = svc.ClearSessionSuccessor(ctx, source.ID, SuccessorPlacement{})
			} else {
				id, err = svc.ForkSessionSuccessor(ctx, ForkSuccessorRequest{Source: source.ID})
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(seen) != 1 || seen[0] != "auto" {
				t.Fatalf("successor factory selectors = %q, want one explicit auto", seen)
			}
			successor, err := store.Load(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if successor.ReasoningEffort != "" {
				t.Fatalf("successor adopted effort %q, want the source's explicit auto", successor.ReasoningEffort)
			}
		})
	}
}

func TestSuccessorSelectorKeepsExplicitOverrideOverAuto(t *testing.T) {
	source := session.New("s", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	source.AgentDefinitionName = "reviewer"
	got, err := successorProviderSelector(source, ForkSuccessorRequest{ReasoningEffort: "low"})
	if err != nil || got.ReasoningEffort != "low" {
		t.Fatalf("explicit successor override = %+v, %v", got, err)
	}
	got, err = successorProviderSelector(source, ForkSuccessorRequest{})
	if err != nil || got.ReasoningEffort != "auto" {
		t.Fatalf("inherited agent-bound empty effort = %+v, %v; want explicit auto", got, err)
	}
	unbound := session.New("u", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	got, err = successorProviderSelector(unbound, ForkSuccessorRequest{})
	if err != nil || got.ReasoningEffort != "" {
		t.Fatalf("unbound session selector = %+v, %v; its semantics must not change", got, err)
	}
}
