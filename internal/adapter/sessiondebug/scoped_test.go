package sessiondebug

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestHistoryHandlesBindRootAndScopeIncarnations(t *testing.T) {
	base := historyHandle("root", "child", "root-inc-1", "child-inc-1", "snapshot", 0)
	for _, changed := range []string{
		historyHandle("root", "child", "root-inc-2", "child-inc-1", "snapshot", 0),
		historyHandle("root", "child", "root-inc-1", "child-inc-2", "snapshot", 0),
	} {
		if handleEqual(base, changed) {
			t.Fatal("history handle did not bind both incarnations")
		}
	}
}

func TestScopedHandlesAreStableBoundAndRevalidated(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	owner := &session.Principal{Issuer: "issuer", Subject: "owner", GrantType: session.GrantTypeUser}
	root := session.New("root-sensitive-id", session.ModeDefault, "/ws", session.Limits{}, time.Now())
	root.Owner = owner.Clone()
	childCreated := time.Unix(1700000000, 0)
	child, err := session.NewSubagent("child-sensitive-id", session.ModeDefault, "/ws", session.Limits{}, childCreated, root.ID, root.Incarnation(), "call-1")
	if err != nil {
		t.Fatal(err)
	}
	child.Owner = owner.Clone()
	_ = child.SeedHistory([]session.Message{session.NewUserMessage("child transcript")})
	grandchild, err := session.NewTeamMember("nested-child-sensitive-id", session.ModeDefault, "/ws", session.Limits{}, time.Now(), "team-1", "reviewer", child.ID, child.Incarnation())
	if err != nil {
		t.Fatal(err)
	}
	grandchild.Owner = owner.Clone()
	if err := store.Save(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, child); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, grandchild); err != nil {
		t.Fatal(err)
	}
	ownedCtx := session.WithPrincipal(ctx, owner)
	tool1 := NewBound(root.ID, session.DebugTargetFingerprint(root), owner, true, store, nil).(*inspectTool)
	g := tool1.scanLineage(ctx, root)
	if len(g.Nodes) != 2 || !g.Nodes[0].Inspectable || !g.Nodes[1].Inspectable || g.Nodes[1].Depth != 2 {
		t.Fatalf("graph=%+v", g)
	}
	handle := g.Nodes[0].Handle
	tool2 := NewBound(root.ID, session.DebugTargetFingerprint(root), owner, true, store, nil).(*inspectTool)
	g2 := tool2.scanLineage(ctx, root)
	if !handleEqual(handle, g2.Nodes[0].Handle) {
		t.Fatalf("handle changed across restart")
	}
	got := executeAs(ownedCtx, t, tool2, `{"view":"transcript","scope_handle":"`+handle+`"}`)
	if got.IsError || !strings.Contains(got.Content, "child transcript") {
		t.Fatalf("scoped transcript=%s", got.Content)
	}
	subtree := executeAs(ownedCtx, t, tool2, `{"view":"related","scope_handle":"`+handle+`"}`)
	if subtree.IsError || strings.Count(subtree.Content, `"kind":"team_member"`) != 1 || strings.Contains(subtree.Content, `"kind":"subagent"`) {
		t.Fatalf("selected child related view leaked siblings or omitted subtree: %s", subtree.Content)
	}
	wrong := handle + "-wrong"
	if got := executeAs(ownedCtx, t, tool2, `{"view":"status","scope_handle":"`+wrong+`"}`); !got.IsError {
		t.Fatalf("wrong-target handle accepted: %s", got.Content)
	}
	child.Owner = &session.Principal{Issuer: owner.Issuer, Subject: owner.Subject, GrantType: session.GrantTypeClientCredentials, Name: "renamed"}
	if err := store.Save(ctx, child); err != nil {
		t.Fatal(err)
	}
	if got := executeAs(ownedCtx, t, tool2, `{"view":"status","scope_handle":"`+handle+`"}`); got.IsError {
		t.Fatalf("same owner identity with changed metadata was rejected: %s", got.Content)
	}
	child.Owner = &session.Principal{Issuer: "issuer", Subject: "replacement", GrantType: session.GrantTypeUser}
	if err := store.Save(ctx, child); err != nil {
		t.Fatal(err)
	}
	if got := executeAs(ownedCtx, t, tool2, `{"view":"status","scope_handle":"`+handle+`"}`); !got.IsError {
		t.Fatalf("owner-mutated child handle accepted: %s", got.Content)
	}
	child.Owner = owner.Clone()
	child.Relationship.CallID = "changed-call"
	if err := store.Save(ctx, child); err != nil {
		t.Fatal(err)
	}
	if got := executeAs(ownedCtx, t, tool2, `{"view":"status","scope_handle":"`+handle+`"}`); !got.IsError {
		t.Fatalf("relationship-mutated child handle accepted: %s", got.Content)
	}
	if err := store.Delete(ctx, child.ID); err != nil {
		t.Fatal(err)
	}
	if got := executeAs(ownedCtx, t, tool2, `{"view":"status","scope_handle":"`+handle+`"}`); !got.IsError {
		t.Fatalf("stale handle accepted: %s", got.Content)
	}
	replacement, err := session.NewSubagent(child.ID, session.ModeDefault, "/ws", session.Limits{}, childCreated, root.ID, root.Incarnation(), "call-1")
	if err != nil {
		t.Fatal(err)
	}
	replacement.Owner = owner.Clone()
	if replacement.Incarnation() == child.Incarnation() || replacement.CreatedAt != child.CreatedAt || !replacement.Owner.SameIdentity(child.Owner) {
		t.Fatal("replacement did not preserve identical ID/time/owner while changing only incarnation")
	}
	_ = replacement.SeedHistory([]session.Message{session.NewUserMessage("replacement evidence")})
	if err := store.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	got = executeAs(ownedCtx, t, tool2, `{"view":"transcript","scope_handle":"`+handle+`"}`)
	if !got.IsError || strings.Contains(got.Content, "replacement evidence") {
		t.Fatalf("same-lineage replacement accepted or exposed: %s", got.Content)
	}
}

