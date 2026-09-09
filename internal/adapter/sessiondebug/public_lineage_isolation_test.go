package sessiondebug_test

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/sessiondebug"
)

type observedStore struct {
	*memstore.Store
	loads        []session.SessionID
	queries      []port.SessionLineageQuery
	missingEdges map[string]bool
}

func (s *observedStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	s.loads = append(s.loads, id)
	return s.Store.Load(ctx, id)
}

func (s *observedStore) ReadSessionLineage(ctx context.Context, query port.SessionLineageQuery) (port.SessionLineageResult, error) {
	s.queries = append(s.queries, query)
	key := string(query.RootID) + "\x00" + string(query.RootIncarnation) + "\x00" + string(query.RecordID) + "\x00" + string(query.RecordIncarnation)
	if s.missingEdges[key] {
		return port.SessionLineageResult{}, nil
	}
	return s.Store.ReadSessionLineage(ctx, query)
}

type observedLog struct {
	*memstore.EventLog
	reads []session.SessionID
}

func (l *observedLog) Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	l.reads = append(l.reads, id)
	return l.EventLog.Read(ctx, id)
}

func publicFixture(t *testing.T) (*observedStore, *session.Session, *session.Session, *session.Session) {
	t.Helper()
	base := memstore.New()
	env := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "v1"}
	root := session.New("root", session.ModeDefault, env, session.Limits{}, time.Unix(1, 0))
	child, err := session.NewSubagent("child", session.ModeDefault, env, session.Limits{}, time.Unix(2, 0), root.ID, root.Incarnation(), "call-child")
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := session.NewTeamMember("grandchild", session.ModeDefault, env, session.Limits{}, time.Unix(3, 0), "team", "worker", child.ID, child.Incarnation())
	if err != nil {
		t.Fatal(err)
	}
	owner := &session.Principal{Issuer: "issuer", Subject: "owner", GrantType: session.GrantTypeUser}
	for _, current := range []*session.Session{root, child, grandchild} {
		current.Owner = owner.Clone()
		if err := base.Save(t.Context(), current); err != nil {
			t.Fatal(err)
		}
	}
	return &observedStore{Store: base}, root, child, grandchild
}

func inspect(t *testing.T, inspector tool.Tool, args string) session.ToolResult {
	t.Helper()
	result, err := inspector.Execute(t.Context(), session.NewToolCall("inspect", sessiondebug.ToolName, []byte(args)), tool.Environment{})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func evidence(t *testing.T, result session.ToolResult) map[string]any {
	t.Helper()
	body := strings.TrimSuffix(strings.TrimPrefix(result.Content, governance.UntrustedFence+"\n"), "\n"+governance.UntrustedFence+"\n")
	var value map[string]any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		t.Fatalf("decode %q: %v", result.Content, err)
	}
	return value
}

func firstHandle(t *testing.T, result session.ToolResult) string {
	t.Helper()
	rows, ok := evidence(t, result)["rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("rows = %#v", evidence(t, result)["rows"])
	}
	handle, _ := rows[0].(map[string]any)["scope_handle"].(string)
	if !strings.HasPrefix(handle, "v2.") {
		t.Fatalf("scope handle = %q", handle)
	}
	return handle
}

