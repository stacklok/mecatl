package sessiondebug_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
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

type failedEventLog struct{}

func (failedEventLog) Append(context.Context, session.SessionID, session.Event) error {
	return errors.New("injected append failure")
}

func (failedEventLog) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(func(session.Event, error) bool) {}
}

type readFailedEventLog struct{}

func (readFailedEventLog) Append(context.Context, session.SessionID, session.Event) error { return nil }
func (readFailedEventLog) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		yield(session.Event{}, errors.New("injected read failure"))
	}
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

func stampTeamCall(t *testing.T, member *session.Session, callID session.ToolCallID) {
	t.Helper()
	rel := member.Relationship
	rel.CallID = callID
	if err := member.RestoreSessionMetadata(session.SessionKindTeamMember, rel); err != nil {
		t.Fatalf("stamp team call: %v", err)
	}
}

func recordCompletedToolCall(t *testing.T, root *session.Session, callID, toolName string) {
	t.Helper()
	if err := root.RecordUserPrompt("run tool", nil); err != nil {
		t.Fatal(err)
	}
	if err := root.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := root.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{session.NewToolCall(session.ToolCallID(callID), toolName, nil)})); err != nil {
		t.Fatal(err)
	}
	if err := root.RecordToolResults([]session.ToolResult{session.NewToolResult(session.ToolCallID(callID), "done")}); err != nil {
		t.Fatal(err)
	}
	if err := root.Complete(); err != nil {
		t.Fatal(err)
	}
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
	index := len("v2.")
	replacement := byte('A')
	if handle[index] == replacement {
		replacement = 'B'
	}
	tampered := handle[:index] + string(replacement) + handle[index+1:]
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

