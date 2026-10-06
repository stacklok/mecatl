package server

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/localauthority"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

type switchedDirectTool struct {
	lifecycleMarkerTool
	calls atomic.Int32
}

func (s *switchedDirectTool) Execute(ctx context.Context, call session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	s.calls.Add(1)
	return s.lifecycleMarkerTool.Execute(ctx, call, env)
}

func TestSessionBrokerConfigSwitchCannotReinterpretSavedAuthority(t *testing.T) {
	for _, kind := range []string{"pending", "no-pending", "genuine-direct"} {
		t.Run(kind, func(t *testing.T) {
			owner := &session.Principal{Issuer: "test", Subject: "owner", GrantType: session.GrantTypeUser}
			ctx := session.WithPrincipal(t.Context(), owner)
			store := memstore.New()
			name := "mcp__echo__echo"
			sess := session.New("switch", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
			sess.Owner = owner
			sess.ModelID = "persisted-model"
			if kind == "no-pending" {
				sess.ModelID = ""
			}
			if err := sess.BindAuthority(session.Authority{Provenance: "test", CapabilitySet: governance.CapabilitySet{Tools: []string{name}, FileSystem: true}}); err != nil {
				t.Fatal(err)
			}
			if kind != "genuine-direct" {
				if err := sess.AdoptBrokerCatalogue(session.BrokerSessionRef(hostProofRef(1)), session.BrokerCatalogueRef(hostProofRef(2)), time.Now().Add(time.Hour), []string{name}); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "pending" {
				call := session.NewToolCall("unresolved", name, []byte(`{}`))
				if err := sess.BeginTurn(); err != nil {
					t.Fatal(err)
				}
				if err := sess.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
					t.Fatal(err)
				}
				a, _ := sess.BrokerAccess()
				if err := sess.FenceBrokerInvocation(a.Session, a.Catalogue, call.ID, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Save(ctx, sess); err != nil {
				t.Fatal(err)
			}
			before, err := store.Load(ctx, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			direct := &switchedDirectTool{lifecycleMarkerTool: lifecycleMarkerTool{name: name}}
			cat := tool.NewCatalog()
			if err := cat.Register(direct); err != nil {
				t.Fatal(err)
			}
			engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("fresh", name, []byte(`{}`))), mockllm.TextTurn("done")), Catalog: cat, Store: store, AuthorityEvaluator: localauthority.New(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)})
			builds := 0
			svc, err := NewService(Config{Engine: engine, Store: store, OwnershipEnforced: true, PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test", SessionEngine: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
				builds++
				return SessionEngineResult{Engine: engine}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			if _, err := svc.LoadSession(ctx, sess.ID); kind != "genuine-direct" && !errors.Is(err, ErrFailedPrecondition) {
				t.Fatalf("load activated broker state: %v", err)
			}
			run, err := svc.StartRunContent(ctx, sess.ID, "execute", nil)
			if kind == "genuine-direct" {
				if err != nil {
					t.Fatal(err)
				}
				for range run.Events() {
				}
				svc.FinishRun(sess.ID, run)
				if direct.calls.Load() != 1 {
					t.Fatal("genuine direct tool did not execute")
				}
				return
			}
			if !errors.Is(err, ErrFailedPrecondition) {
				if run != nil {
					for range run.Events() {
					}
					svc.FinishRun(sess.ID, run)
				}
				t.Fatalf("run activated broker state: %v", err)
			}
			if builds != 0 || direct.calls.Load() != 0 {
				t.Fatal("direct composition activated")
			}
			after, err := store.Load(ctx, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejection mutated durable state")
			}
			foreign := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "test", Subject: "foreign"})
			if _, err := svc.LoadSession(foreign, sess.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("owner disclosure: %v", err)
			}
		})
	}
}