func TestInspectSessionLineageIsolation_Scenario1_RootReadsArePureAndPartitioned(t *testing.T) {
	store, root, child, _ := publicFixture(t)
	log := &observedLog{EventLog: memstore.NewEventLog()}
	if err := log.Append(t.Context(), root.ID, session.Event{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{ParentCallID: "call-child", ChildID: string(child.ID), ChildIncarnation: child.Incarnation()}}); err != nil {
		t.Fatal(err)
	}
	inspector := sessiondebug.New(root.ID, store, log)
	store.loads, store.queries = nil, nil

	related := inspect(t, inspector, `{"view":"related"}`)
	handle := firstHandle(t, related)
	if strings.Contains(related.Content, "grandchild") || len(store.queries) != 2 {
		t.Fatalf("root related traversed beyond its direct partition: queries=%+v evidence=%s", store.queries, related.Content)
	}
	for _, query := range store.queries {
		if query.RootID != root.ID || query.Limit != port.MaxSessionLineageRecords {
			t.Fatalf("root related query = %+v", query)
		}
	}
	if len(store.loads) != 2 || store.loads[0] != root.ID || store.loads[1] != root.ID {
		t.Fatalf("root related loaded child snapshots: %v", store.loads)
	}
	store.loads, store.queries = nil, nil
	rootStatus := inspect(t, inspector, `{"view":"status","scope_handle":"root"}`)
	if rootStatus.IsError || len(store.queries) != 0 {
		t.Fatalf("literal root scope traversed lineage: queries=%+v result=%+v", store.queries, rootStatus)
	}
	store.loads, store.queries = nil, nil
	log.reads = nil
	delegation := inspect(t, inspector, `{"view":"delegation"}`)
	if !strings.Contains(delegation.Content, handle) || len(store.queries) != 2 || len(log.reads) != 1 || log.reads[0] != root.ID {
		t.Fatalf("root delegation query path: queries=%+v event_reads=%v evidence=%s", store.queries, log.reads, delegation.Content)
	}
}

func exactEdgeKey(parent *session.Session, child *session.Session) string {
	return string(parent.ID) + "\x00" + string(parent.Incarnation()) + "\x00" + string(child.ID) + "\x00" + string(child.Incarnation())
}

