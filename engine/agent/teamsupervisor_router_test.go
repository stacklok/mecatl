package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

// teamsupervisor_router_test.go drives the team-member routing seam (ADR 0034): the
// supervisor classifies each PLAIN UNDEFINED member ONCE at AddMember (decide-once,
// off InitialPrompt) and threads the routed model into the factory. It asserts the
// precedence (defined member skipped), fail-soft, the zero-caps no-route, and that the
// recorded MemberRouting carries no member content.

// routingMemberFactory builds a member factory that records the (spec.Name → routedModel)
// the supervisor passed it, so a test can prove the routed model reached composition. The
// member engine is a one-turn marker so Run completes cleanly.
func routingMemberFactory(tm *team.Team, recorded map[string]string, mu *sync.Mutex) MemberEngine {
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	return func(spec MemberSpec, routedModel string) MemberBuild {
		mu.Lock()
		recorded[spec.Name] = routedModel
		mu.Unlock()
		cat := tool.NewCatalog()
		for _, tl := range MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		// The engine's Model echoes the routed model when set, else "default", so the
		// member's own build reflects the decision.
		model := "default"
		if routedModel != "" {
			model = routedModel
		}
		return MemberBuild{Engine: NewEngine(Deps{
			LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: cat,
			Policy: allow, Hooks: noopHookRunner{}, Model: model,
		})}
	}
}