func TestADR_0352_Scenario6_WireAndDebugger(t *testing.T) {
	store := memstore.New()
	env := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "v1"}
	root := session.New("routing-root", session.ModeDefault, env, session.Limits{}, time.Unix(10, 0))
	root.Owner = (&session.Principal{Issuer: "issuer", Subject: "owner", GrantType: session.GrantTypeUser}).Clone()
	if err := root.RecordUserPrompt("form team", nil); err != nil {
		t.Fatal(err)
	}
	if err := root.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := root.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{
		session.NewToolCall("team-call", "Team", nil),
		session.NewToolCall("team-other", "Team", nil),
	})); err != nil {
		t.Fatal(err)
	}
	if err := root.RecordToolResults([]session.ToolResult{
		session.NewToolResult("team-call", "done"),
		session.NewToolResult("team-other", "done"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := root.Complete(); err != nil {
		t.Fatal(err)
	}
	sub, err := session.NewSubagent("routing-sub", session.ModeDefault, env, session.Limits{}, time.Unix(11, 0), root.ID, root.Incarnation(), "sub-call")
	if err != nil {
		t.Fatal(err)
	}
	parallel, err := session.NewParallelBranch("routing-parallel", session.ModeDefault, env, session.Limits{}, time.Unix(12, 0), root.ID, root.Incarnation(), "parallel-call", 1)
	if err != nil {
		t.Fatal(err)
	}
	member, err := session.NewTeamMember("routing-team-member", session.ModeDefault, env, session.Limits{}, time.Unix(13, 0), "team-1", "reviewer", root.ID, root.Incarnation())
	if err != nil {
		t.Fatal(err)
	}
	stampTeamCall(t, member, "team-call")
	for _, current := range []*session.Session{root, sub, parallel, member} {
		current.Owner = root.Owner.Clone()
		if err := store.Save(t.Context(), current); err != nil {
			t.Fatal(err)
		}
	}
	zero := 0.0
	decision := &session.RoutingDecision{Backend: "jev", ClassifierModel: "jev-1.13.0\u202e", CandidateCategory: "deep\u200b", CandidateModel: "capable", Confidence: &zero, MinimumConfidence: &zero, Outcome: "routed", MissLimit: 3}
	log := memstore.NewEventLog()
	for _, ev := range []session.Event{
		{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{ParentCallID: "sub-call", ChildID: string(sub.ID), ChildIncarnation: sub.Incarnation(), Model: "capable", RoutedCategory: "deep", RoutedModel: "capable", RoutingDecision: decision}},
		{Type: session.EvParallelBranch, Parallel: &session.ParallelPayload{ParentCallID: "parallel-call", Kind: session.ParallelBranchStart, BranchIndex: 1, ChildID: string(parallel.ID), ChildIncarnation: parallel.Incarnation(), Model: "capable", RoutedCategory: "deep", RoutedModel: "capable", RoutingDecision: decision}},
		{Type: session.EvTeamStart, Team: &session.TeamPayload{ParentCallID: "team-call", TeamID: "team-1", Roster: []session.TeamMemberSpec{{Name: "reviewer", Model: "capable", RoutedCategory: "deep", RoutedModel: "capable", RoutingDecision: decision, MemberSessionID: member.ID, MemberIncarnation: member.Incarnation()}}}},
	} {
		if err := log.Append(t.Context(), root.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	result := inspect(t, sessiondebug.New(root.ID, store, log), `{"view":"delegation"}`)
	body := evidence(t, result)
	if strings.Contains(result.Content, string(member.ID)) || strings.Contains(result.Content, string(member.Incarnation())) || strings.Contains(result.Content, "member_session_id") || strings.Contains(result.Content, "member_incarnation") {
		t.Fatalf("private team lifetime leaked through debugger JSON: %s", result.Content)
	}
	rows, ok := body["rows"].([]any)
	if !ok || len(rows) != 3 {
		t.Fatalf("routing rows = %#v", body["rows"])
	}
	for _, raw := range rows {
		row := raw.(map[string]any)
		if row["actual_model"] != "capable" || row["routed_category"] != "deep" || row["routed_model"] != "capable" {
			t.Fatalf("final routing facts missing: %#v", row)
		}
		rd, ok := row["routing_decision"].(map[string]any)
		if !ok || rd["backend"] != "jev" || rd["classifier_model"] != "jev-1.13.0" || rd["candidate_category"] != "deep" || rd["confidence"] != float64(0) || rd["minimum_confidence"] != float64(0) {
			t.Fatalf("decision missing optional zero: %#v", row)
		}
	}

	historicalLog := memstore.NewEventLog()
	if err := historicalLog.Append(t.Context(), root.ID, session.Event{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{ParentCallID: "sub-call", ChildID: string(sub.ID), ChildIncarnation: sub.Incarnation()}}); err != nil {
		t.Fatal(err)
	}
	if err := historicalLog.Append(t.Context(), root.ID, session.Event{Type: session.EvTeamStart, Team: &session.TeamPayload{
		ParentCallID: "team-call", TeamID: "team-1", Roster: []session.TeamMemberSpec{{Name: "reviewer"}},
	}}); err != nil {
		t.Fatal(err)
	}
	historical := inspect(t, sessiondebug.New(root.ID, store, historicalLog), `{"view":"delegation"}`)
	if strings.Contains(historical.Content, "routing_decision") {
		t.Fatalf("historical event fabricated routing evidence: %s", historical.Content)
	}
	if got := len(evidence(t, historical)["rows"].([]any)); got != 1 {
		t.Fatalf("historical roster without exact identity joined by tuple: %s", historical.Content)
	}

	legacyMember, err := session.NewTeamMember("legacy-team-member", session.ModeDefault, env, session.Limits{}, time.Unix(13, 0), "team-1", "legacy", root.ID, root.Incarnation())
	if err != nil {
		t.Fatal(err)
	}
	legacyMember.Owner = root.Owner.Clone()
	if err := store.Save(t.Context(), legacyMember); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(t.Context(), legacyMember.ID); err != nil {
		t.Fatalf("historical member without call id did not load: %v", err)
	}
	legacyLog := memstore.NewEventLog()
	if err := legacyLog.Append(t.Context(), root.ID, session.Event{Type: session.EvTeamStart, Team: &session.TeamPayload{
		ParentCallID: "team-call", TeamID: "team-1", Roster: []session.TeamMemberSpec{{Name: "legacy", MemberSessionID: legacyMember.ID, MemberIncarnation: legacyMember.Incarnation()}},
	}}); err != nil {
		t.Fatal(err)
	}
	if got := len(evidence(t, inspect(t, sessiondebug.New(root.ID, store, legacyLog), `{"view":"delegation"}`))["rows"].([]any)); got != 0 {
		t.Fatalf("historical relationship without call id correlated: %d rows", got)
	}

	wrongCallLog := memstore.NewEventLog()
	if err := wrongCallLog.Append(t.Context(), root.ID, session.Event{Type: session.EvTeamStart, Team: &session.TeamPayload{
		ParentCallID: "team-other", TeamID: "team-1", Roster: []session.TeamMemberSpec{{Name: "reviewer", MemberSessionID: member.ID, MemberIncarnation: member.Incarnation()}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := wrongCallLog.Append(t.Context(), root.ID, session.Event{Type: session.EvTeamMember, Team: &session.TeamPayload{
		ParentCallID: "team-other", TeamID: "team-1", Member: "reviewer", MemberSessionID: string(member.ID), MemberIncarnation: member.Incarnation(), InnerKind: session.EvTurnEnd,
	}}); err != nil {
		t.Fatal(err)
	}
	if got := len(evidence(t, inspect(t, sessiondebug.New(root.ID, store, wrongCallLog), `{"view":"delegation"}`))["rows"].([]any)); got != 0 {
		t.Fatalf("team roster joined without the same parent Team call: %d rows", got)
	}
	unavailable := inspect(t, sessiondebug.New(root.ID, store, nil), `{"view":"delegation"}`)
	if !strings.Contains(unavailable.Content, "event log is not configured") || strings.Contains(unavailable.Content, "routing_decision") {
		t.Fatalf("unavailable log changed debugger semantics or fabricated evidence: %s", unavailable.Content)
	}
	failed := failedEventLog{}
	if err := failed.Append(t.Context(), root.ID, session.Event{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{RoutingDecision: decision}}); err == nil {
		t.Fatal("injected append unexpectedly succeeded")
	}
	failedEvidence := inspect(t, sessiondebug.New(root.ID, store, failed), `{"view":"delegation"}`)
	if strings.Contains(failedEvidence.Content, "routing_decision") || !strings.Contains(failedEvidence.Content, `"rows":[]`) {
		t.Fatalf("failed append was inferred or fabricated: %s", failedEvidence.Content)
	}
	readFailure := inspect(t, sessiondebug.New(root.ID, store, readFailedEventLog{}), `{"view":"delegation"}`)
	if strings.Contains(readFailure.Content, "routing_decision") || !strings.Contains(readFailure.Content, `"rows":[]`) || !strings.Contains(readFailure.Content, "event log read failed") {
		t.Fatalf("failed log history was inferred or fabricated: %s", readFailure.Content)
	}

	// A second retained incarnation with the same team/member labels cannot capture
	// the roster row because the exact trusted lifetime still identifies the original.
	ambiguous, err := session.NewTeamMember("routing-team-member-2", session.ModeDefault, env, session.Limits{}, time.Unix(14, 0), "team-1", "reviewer", root.ID, root.Incarnation())
	if err != nil {
		t.Fatal(err)
	}
	ambiguous.Owner = root.Owner.Clone()
	if err := store.Save(t.Context(), ambiguous); err != nil {
		t.Fatal(err)
	}
	result = inspect(t, sessiondebug.New(root.ID, store, log), `{"view":"delegation"}`)
	rows = evidence(t, result)["rows"].([]any)
	if len(rows) != 3 {
		t.Fatalf("same-label replacement displaced the exact team lifetime: %#v", rows)
	}
	if err := store.Delete(t.Context(), member.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(t.Context(), ambiguous.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := session.NewTeamMember(member.ID, session.ModeDefault, env, session.Limits{}, time.Unix(15, 0), "team-1", "reviewer", root.ID, root.Incarnation())
	if err != nil {
		t.Fatal(err)
	}
	replacement.Owner = root.Owner.Clone()
	if err := store.Save(t.Context(), replacement); err != nil {
		t.Fatal(err)
	}
	rows = evidence(t, inspect(t, sessiondebug.New(root.ID, store, log), `{"view":"delegation"}`))["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("deleted lifetime rebound to same-id replacement incarnation: %#v", rows)
	}
	foreign, err := session.NewTeamMember("routing-team-member-foreign", session.ModeDefault, env, session.Limits{}, time.Unix(16, 0), "team-1", "reviewer", root.ID, root.Incarnation())
	if err != nil {
		t.Fatal(err)
	}
	foreign.Owner = &session.Principal{Issuer: "issuer", Subject: "other", GrantType: session.GrantTypeUser}
	if err := store.Save(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	rows = evidence(t, inspect(t, sessiondebug.New(root.ID, store, log), `{"view":"delegation"}`))["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("stale/cross-owner team lineage did not fail closed: %#v", rows)
	}
}

func TestADR_0352_Scenario6_TwoRealTeamCallsCannotSubstituteCorrelation(t *testing.T) {
	store := memstore.New()
	providers := map[string]*mockllm.Provider{
		"lead": mockllm.New(
			mockllm.TextTurn("working A"), mockllm.TextTurn("report A"),
			mockllm.TextTurn("working B"), mockllm.TextTurn("report B"),
		),
	}
	factory := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		catalog := tool.NewCatalog()
		for _, memberTool := range agent.MemberTools(tm, spec.Name, nil) {
			catalog.MustRegister(memberTool)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: providers[spec.Name], Catalog: catalog,
			Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "member-model",
		})}
	}
	teamTool := agent.NewTeamTool(factory,
		agent.WithTeamToolStore(store),
		agent.WithTeamToolReadLedgerFactory(func() tool.ReadLedger { return memledger.New() }),
	)
	catalog := tool.NewCatalog()
	catalog.MustRegister(teamTool)
	parent := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("team-a", "Team", json.RawMessage(`{"goal":"A","members":[{"name":"lead","role":"lead A"}]}`))),
			mockllm.ToolCallTurn(session.NewToolCall("team-b", "Team", json.RawMessage(`{"goal":"B","members":[{"name":"lead","role":"lead B"}]}`))),
			mockllm.TextTurn("done"),
		),
		Catalog: catalog, Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "parent-model",
	})
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}
	env := tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), memledger.New(), nil)
	root := session.New("two-teams", session.ModeDefault, ref, session.Limits{}, time.Unix(20, 0))
	root.Owner = &session.Principal{Issuer: "issuer", Subject: "owner", GrantType: session.GrantTypeUser}

	var starts = map[string]session.Event{}
	var memberA session.Event
	for ev := range parent.Run(t.Context(), root, env, agent.RunRequest{Text: "run A then B"}).Events() {
		if ev.Type == session.EvTeamStart && ev.Team != nil {
			starts[ev.Team.ParentCallID] = ev
		}
		if ev.Type == session.EvTeamMember && ev.Team != nil && ev.Team.ParentCallID == "team-a" && memberA.Team == nil {
			memberA = ev
		}
	}
	if starts["team-a"].Team == nil || starts["team-b"].Team == nil || memberA.Team == nil {
		t.Fatalf("real Team events missing: starts=%v memberA=%+v", starts, memberA.Team)
	}
	if err := store.Save(t.Context(), root); err != nil {
		t.Fatal(err)
	}

	genuine := memstore.NewEventLog()
	if err := genuine.Append(t.Context(), root.ID, starts["team-a"]); err != nil {
		t.Fatal(err)
	}
	if err := genuine.Append(t.Context(), root.ID, starts["team-b"]); err != nil {
		t.Fatal(err)
	}
	rows := evidence(t, inspect(t, sessiondebug.New(root.ID, store, genuine), `{"view":"delegation"}`))["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("genuine Team calls did not both correlate: %#v", rows)
	}

	substitutedStart := starts["team-a"]
	substitutedStart.Team = cloneTeamPayloadForTest(starts["team-a"].Team)
	substitutedStart.Team.ParentCallID = "team-b"
	substitutedMember := memberA
	substitutedMember.Team = cloneTeamPayloadForTest(memberA.Team)
	substitutedMember.Team.ParentCallID = "team-b"
	for name, ev := range map[string]session.Event{"roster": substitutedStart, "member": substitutedMember} {
		t.Run(name, func(t *testing.T) {
			log := memstore.NewEventLog()
			if err := log.Append(t.Context(), root.ID, ev); err != nil {
				t.Fatal(err)
			}
			got := evidence(t, inspect(t, sessiondebug.New(root.ID, store, log), `{"view":"delegation"}`))["rows"].([]any)
			if len(got) != 0 {
				t.Fatalf("call B captured call A %s evidence: %#v", name, got)
			}
		})
	}
}

func cloneTeamPayloadForTest(in *session.TeamPayload) *session.TeamPayload {
	out := *in
	out.Roster = append([]session.TeamMemberSpec(nil), in.Roster...)
	return &out
}

func TestInspectSessionLineageIsolation_ParallelAndTeamEvidenceJoinExactLifetimes(t *testing.T) {
	store, root, _, _ := publicFixture(t)
	recordCompletedToolCall(t, root, "team-call", "Team")
	if err := store.Save(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	parallel, err := session.NewParallelBranch("parallel-child", session.ModeDefault, root.EnvironmentRef, session.Limits{}, time.Unix(4, 0), root.ID, root.Incarnation(), "parallel-call", 2)
	if err != nil {
		t.Fatal(err)
	}
	member, err := session.NewTeamMember("team-child", session.ModeDefault, root.EnvironmentRef, session.Limits{}, time.Unix(5, 0), "team-7", "reviewer", root.ID, root.Incarnation())
	if err != nil {
		t.Fatal(err)
	}
	stampTeamCall(t, member, "team-call")
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
