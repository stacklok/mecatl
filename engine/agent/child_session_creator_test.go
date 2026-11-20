package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

// childCreation is one CreateChild call a childCreatorStore saw.
type childCreation struct {
	id         session.SessionID
	kind       session.SessionKind
	parent     session.SessionID
	definition string
}

// childCreatorStore is a memstore that implements port.ChildSessionCreator. It
// records each call and, when refuse is set, refuses the child without
// publishing it.
type childCreatorStore struct {
	inner        *memstore.Store
	refuse       error
	refuseResume error

	mu      sync.Mutex
	created []childCreation
	resumed []childCreation
	creates int
}

func newChildCreatorStore() *childCreatorStore {
	return &childCreatorStore{inner: memstore.New()}
}

func (c *childCreatorStore) Save(ctx context.Context, s *session.Session) error {
	return c.inner.Save(ctx, s)
}

func (c *childCreatorStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return c.inner.Load(ctx, id)
}

func (c *childCreatorStore) Create(ctx context.Context, s *session.Session) error {
	c.mu.Lock()
	c.creates++
	c.mu.Unlock()
	return c.inner.Create(ctx, s)
}

func (c *childCreatorStore) CreateChild(ctx context.Context, s *session.Session, child port.ChildSessionInfo) error {
	c.mu.Lock()
	c.created = append(c.created, childCreation{id: s.ID, kind: s.Kind, parent: s.Relationship.ParentSessionID, definition: child.DefinitionName})
	refuse := c.refuse
	c.mu.Unlock()
	if refuse != nil {
		return refuse
	}
	return c.inner.Create(ctx, s)
}

func (c *childCreatorStore) ResumeChild(_ context.Context, s *session.Session, child port.ChildSessionInfo) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resumed = append(c.resumed, childCreation{id: s.ID, kind: s.Kind, parent: s.Relationship.ParentSessionID, definition: child.DefinitionName})
	return c.refuseResume
}

func (c *childCreatorStore) resumes() []childCreation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]childCreation(nil), c.resumed...)
}

func (c *childCreatorStore) children() []childCreation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]childCreation(nil), c.created...)
}

func (c *childCreatorStore) plainCreates() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.creates
}

var errChildRefused = errors.New("child refused by host")

