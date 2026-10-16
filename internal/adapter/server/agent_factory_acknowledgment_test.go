package server

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

func TestAgentFactoryBindingAcknowledgment(t *testing.T) {
	for _, operation := range []string{"create", "direct-create", "fork", "clear", "rebuild"} {
		for _, result := range []string{"unaware", "wrong-name", "zero-authority", "no-ceiling", "exceeds-ceiling", "valid"} {
			t.Run(operation+"/"+result, func(t *testing.T) {
				store := memstore.New()
				svc := newAgentDefRetryService(t, store)
				ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "owner"})
				var source *session.Session
				var err error
				if operation != "create" && operation != "direct-create" {
					source, err = svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault, WithAgentDefinitionName("reviewer"))
					if err != nil {
						t.Fatal(err)
					}
				}
				var prior *sessionEngine
				if source != nil {
					prior = svc.sessionEngines[source.ID]
				}
				svc.cfg.NewID = func() session.SessionID { return "destination" }
				closed := 0
				factory := func(_ context.Context, _ ProviderSelector, _ SessionProfile, mode session.PermissionMode, limits session.Limits, name string) (SessionEngineResult, error) {
					res := agentDefinitionEngineResult(name)
					res.BuiltForMode, res.Limits = mode, &limits
					res.Close = func() error { closed++; return nil }
					switch result {
					case "unaware":
						res.AgentDefinitionName, res.Authority = "", session.Authority{}
					case "wrong-name":
						res.AgentDefinitionName = "other"
					case "zero-authority":
						res.Authority = session.Authority{}
					case "no-ceiling":
						res.Authority.Ceiling = nil
					case "exceeds-ceiling":
						res.Authority.CapabilitySet.Tools = []string{"Write"}
					case "valid":
						res.Authority.CapabilitySet.Tools = []string{"Read", "Write"}
						res.Authority.Ceiling.Tools = []string{"Read", "Write"}
					}
					return res, nil
				}
				svc.cfg.AgentDefSessionEngine = factory
				if operation != "direct-create" {
					svc.cfg.SessionContextEngine = func(ctx context.Context, _ session.SessionID, _ *session.Principal, _ ExecutionWorkspaceAcquirer, sel ProviderSelector, _ []mcp.ServerConfig, profile SessionProfile, _ string, mode session.PermissionMode, _ []tool.Tool, _ []string, _ session.SessionID) (SessionEngineResult, error) {
						if result == "unaware" {
							return factory(ctx, sel, profile, mode, session.Limits{}, "")
						}
						selection, ok := AgentDefSessionSelectionFromContext(ctx)
						if !ok || selection.Name != "reviewer" {
							t.Fatal("missing named selection")
						}
						return factory(ctx, sel, profile, mode, selection.Limits, selection.Name)
					}
				}
				var id session.SessionID
				switch operation {
				case "create", "direct-create":
					var created *session.Session
					created, err = svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, ProviderSelector{}, ProfileDefault, WithAgentDefinitionName("reviewer"))
					if created != nil {
						id = created.ID
					}
				case "fork":
					id, err = svc.ForkSessionSuccessor(ctx, ForkSuccessorRequest{Source: source.ID})
				case "clear":
					id, err = svc.ClearSessionSuccessor(ctx, source.ID, SuccessorPlacement{})
				case "rebuild":
					_, err = svc.buildAndRegisterSessionEngine(ctx, source, ProviderSelector{}, ProfileDefault, source.Mode, true)
					id = source.ID
				}
				if result != "valid" {
					if !errors.Is(err, ErrFailedPrecondition) || closed != 1 {
						t.Fatalf("rejected build: err=%v closes=%d", err, closed)
					}
					if _, err := store.Load(ctx, "destination"); !errors.Is(err, port.ErrSessionNotFound) {
						t.Fatalf("rejected build published: %v", err)
					}
					if _, ok := svc.sessionEngines["destination"]; ok {
						t.Fatal("rejected engine registered")
					}
					if source != nil && svc.sessionEngines[source.ID] != prior {
						t.Fatal("rejected build replaced source engine")
					}
				} else {
					if err != nil || closed != 0 {
						t.Fatalf("valid build: err=%v closes=%d", err, closed)
					}
					if source != nil {
						stored, err := store.Load(ctx, id)
						if err != nil || !reflect.DeepEqual(stored.Authority, source.Authority) {
							t.Fatalf("inherited authority replaced: %+v, %v", stored, err)
						}
					}
					svc.CloseSession(id)
					if closed != 1 {
						t.Fatalf("valid engine cleanup=%d", closed)
					}
				}
			})
		}
	}
}