type descendantReplacingStore struct {
	base             *memstore.Store
	descendant       session.SessionID
	replacement      *session.Session
	replaceAfterLoad int
	loads            int
	replaceErr       error
}

func (s *descendantReplacingStore) Save(ctx context.Context, current *session.Session) error {
	return s.base.Save(ctx, current)
}

func (s *descendantReplacingStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	current, err := s.base.Load(ctx, id)
	if err != nil || id != s.descendant {
		return current, err
	}
	s.loads++
	if s.loads == s.replaceAfterLoad {
		if err := s.base.Delete(ctx, id); err != nil {
			s.replaceErr = err
		} else {
			s.replaceErr = s.base.Create(ctx, s.replacement)
		}
	}
	return current, nil
}

func (s *descendantReplacingStore) ReadSessionLineage(ctx context.Context, query port.SessionLineageQuery) (port.SessionLineageResult, error) {
	return s.base.ReadSessionLineage(ctx, query)
}

func TestScopedViewFailsClosedWhenDescendantIsReplacedAfterResolution(t *testing.T) {
	ctx := context.Background()
	base := memstore.New()
	owner := &session.Principal{Issuer: "issuer", Subject: "owner", GrantType: session.GrantTypeUser}
	root := session.New("root", session.ModeDefault, "/ws", session.Limits{}, time.Unix(1, 0))
	root.Owner = owner.Clone()
	child, err := session.NewSubagent("child", session.ModeDefault, "/ws", session.Limits{}, time.Unix(2, 0), root.ID, root.Incarnation(), "call-1")
	if err != nil {
		t.Fatal(err)
	}
	child.Owner = owner.Clone()
	_ = child.SeedHistory([]session.Message{session.NewUserMessage("original evidence")})
	for _, current := range []*session.Session{root, child} {
		if err := base.Create(ctx, current); err != nil {
			t.Fatal(err)
		}
	}

	graph := New(root.ID, base, nil).(*inspectTool).scanLineage(ctx, root)
	if len(graph.Nodes) != 1 || graph.Nodes[0].Handle == "" {
		t.Fatalf("graph=%+v", graph)
	}
	handle := graph.Nodes[0].Handle
	replacement, err := session.NewSubagent(child.ID, session.ModeDefault, "/ws", session.Limits{}, child.CreatedAt, root.ID, root.Incarnation(), "call-1")
	if err != nil {
		t.Fatal(err)
	}
	replacement.Owner = owner.Clone()
	_ = replacement.SeedHistory([]session.Message{session.NewUserMessage("replacement evidence must not escape")})
	hooked := &descendantReplacingStore{base: base, descendant: child.ID, replacement: replacement, replaceAfterLoad: 2}

	got := execute(t, New(root.ID, hooked, nil), `{"view":"transcript","scope_handle":"`+handle+`"}`)
	if hooked.replaceErr != nil {
		t.Fatal(hooked.replaceErr)
	}
	if !got.IsError || !strings.Contains(got.Content, "stale or inaccessible") {
		t.Fatalf("replacement race result=%+v", got)
	}
	if strings.Contains(got.Content, "original evidence") || strings.Contains(got.Content, "replacement evidence") {
		t.Fatalf("replacement race exposed projected evidence: %s", got.Content)
	}
}