func childCreatorParent(t *testing.T) *session.Session {
	t.Helper()
	parent := session.New("parent", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	alice := session.Principal{Issuer: "https://issuer.example", Subject: "alice"}
	if err := parent.RestoreLabels(&alice, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels(parent): %v", err)
	}
	return parent
}

// runParentCall runs a parent that makes one tool call with id p1 and returns
// that call's result.
func runParentCall(t *testing.T, tl tool.Tool, name, args string) *session.ToolResult {
	t.Helper()
	parentLLM := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("p1", name, json.RawMessage(args))),
		mockllm.TextTurn("parent done"),
	)
	e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, tl)})
	events := drain(e.Run(context.Background(), childCreatorParent(t), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
	res := resultByCallID(events)["p1"]
	if res == nil {
		t.Fatalf("%s call p1 has no result", name)
	}
	return res
}

func wantOneChild(t *testing.T, store *childCreatorStore, kind session.SessionKind, definition string) childCreation {
	t.Helper()
	got := store.children()
	if len(got) != 1 {
		t.Fatalf("CreateChild calls = %+v, want exactly one", got)
	}
	if got[0].kind != kind || got[0].definition != definition || got[0].parent != "parent" {
		t.Fatalf("CreateChild = %+v, want kind %q, definition %q, parent %q", got[0], kind, definition, "parent")
	}
	if n := store.plainCreates(); n != 0 {
		t.Fatalf("Create calls = %d, want 0: a child store with CreateChild must not also get Create", n)
	}
	return got[0]
}

func TestChildSessionCreatorPublishesEachChildKind(t *testing.T) {
	t.Run("default subagent", func(t *testing.T) {
		store := newChildCreatorStore()
		task := agent.NewSubagentTool(childEngineWith(mockllm.New(mockllm.TextTurn("child done")), catalogWith(t)), agent.WithSubagentStore(store))
		if res := runParentCall(t, task, "Subagent", `{"prompt":"investigate"}`); res.IsError {
			t.Fatalf("Subagent failed: %+v", res)
		}
		child := wantOneChild(t, store, session.SessionKindSubagent, "")
		if _, err := store.Load(context.Background(), child.id); err != nil {
			t.Fatalf("the child was not published: %v", err)
		}
	})
	t.Run("named subagent", func(t *testing.T) {
		store := newChildCreatorStore()
		reviewer := childEngineWith(mockllm.New(mockllm.TextTurn("reviewed")), catalogWith(t))
		task := agent.NewSubagentTool(childEngineWith(mockllm.New(), catalogWith(t)),
			agent.WithSubagentStore(store),
			agent.WithAgentEngines(map[string]*agent.Engine{"reviewer": reviewer}, []agent.AgentMeta{{Name: "reviewer"}}))
		if res := runParentCall(t, task, "Subagent", `{"prompt":"review","agent":"reviewer"}`); res.IsError {
			t.Fatalf("Subagent failed: %+v", res)
		}
		wantOneChild(t, store, session.SessionKindSubagent, "reviewer")
	})
	t.Run("background subagent", func(t *testing.T) {
		store := newChildCreatorStore()
		reviewer := childEngineWith(mockllm.New(mockllm.TextTurn("reviewed")), catalogWith(t))
		task := agent.NewSubagentTool(childEngineWith(mockllm.New(), catalogWith(t)),
			agent.WithSubagentStore(store),
			agent.WithAgentEngines(map[string]*agent.Engine{"reviewer": reviewer}, []agent.AgentMeta{{Name: "reviewer"}}))
		if res := runParentCall(t, task, "Subagent", `{"prompt":"review","agent":"reviewer","background":true}`); res.IsError {
			t.Fatalf("Subagent failed: %+v", res)
		}
		waitForChildren(t, store, 1)
		wantOneChild(t, store, session.SessionKindSubagent, "reviewer")
	})
	t.Run("parallel branch", func(t *testing.T) {
		store := newChildCreatorStore()
		parallel := agent.NewParallelTool(childEngineWith(&branchProvider{summary: "branch done"}, catalogWith(t)), &memForker{}, agent.WithParallelStore(store))
		if res := runParentCall(t, parallel, "Parallel", `{"tasks":["explore"]}`); res.IsError {
			t.Fatalf("Parallel failed: %+v", res)
		}
		wantOneChild(t, store, session.SessionKindParallelBranch, "")
	})
	// A member's definition is the one its factory built it from, not the
	// roster's agent type: a factory that falls back from an unknown type
	// builds a generic member.
	for name, tc := range map[string]struct{ built, want string }{
		"team member":                       {built: "reviewer", want: "reviewer"},
		"team member the factory fell back": {built: "", want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			store := newChildCreatorStore()
			providers := map[string]*mockllm.Provider{"lead": mockllm.New(mockllm.TextTurn("member done"))}
			base := teamToolFactory(t, providers)
			factory := func(tm *team.Team, spec agent.MemberSpec, model string) agent.MemberBuild {
				build := base(tm, spec, model)
				build.DefinitionName = tc.built
				return build
			}
			teamTool := agent.NewTeamTool(factory, agent.WithTeamToolStore(store))
			if res := runParentCall(t, teamTool, "Team", `{"goal":"fix it","members":[{"name":"lead","role":"lead","agent":"reviewer"}]}`); res.IsError {
				t.Fatalf("Team failed: %+v", res)
			}
			wantOneChild(t, store, session.SessionKindTeamMember, tc.want)
		})
	}
}