func TestInspectSessionLineageIsolation_Scenario1_TargetedWriteAndCrashRecovery(t *testing.T) {
	store, root, child, _ := publicFixture(t)
	if err := child.RecordUserPrompt("CHILD-CONTENT-MUST-NOT-PROJECT", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	inspector := sessiondebug.New(root.ID, store, nil)
	handle := firstHandle(t, inspect(t, inspector, `{"view":"related"}`))
	store.missingEdges = map[string]bool{exactEdgeKey(root, child): true}

	result := inspect(t, inspector, `{"view":"transcript","scope_handle":"`+handle+`"}`)
	if !result.IsError || strings.Contains(result.Content, "CHILD-CONTENT-MUST-NOT-PROJECT") {
		t.Fatalf("missing root edge projected child content: %+v", result)
	}
}

func TestInspectSessionLineageIsolation_Scenario1_HandleBackwardProof(t *testing.T) {
	store, root, child, grandchild := publicFixture(t)
	inspector := sessiondebug.New(root.ID, store, nil)
	childHandle := firstHandle(t, inspect(t, inspector, `{"view":"related"}`))
	store.queries = nil
	grandchildHandle := firstHandle(t, inspect(t, inspector, `{"view":"related","scope_handle":"`+childHandle+`"}`))
	if len(store.queries) != 6 ||
		store.queries[0].RootID != child.ID || store.queries[0].RecordID != "" || store.queries[0].Limit != 1 ||
		store.queries[1].RootID != root.ID || store.queries[1].RecordID != child.ID || store.queries[1].Limit != 1 ||
		store.queries[2].RootID != child.ID || store.queries[2].RecordID != "" || store.queries[2].Limit != port.MaxSessionLineageRecords ||
		store.queries[3].RootID != child.ID || store.queries[3].RecordID != "" || store.queries[3].Limit != port.MaxSessionLineageRecords ||
		store.queries[4].RootID != child.ID || store.queries[4].RecordID != "" || store.queries[4].Limit != 1 ||
		store.queries[5].RootID != root.ID || store.queries[5].RecordID != child.ID || store.queries[5].Limit != 1 {
		t.Fatalf("scoped direct query path = %+v", store.queries)
	}
	store.queries = nil
	result := inspect(t, inspector, `{"view":"status","scope_handle":"`+grandchildHandle+`"}`)
	if result.IsError {
		t.Fatalf("deep status: %s", result.Content)
	}
	wantRoots := []session.SessionID{grandchild.ID, child.ID, child.ID, root.ID, grandchild.ID, child.ID, child.ID, root.ID}
	wantRecords := []session.SessionID{"", grandchild.ID, "", child.ID, "", grandchild.ID, "", child.ID}
	if len(store.queries) != len(wantRoots) {
		t.Fatalf("deep proof queries = %+v", store.queries)
	}
	for i, query := range store.queries {
		if query.RootID != wantRoots[i] || query.RecordID != wantRecords[i] || query.Limit != 1 {
			t.Fatalf("deep proof query[%d] = %+v, want root %s record %s", i, query, wantRoots[i], wantRecords[i])
		}
	}
}

func TestInspectSessionLineageIsolation_Scenario1_LegacyHandleMigration(t *testing.T) {
	store, root, _, _ := publicFixture(t)
	inspector := sessiondebug.New(root.ID, store, nil)
	for _, args := range []string{`{"view":"status"}`, `{"view":"status","scope_handle":"root"}`} {
		store.queries = nil
		if result := inspect(t, inspector, args); result.IsError || len(store.queries) != 0 {
			t.Fatalf("root-compatible scope %s changed behavior: queries=%+v result=%+v", args, store.queries, result)
		}
	}
	for _, handle := range []string{"legacy-v1-handle", "v1.deadbeef", "v2.not-base64!"} {
		store.loads, store.queries = nil, nil
		result := inspect(t, inspector, `{"view":"status","scope_handle":"`+handle+`"}`)
		if !result.IsError || !strings.Contains(result.Content, "refresh related evidence") || len(store.loads) != 0 || len(store.queries) != 0 {
			t.Fatalf("handle %q traversed storage or lacked refresh failure: loads=%v queries=%v result=%+v", handle, store.loads, store.queries, result)
		}
	}
	handle := firstHandle(t, inspect(t, inspector, `{"view":"related"}`))
	last := byte('A')
	if handle[len(handle)-1] == last {
		last = 'B'
	}
	tampered := handle[:len(handle)-1] + string(last)
	store.loads, store.queries = nil, nil
	result := inspect(t, inspector, `{"view":"status","scope_handle":"`+tampered+`"}`)
	if !result.IsError || len(store.loads) != 0 || len(store.queries) != 0 {
		t.Fatalf("tampered handle traversed storage: loads=%v queries=%v result=%+v", store.loads, store.queries, result)
	}
	childID := session.SessionID("child")
	if err := store.Delete(t.Context(), childID); err != nil {
		t.Fatal(err)
	}
	store.loads, store.queries = nil, nil
	result = inspect(t, inspector, `{"view":"status","scope_handle":"`+handle+`"}`)
	if !result.IsError || !strings.Contains(result.Content, "stale or inaccessible") {
		t.Fatalf("stale handle was accepted: loads=%v queries=%v result=%+v", store.loads, store.queries, result)
	}
}

func TestInspectSessionLineageIsolation_BackwardProofDepthBoundary(t *testing.T) {
	store := &observedStore{Store: memstore.New()}
	env := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "v1"}
	owner := &session.Principal{Issuer: "issuer", Subject: "owner", GrantType: session.GrantTypeUser}
	root := session.New("depth-root", session.ModeDefault, env, session.Limits{}, time.Unix(1, 0))
	root.Owner = owner.Clone()
	if err := store.Save(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	parent := root
	for depth := 1; depth <= 9; depth++ {
		child, err := session.NewSubagent(session.SessionID("depth-"+string(rune('0'+depth))), session.ModeDefault, env, session.Limits{}, time.Unix(int64(depth+1), 0), parent.ID, parent.Incarnation(), session.ToolCallID("call-"+string(rune('0'+depth))))
		if err != nil {
			t.Fatal(err)
		}
		child.Owner = owner.Clone()
		if err := store.Save(t.Context(), child); err != nil {
			t.Fatal(err)
		}
		parent = child
	}

	inspector := sessiondebug.New(root.ID, store, nil)
	handle := ""
	for depth := 1; depth <= 9; depth++ {
		args := `{"view":"related"}`
		if handle != "" {
			args = `{"view":"related","scope_handle":"` + handle + `"}`
		}
		handle = firstHandle(t, inspect(t, inspector, args))
		status := inspect(t, inspector, `{"view":"status","scope_handle":"`+handle+`"}`)
		if depth <= 8 && status.IsError {
			t.Fatalf("depth %d was rejected: %s", depth, status.Content)
		}
		if depth == 9 && (!status.IsError || !strings.Contains(status.Content, "exceeds the maximum depth")) {
			t.Fatalf("ninth hop was not rejected by the backward-proof bound: %+v", status)
		}
	}
}

func TestInspectSessionLineageIsolation_ParallelAndTeamEvidenceJoinExactLifetimes(t *testing.T) {
	store, root, _, _ := publicFixture(t)
	parallel, err := session.NewParallelBranch("parallel-child", session.ModeDefault, root.EnvironmentRef, session.Limits{}, time.Unix(4, 0), root.ID, root.Incarnation(), "parallel-call", 2)
	if err != nil {
		t.Fatal(err)
	}
	member, err := session.NewTeamMember("team-child", session.ModeDefault, root.EnvironmentRef, session.Limits{}, time.Unix(5, 0), "team-7", "reviewer", root.ID, root.Incarnation())
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range []*session.Session{parallel, member} {
		child.Owner = root.Owner.Clone()
		if err := store.Save(t.Context(), child); err != nil {
			t.Fatal(err)
		}
	}
	log := memstore.NewEventLog()
	events := []session.Event{
		{Type: session.EvParallelBranch, Parallel: &session.ParallelPayload{ParentCallID: "parallel-call", Kind: session.ParallelBranchStart, BranchIndex: 2, ChildID: string(parallel.ID), ChildIncarnation: session.NewIncarnationID(), BranchLabel: "branch-3"}},
		{Type: session.EvParallelBranch, Parallel: &session.ParallelPayload{ParentCallID: "parallel-call", Kind: session.ParallelBranchStart, BranchIndex: 1, ChildID: string(parallel.ID), ChildIncarnation: parallel.Incarnation(), BranchLabel: "branch-2"}},
		{Type: session.EvTeamMember, Team: &session.TeamPayload{ParentCallID: "team-call", TeamID: "wrong-team", Member: "reviewer", MemberSessionID: string(member.ID), MemberIncarnation: member.Incarnation(), InnerKind: session.EvTurnEnd}},
		{Type: session.EvTeamMember, Team: &session.TeamPayload{ParentCallID: "team-call", TeamID: "team-7", Member: "reviewer", MemberSessionID: string(member.ID), MemberIncarnation: session.NewIncarnationID(), InnerKind: session.EvTurnEnd}},
		{Type: session.EvParallelBranch, Parallel: &session.ParallelPayload{ParentCallID: "parallel-call", Kind: session.ParallelBranchStart, BranchIndex: 2, ChildID: string(parallel.ID), ChildIncarnation: parallel.Incarnation(), BranchLabel: "branch-3"}},
		{Type: session.EvTeamMember, Team: &session.TeamPayload{ParentCallID: "team-call", TeamID: "team-7", Member: "reviewer", MemberSessionID: string(member.ID), MemberIncarnation: member.Incarnation(), InnerKind: session.EvTurnEnd, Tasks: []session.TeamTaskSnapshot{{ID: "task-1", State: "working", Assignee: "reviewer"}}, Findings: []session.TeamFindingSnapshot{{Member: "reviewer", Body: "evidence"}}}},
	}
	for _, ev := range events {
		if err := log.Append(t.Context(), root.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	result := inspect(t, sessiondebug.New(root.ID, store, log), `{"view":"delegation"}`)
	body := evidence(t, result)
	rows, ok := body["rows"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("joined rows = %#v", body["rows"])
	}
	parallelRow := rows[0].(map[string]any)
	teamRow := rows[1].(map[string]any)
	if parallelRow["type"] != "parallel" || parallelRow["call_id"] != "parallel-call" || parallelRow["branch_index"] != float64(2) || !strings.HasPrefix(parallelRow["scope_handle"].(string), "v2.") {
		t.Fatalf("parallel join omitted exact branch metadata: %#v", parallelRow)
	}
	if teamRow["type"] != "team" || teamRow["member"] != "reviewer" || !strings.HasPrefix(teamRow["scope_handle"].(string), "v2.") || len(teamRow["tasks"].([]any)) != 1 || len(teamRow["findings"].([]any)) != 1 {
		t.Fatalf("team join omitted exact team metadata: %#v", teamRow)
	}
}