func TestRelatedNeverLeaksForeignIDsAndClassifiesLineage(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	root := session.New("root", session.ModeDefault, "", session.Limits{}, time.Now())
	root.Owner = &session.Principal{Issuer: "i", Subject: "a", GrantType: session.GrantTypeUser}
	foreign, _ := session.NewSubagent("foreign-raw-secret", session.ModeDefault, "", session.Limits{}, time.Now(), root.ID, root.Incarnation(), "call")
	foreign.Owner = &session.Principal{Issuer: "i", Subject: "b", GrantType: session.GrantTypeUser}
	pruned, _ := session.NewSubagent("pruned-raw-secret", session.ModeDefault, "", session.Limits{}, time.Now(), root.ID, root.Incarnation(), "call2")
	pruned.Owner = root.Owner.Clone()
	for _, s := range []*session.Session{root, foreign, pruned} {
		if err := store.Save(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Delete(ctx, pruned.ID); err != nil {
		t.Fatal(err)
	}
	inspect := NewBound(root.ID, session.DebugTargetFingerprint(root), root.Owner, true, store, nil)
	gotResult, err := inspect.Execute(session.WithPrincipal(ctx, root.Owner), session.NewToolCall("call", ToolName, []byte(`{"view":"related"}`)), tool.Environment{})
	if err != nil {
		t.Fatal(err)
	}
	got := gotResult
	for _, want := range []string{`"status":"inaccessible"`, `"status":"pruned"`} {
		if !strings.Contains(got.Content, want) {
			t.Fatalf("missing %s: %s", want, got.Content)
		}
	}
	if strings.Contains(got.Content, "foreign-raw-secret") || strings.Contains(got.Content, "pruned-raw-secret") {
		t.Fatalf("raw related id leaked: %s", got.Content)
	}
}

func TestDelegationHistoryManifestAndLifetimeEvidence(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	log := memstore.NewEventLog()
	root := session.New("root", session.ModeDefault, "", session.Limits{}, time.Now())
	root.Owner = &session.Principal{Issuer: "i", Subject: "a", GrantType: session.GrantTypeUser}
	call := session.NewToolCall("delegate-call", "Team", []byte(`{}`))
	messages := []session.Message{session.NewAssistantMessage("", "", []session.ToolCall{call}), session.NewToolMessage(session.NewToolResult(call.ID, "team synthesis"))}
	if err := root.SeedHistory(messages); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, root); err != nil {
		t.Fatal(err)
	}
	backgroundIncarnation := session.NewIncarnationID()
	parallelIncarnation := session.NewIncarnationID()
	events := []session.Event{
		{Type: session.EvSubagentStart, RunID: "run-1", Subagent: &session.SubagentPayload{ParentCallID: string(call.ID), ChildID: "background-child-secret", ChildIncarnation: backgroundIncarnation, Background: true}},
		{Type: session.EvParallelStart, RunID: "run-1", Parallel: &session.ParallelPayload{ParentCallID: string(call.ID), Join: "first", BranchCount: 2}},
		{Type: session.EvParallelBranch, RunID: "run-1", Parallel: &session.ParallelPayload{ParentCallID: string(call.ID), Kind: session.ParallelBranchStart, BranchIndex: 0, ChildID: "parallel-child-secret", ChildIncarnation: parallelIncarnation}},
		{Type: session.EvParallelEnd, RunID: "run-1", Parallel: &session.ParallelPayload{ParentCallID: string(call.ID), Join: "first", BranchCount: 2, Winner: 0}},
		{Type: session.EvScheduleSkipped, Schedule: &session.SchedulePayload{ScheduleName: "nightly", Kind: "skipped"}},
		{Type: session.EvTurnEnd, RunID: "run-1", Turn: 1, TurnEnd: &session.TurnEndPayload{Usage: session.Usage{InputTokens: 3, OutputTokens: 2}}},
		{Type: session.EvToolCall, RunID: "run-1", ToolCall: &call},
		{Type: session.EvToolResult, RunID: "run-1", ToolResult: ptr(session.NewToolError(call.ID, "failed once"))},
		{Type: session.EvTeamEnd, RunID: "run-1", Team: &session.TeamPayload{ParentCallID: string(call.ID), Stop: session.StopEndTurn, Tasks: []session.TeamTaskSnapshot{{ID: "task", Description: "work", State: "completed", Assignee: "lead"}}, Findings: []session.TeamFindingSnapshot{{Member: "lead", Body: "found"}}, Dispositions: []session.TeamMemberDisposition{{Name: "lead", Disposition: "done", ErrorRounds: 1}}}},
		{Type: session.EvCompactionArchive, RunID: "run-1", CompactionArchive: &session.CompactionArchivePayload{Replaced: messages}},
		{Type: session.EvRequestManifest, RunID: "run-1", Turn: 1, RequestManifest: &session.RequestManifestPayload{Provider: "mock", Model: "m", ReasoningEffort: "high", ContextWindow: 8192, ToolNames: []string{"Read", "Team"}, ToolDecisions: []session.RequestToolDecision{{Name: "Read", Source: session.RequestToolSourceCatalog, Decision: session.RequestToolAdvertised}}, MessageCount: 2, MessageBytes: 42, Prompt: []session.RequestPromptComponent{{Kind: session.RequestPromptSystem, Provenance: session.RequestProvenanceStable, Bytes: 7}}}},
		{Type: session.EvResult, RunID: "run-1", Result: &session.ResultPayload{Stop: session.StopEndTurn}},
	}
	for _, ev := range events {
		if err := log.Append(ctx, root.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	status := execute(t, New(root.ID, store, log), `{"view":"status"}`)
	for _, want := range []string{`"latest_run_counters"`, `"snapshot_cumulative_usage"`, `"runs":1`, `"turns":1`, `"tool_calls":1`, `"tool_failures":1`} {
		if !strings.Contains(status.Content, want) {
			t.Fatalf("status missing %s: %s", want, status.Content)
		}
	}
	delegation := execute(t, New(root.ID, store, log), `{"view":"delegation"}`)
	for _, want := range []string{`"parent_conclusion":"tool_result"`, `"description":"work"`, `"body":"found"`, `"error_rounds":1`, `"background":true`, `"collection":"not recorded durably"`, `"join":"first"`} {
		if !strings.Contains(delegation.Content, want) {
			t.Fatalf("delegation missing %s: %s", want, delegation.Content)
		}
	}
	if strings.Contains(delegation.Content, "background-child-secret") || strings.Contains(delegation.Content, "parallel-child-secret") {
		t.Fatalf("delegation leaked child id: %s", delegation.Content)
	}
	related := execute(t, New(root.ID, store, log), `{"view":"related"}`)
	for _, want := range []string{`"status":"not_retained"`, `"status":"never_produced"`, `"status":"absent"`} {
		if !strings.Contains(related.Content, want) {
			t.Fatalf("related missing %s: %s", want, related.Content)
		}
	}
	manifest := execute(t, New(root.ID, store, log), `{"view":"manifest"}`)
	if !strings.Contains(manifest.Content, `"tools":["Read","Team"]`) || !strings.Contains(manifest.Content, `"provider":"mock"`) || !strings.Contains(manifest.Content, `"context_window":8192`) || strings.Contains(manifest.Content, "digest") {
		t.Fatalf("manifest=%s", manifest.Content)
	}
	history := execute(t, New(root.ID, store, log), `{"view":"history"}`)
	if !strings.Contains(history.Content, "compaction_archive") || !strings.Contains(history.Content, "retained_event_history") {
		t.Fatalf("history=%s", history.Content)
	}
}