// TestChildSessionCreatorRefusalStopsTheChild pins that a CreateChild error
// stops each kind of child before its first turn: the child's model is never
// called, and the parent gets the creation failure.
func TestChildSessionCreatorRefusalStopsTheChild(t *testing.T) {
	t.Run("subagent", func(t *testing.T) {
		store := newChildCreatorStore()
		store.refuse = errChildRefused
		childLLM := mockllm.New(mockllm.TextTurn("must not run"))
		task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t)), agent.WithSubagentStore(store))
		res := runParentCall(t, task, "Subagent", `{"prompt":"investigate"}`)
		if !res.IsError || !strings.Contains(res.Content, errChildRefused.Error()) {
			t.Fatalf("Subagent result = %+v, want the refusal", res)
		}
		if childLLM.Calls() != 0 {
			t.Fatalf("refused child called its model %d times", childLLM.Calls())
		}
	})
	t.Run("background subagent", func(t *testing.T) {
		store := newChildCreatorStore()
		store.refuse = errChildRefused
		childLLM := mockllm.New(mockllm.TextTurn("must not run"))
		task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t)), agent.WithSubagentStore(store))
		// The parent waits for the background child to end before the check,
		// so a child that ran its model late would be caught.
		parentLLM := mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"investigate","background":true}`))),
			mockllm.ToolCallTurn(session.NewToolCall("wait", "SubagentStatus", json.RawMessage(`{"agent_id":"subagent-parent-p1","wait_ms":30000}`))),
			mockllm.TextTurn("parent done"),
		)
		e := newEngine(agent.Deps{LLM: parentLLM, Catalog: catalogWith(t, task, agent.NewSubagentStatusTool())})
		events := drain(e.Run(context.Background(), childCreatorParent(t), agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
		if res := resultByCallID(events)["wait"]; res == nil {
			t.Fatal("the parent never waited for the background child")
		}
		waitForChildren(t, store, 1)
		if childLLM.Calls() != 0 {
			t.Fatalf("refused background child called its model %d times", childLLM.Calls())
		}
	})
	t.Run("parallel branch", func(t *testing.T) {
		store := newChildCreatorStore()
		store.refuse = errChildRefused
		branch := mockllm.New(mockllm.TextTurn("must not run"))
		parallel := agent.NewParallelTool(childEngineWith(branch, catalogWith(t)), &memForker{}, agent.WithParallelStore(store))
		res := runParentCall(t, parallel, "Parallel", `{"tasks":["explore"]}`)
		if !strings.Contains(res.Content, errChildRefused.Error()) {
			t.Fatalf("Parallel result = %+v, want the refusal", res)
		}
		if branch.Calls() != 0 {
			t.Fatalf("refused branch called its model %d times", branch.Calls())
		}
	})
	t.Run("team member", func(t *testing.T) {
		store := newChildCreatorStore()
		store.refuse = errChildRefused
		memberLLM := mockllm.New(mockllm.TextTurn("must not run"))
		teamTool := agent.NewTeamTool(teamToolFactory(t, map[string]*mockllm.Provider{"lead": memberLLM}), agent.WithTeamToolStore(store))
		res := runParentCall(t, teamTool, "Team", `{"goal":"fix it","members":[{"name":"lead","role":"lead"}]}`)
		if !res.IsError || !strings.Contains(res.Content, errChildRefused.Error()) {
			t.Fatalf("Team result = %+v, want the refusal", res)
		}
		if memberLLM.Calls() != 0 {
			t.Fatalf("refused member called its model %d times", memberLLM.Calls())
		}
	})
}

func waitForChildren(t *testing.T, store *childCreatorStore, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(store.children()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("CreateChild calls = %d, want %d", len(store.children()), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestChildSessionResumerAdmitsAResumedSubagent pins that resuming a subagent,
// in the foreground or the background, calls ResumeChild with the default
// explorer's (empty) definition before the child runs again, and that a
// refusal stops the resume before the child's model is called.
func TestChildSessionResumerAdmitsAResumedSubagent(t *testing.T) {
	for _, background := range []bool{false, true} {
		for _, refuse := range []bool{false, true} {
			name := map[bool]string{false: "foreground", true: "background"}[background] + map[bool]string{false: "", true: " refused"}[refuse]
			t.Run(name, func(t *testing.T) {
				store := newChildCreatorStore()
				childLLM := mockllm.New(mockllm.TextTurn("seeded"), mockllm.TextTurn("resumed"))
				task := agent.NewSubagentTool(childEngineWith(childLLM, catalogWith(t)), agent.WithSubagentStore(store))
				seedLLM := mockllm.New(
					mockllm.ToolCallTurn(session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"seed"}`))),
					mockllm.TextTurn("parent done"),
				)
				seed := agent.NewEngine(agent.Deps{LLM: seedLLM, Catalog: catalogWith(t, task), Policy: allowAll(), Model: "parent-model"})
				seedSess := session.New("resume-parent", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
				if err := seedSess.RestoreLabels(resumeOwnerAlice, session.Authority{}); err != nil {
					t.Fatal(err)
				}
				drainObserving(t, seed.Run(context.Background(), seedSess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}), nil)
				childID := session.SessionID("subagent-resume-parent-p1")
				if len(store.resumes()) != 0 {
					t.Fatalf("a fresh child called ResumeChild: %+v", store.resumes())
				}
				if refuse {
					store.mu.Lock()
					store.refuseResume = errChildRefused
					store.mu.Unlock()
				}
				res := runResumeAttempt(t, task, childID, background, resumeOwnerAlice, background)
				resumes := store.resumes()
				if len(resumes) != 1 || resumes[0].id != childID || resumes[0].definition != "" {
					t.Fatalf("ResumeChild calls = %+v, want one for %s with no definition", resumes, childID)
				}
				if n := len(store.children()); n != 1 {
					t.Fatalf("the resume published the child again: %d CreateChild calls", n)
				}
				switch {
				case refuse && childLLM.Calls() != 1:
					t.Fatalf("a refused resume called the child's model: %d calls, want only the seed", childLLM.Calls())
				case refuse && !background && (!res.IsError || !strings.Contains(res.Content, errChildRefused.Error())):
					t.Fatalf("a refused resume returned %+v, want the refusal", res)
				case !refuse && childLLM.Calls() != 2:
					t.Fatalf("an admitted resume made %d child model calls, want 2", childLLM.Calls())
				}
			})
		}
	}
}