// An UNDEFINED member is routed at AddMember off its InitialPrompt; the factory receives
// the routed model and MemberRouting reads back the bare metadata.
func TestMemberRoutesAtAddMember(t *testing.T) {
	tm := team.New("t")
	recorded := map[string]string{}
	var mu sync.Mutex
	var seenPrompt string
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(_ context.Context, prompt string) (string, string, bool) {
		seenPrompt = prompt
		return "large", "big-model", true
	}}
	sup := NewSupervisor(tm, memfs.NewWorkspace("/ws"),
		routingMemberFactory(tm, recorded, &mu), withParentCaps(caps))
	if err := sup.AddMember(context.Background(),
		MemberSpec{Name: "worker", InitialPrompt: "redesign the storage layer"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	// The classifier saw the member's InitialPrompt (the routing artifact).
	if seenPrompt != "redesign the storage layer" {
		t.Fatalf("router classified %q, want the member's InitialPrompt", seenPrompt)
	}
	mu.Lock()
	if recorded["worker"] != "big-model" {
		t.Fatalf("factory received routedModel=%q, want big-model", recorded["worker"])
	}
	mu.Unlock()
	cat, model := sup.MemberRouting("worker")
	if cat != "large" || model != "big-model" {
		t.Fatalf("MemberRouting = (%q, %q), want (large, big-model)", cat, model)
	}
	// MemberModel (issue #112 / ADR 0035) reads back the concrete model the routed
	// member's engine ACTUALLY runs on — the routed model, == MemberRouting's model.
	if mm := sup.MemberModel("worker"); mm != "big-model" {
		t.Fatalf("MemberModel = %q, want big-model (the routed model)", mm)
	}
}

// A DEFINED member (spec.AgentType set) pins its own engine/model — the router must NOT
// fire, the factory receives no routed model, and MemberRouting is empty.
func TestDefinedMemberSkipsRouter(t *testing.T) {
	tm := team.New("t")
	recorded := map[string]string{}
	var mu sync.Mutex
	var calls int
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, bool) {
		calls++
		return "large", "big-model", true
	}}
	sup := NewSupervisor(tm, memfs.NewWorkspace("/ws"),
		routingMemberFactory(tm, recorded, &mu), withParentCaps(caps))
	if err := sup.AddMember(context.Background(),
		MemberSpec{Name: "specialist", AgentType: "reviewer", InitialPrompt: "review"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if calls != 0 {
		t.Fatalf("routeTask must NOT be consulted for a DEFINED member; called %d times", calls)
	}
	mu.Lock()
	if recorded["specialist"] != "" {
		t.Fatalf("a defined member's factory got routedModel=%q, want empty", recorded["specialist"])
	}
	mu.Unlock()
	if cat, model := sup.MemberRouting("specialist"); cat != "" || model != "" {
		t.Fatalf("MemberRouting for a defined member = (%q, %q), want empty", cat, model)
	}
	// MemberModel (issue #112) still surfaces the concrete model a DEFINED member's
	// engine runs on — the def's pinned model ("default" here), even though the router
	// never fired (routed fields empty).
	if mm := sup.MemberModel("specialist"); mm != "default" {
		t.Fatalf("MemberModel for a defined member = %q, want default (the def's model)", mm)
	}
}

// FAIL-SOFT: a router miss (ok=false) leaves the member on the default model and
// MemberRouting empty — AddMember still succeeds.
func TestMemberRouteMissInheritsDefault(t *testing.T) {
	tm := team.New("t")
	recorded := map[string]string{}
	var mu sync.Mutex
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, bool) {
		return "", "", false
	}}
	sup := NewSupervisor(tm, memfs.NewWorkspace("/ws"),
		routingMemberFactory(tm, recorded, &mu), withParentCaps(caps))
	if err := sup.AddMember(context.Background(),
		MemberSpec{Name: "worker", InitialPrompt: "do it"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	mu.Lock()
	if recorded["worker"] != "" {
		t.Fatalf("a router miss must pass an empty routedModel; got %q", recorded["worker"])
	}
	mu.Unlock()
	if cat, model := sup.MemberRouting("worker"); cat != "" || model != "" {
		t.Fatalf("MemberRouting after a miss = (%q, %q), want empty", cat, model)
	}
	// MemberModel (issue #112): a router miss inherits the default model, so MemberModel
	// reports the default ("default") even though the routed fields are empty.
	if mm := sup.MemberModel("worker"); mm != "default" {
		t.Fatalf("MemberModel after a miss = %q, want default (inherited)", mm)
	}
}

// ZERO-CAPS (the gRPC RunTeam path): no parent caps ⇒ routeTask nil ⇒ no routing, the
// member is built byte-identically to today.
func TestMemberZeroCapsNoRouting(t *testing.T) {
	tm := team.New("t")
	recorded := map[string]string{}
	var mu sync.Mutex
	// No withParentCaps: the supervisor's caps is the zero value (routeTask nil).
	sup := NewSupervisor(tm, memfs.NewWorkspace("/ws"), routingMemberFactory(tm, recorded, &mu))
	if err := sup.AddMember(context.Background(),
		MemberSpec{Name: "worker", InitialPrompt: "go"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	mu.Lock()
	if recorded["worker"] != "" {
		t.Fatalf("zero-caps must pass an empty routedModel; got %q", recorded["worker"])
	}
	mu.Unlock()
	if cat, model := sup.MemberRouting("worker"); cat != "" || model != "" {
		t.Fatalf("zero-caps MemberRouting = (%q, %q), want empty", cat, model)
	}
}

// An empty InitialPrompt falls back to the member NAME as the routing artifact (the
// classifier always has a signal).
func TestMemberRouteFallsBackToName(t *testing.T) {
	tm := team.New("t")
	recorded := map[string]string{}
	var mu sync.Mutex
	var seenPrompt string
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(_ context.Context, prompt string) (string, string, bool) {
		seenPrompt = prompt
		return "small", "tiny-model", true
	}}
	sup := NewSupervisor(tm, memfs.NewWorkspace("/ws"),
		routingMemberFactory(tm, recorded, &mu), withParentCaps(caps))
	if err := sup.AddMember(context.Background(), MemberSpec{Name: "scout"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if seenPrompt != "scout" {
		t.Fatalf("an empty InitialPrompt must classify off the member name; classified %q", seenPrompt)
	}
}

// scriptedRoutingFactory builds a MemberEngine that looks each member's provider up by
// name (so a multi-round flow can be scripted per member) AND records the routedModel the
// supervisor passed — so a test can prove BOTH that the run actually drove multiple rounds
// (each provider's Calls() > 1) AND that routing fired once per member.
func scriptedRoutingFactory(tm *team.Team, providers map[string]*mockllm.Provider, recorded map[string]string, mu *sync.Mutex) MemberEngine {
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	return func(spec MemberSpec, routedModel string) MemberBuild {
		mu.Lock()
		recorded[spec.Name] = routedModel
		mu.Unlock()
		prov := providers[spec.Name]
		cat := tool.NewCatalog()
		for _, tl := range MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return MemberBuild{Engine: NewEngine(Deps{
			LLM: prov, Catalog: cat, Policy: allow, Hooks: noopHookRunner{}, Model: "mock",
		})}
	}
}

// DECIDE-ONCE: a member is routed exactly once across the whole run — AddMember classifies
// once and Reopen across rounds never re-routes. This drives a genuine MULTI-ROUND lead→
// worker flow (lead delegates → worker completes → lead synthesises) so the members are
// actually Reopen'd across rounds, then asserts (a) each member's engine ran MORE THAN ONCE
// (proving the rounds happened — otherwise the once-per-member count would be vacuous), and
// (b) routeTask was still consulted EXACTLY ONCE per member. A regression that moved the
// route call into the per-round loop (or into driveOneTurn / Reopen) would push the count
// above 2 and FAIL — the mutation the QA finding requires.
func TestMemberRoutesOncePerRun(t *testing.T) {
	tm := team.New("t")

	// Lead: AddTask (round 0) → text (ends round 0) → synthesis turn = 3 LLM calls.
	addTask := session.NewToolCall("l1", "AddTask", json.RawMessage(`{"description":"do the work"}`))
	leadProv := mockllm.New(
		mockllm.ToolCallTurn(addTask),
		mockllm.TextTurn("delegated; waiting for the worker"),
		mockllm.TextTurn("CONSOLIDATED report"),
	)
	// Worker: CompleteTask + RecordFinding (round 1) → text (ends round 1) = 2 LLM calls.
	complete := session.NewToolCall("w1", "CompleteTask", json.RawMessage(`{"task_id":"task-1"}`))
	finding := session.NewToolCall("w2", "RecordFinding", json.RawMessage(`{"finding":"done"}`))
	workerProv := mockllm.New(
		mockllm.ToolCallTurn(complete, finding),
		mockllm.TextTurn("work complete"),
	)
	providers := map[string]*mockllm.Provider{"lead": leadProv, "worker": workerProv}

	recorded := map[string]string{}
	var mu sync.Mutex
	var calls int
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, bool) {
		mu.Lock()
		calls++
		mu.Unlock()
		return "large", "big-model", true
	}}
	sup := NewSupervisor(tm, memfs.NewWorkspace("/ws"),
		scriptedRoutingFactory(tm, providers, recorded, &mu), withParentCaps(caps), WithMaxRounds(10))
	for _, name := range []string{"lead", "worker"} {
		spec := MemberSpec{Name: name, InitialPrompt: "work " + name}
		if name == "lead" {
			spec.Lead = true
		}
		if err := sup.AddMember(context.Background(), spec); err != nil {
			t.Fatalf("AddMember(%s): %v", name, err)
		}
	}
	out := sup.Run(context.Background(), nil)

	// PRECONDITION: the run actually drove the members across MULTIPLE rounds. Without this
	// the once-per-member assertion proves nothing (a single-turn run could route once and
	// still pass even if routing lived in the round loop). The lead ran 3 turns (delegate +
	// idle + synthesis) and the worker 2 (work + end) — both > 1.
	if out.Rounds < 2 {
		t.Fatalf("test precondition: want a MULTI-ROUND run (so Reopen across rounds is exercised); got rounds=%d", out.Rounds)
	}
	if got := leadProv.Calls(); got <= 1 {
		t.Fatalf("lead engine ran %d times, want > 1 (the multi-round drive must actually exercise Reopen)", got)
	}
	if got := workerProv.Calls(); got <= 1 {
		t.Fatalf("worker engine ran %d times, want > 1 (the multi-round drive must actually exercise Reopen)", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("routeTask consulted %d times across the multi-round run, want exactly 2 (once per member at AddMember; Reopen across rounds never re-routes)", calls)
	}
}

// MemberSessionID is stable across rounds and independent of routing — a routed member's
// stored session id is unchanged. (The id is namespaced by team id, derived once at
// AddMember; routing changes only the engine's model, never the id scheme.)
func TestMemberSessionIDUnaffectedByRouting(t *testing.T) {
	tm := team.New("t")
	recorded := map[string]string{}
	var mu sync.Mutex
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, bool) {
		return "large", "big-model", true
	}}
	sup := NewSupervisor(tm, memfs.NewWorkspace("/ws"),
		routingMemberFactory(tm, recorded, &mu), withParentCaps(caps),
		WithMemberSessionPrefix("team-xyz"))
	if err := sup.AddMember(context.Background(),
		MemberSpec{Name: "worker", InitialPrompt: "go"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	m := sup.members["worker"]
	want := session.SessionID("team-xyz-worker")
	if m.sess.ID != want {
		t.Fatalf("member session id = %q, want %q (the prefix scheme, unaffected by routing)", m.sess.ID, want)
	}
}

// GAUNTLET #7 (team, behavioral — the analogue of TestParallelRoutedEventsNoContentLeak):
// the ROUTING addition must carry only bare category/model metadata, never the classified
// content. A routed member's role briefing (= its InitialPrompt, the text the router
// classifies on) carries a unique SECRET. The test drives the REAL Team tool with an emit
// and asserts BOTH:
//
//	(a) the routed RoutedCategory/RoutedModel metadata DOES land on the EvTeamStart roster
//	    entry for the routed member — so the assertion is not vacuous; AND
//	(b) the secret NEVER appears in the ROUTED FIELDS (RoutedCategory/RoutedModel) on ANY
//	    team.* event — the routing path emits only the bare category/model, never the
//	    classified role/InitialPrompt content.
//
// Scope note: the test asserts the secret stays out of the ROUTING-introduced fields, NOT
// out of the whole payload — the EvTeamStart roster legitimately carries the member's
// clamped Role label (the pre-existing teamRoster projection, ADR 0014), which is not the
// gauntlet-#7 surface this slice added. The classifier here returns a CLEAN category/model;
// a buggy router that echoed the classified text into the model id would trip (b).
func TestTeamRoutedMetadataNoContentLeak(t *testing.T) {
	const secret = "SECRET-MEMBER-ROLE-CONTENT"

	// The member engine factory: each member runs a one-turn benign engine; an UNDEFINED
	// member is routed (the supervisor passes routedModel). The Team tool's factory shape is
	// func(*team.Team, MemberSpec, routedModel string) MemberBuild.
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	factory := func(tm *team.Team, spec MemberSpec, _ string) MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return MemberBuild{Engine: NewEngine(Deps{
			LLM: mockllm.New(mockllm.TextTurn("benign output")), Catalog: cat,
			Policy: allow, Hooks: noopHookRunner{}, Model: "mock",
		})}
	}
	teamTool := NewTeamTool(TeamMemberEngineFactory(factory))

	var (
		mu  sync.Mutex
		evs []session.Event
	)
	emit := func(ev session.Event) { mu.Lock(); evs = append(evs, ev); mu.Unlock() }
	caps := parentCaps{children: newChildRunRegistry(), routeTask: func(context.Context, string) (string, string, bool) {
		return "large", "big-model", true
	}}

	// The member's role briefing (→ InitialPrompt, the routing artifact) carries the secret.
	args := `{"goal":"do work","members":[{"name":"lead","role":"` + secret + ` coordinate"}]}`
	if _, err := teamTool.(childCapableTool).ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Team", json.RawMessage(args)),
		memfs.NewWorkspace("/ws"), emit, caps); err != nil {
		t.Fatalf("transport error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// (a) NON-VACUOUS: the routed metadata landed on the EvTeamStart roster.
	var sawRoutedRoster bool
	for _, ev := range evs {
		if ev.Type != session.EvTeamStart || ev.Team == nil {
			continue
		}
		for _, r := range ev.Team.Roster {
			if r.RoutedCategory == "large" && r.RoutedModel == "big-model" {
				sawRoutedRoster = true
			}
		}
	}
	if !sawRoutedRoster {
		t.Fatal("EvTeamStart roster carried no routed metadata; the routing assertion is vacuous")
	}

	// (b) NO LEAK INTO THE ROUTED FIELDS: the secret (the classified role/InitialPrompt
	// content) appears in NEITHER RoutedCategory NOR RoutedModel on ANY team.* event — the
	// routing path emits only the bare category/model. (The roster's clamped Role label
	// legitimately carries it; that is the pre-existing teamRoster projection, not this
	// slice's surface.)
	for _, ev := range evs {
		if ev.Team == nil {
			continue
		}
		for _, r := range ev.Team.Roster {
			if strings.Contains(r.RoutedCategory, secret) || strings.Contains(r.RoutedModel, secret) {
				t.Fatalf("a team.* event (%s) leaked the classified role content into a routed field: cat=%q model=%q",
					ev.Type, r.RoutedCategory, r.RoutedModel)
			}
		}
	}
}
