package sessiondebug

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type countingLineageStore struct {
	*memstore.Store
	calls int
	err   error
}

func (s *countingLineageStore) ReadSessionLineage(ctx context.Context, query port.SessionLineageQuery) (port.SessionLineageResult, error) {
	s.calls++
	if s.err != nil {
		return port.SessionLineageResult{}, s.err
	}
	return s.Store.ReadSessionLineage(ctx, query)
}

func lineageFixture(t *testing.T) (*memstore.Store, *session.Session, *session.Session) {
	t.Helper()
	ctx := context.Background()
	root := session.New("root", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	child, err := session.NewSubagent("child", session.ModeDefault, root.EnvironmentRef, session.Limits{}, time.Unix(2, 0), root.ID, root.Incarnation(), "call")
	if err != nil {
		t.Fatal(err)
	}
	store := memstore.New()
	for _, current := range []*session.Session{root, child} {
		if err := store.Save(ctx, current); err != nil {
			t.Fatal(err)
		}
	}
	return store, root, child
}

func decodeLineageEvidence[T any](t *testing.T, got session.ToolResult) T {
	t.Helper()
	body := strings.TrimSuffix(strings.TrimPrefix(got.Content, governance.UntrustedFence+"\n"), "\n"+governance.UntrustedFence+"\n")
	var out T
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode evidence %q: %v", got.Content, err)
	}
	return out
}

func TestSessionDebuggerInspectSession_Scenario1_RootViewsAvoidLineageTraversal(t *testing.T) {
	base, root, _ := lineageFixture(t)
	store := &countingLineageStore{Store: base}
	inspect := New(root.ID, store, nil)

	for _, view := range []string{"status", "transcript", "activity", "performance", "network", "history", "manifest"} {
		t.Run(view, func(t *testing.T) {
			got := execute(t, inspect, `{"view":"`+view+`"}`)
			if got.IsError {
				t.Fatalf("%s view failed: %s", view, got.Content)
			}
			switch view {
			case "status":
				out := decodeLineageEvidence[statusEvidence](t, got)
				if out.Scope != rootScope || out.Target != root.ID || out.State != session.StateIdle || out.Kind != session.SessionKindMain || out.Lifetime.Available {
					t.Fatalf("status = %+v", out)
				}
			case "transcript":
				out := decodeLineageEvidence[transcriptEvidence](t, got)
				if !out.Authoritative || !out.Complete || !out.ScanComplete || out.Truncated || out.Total != 0 || len(out.Messages) != 0 {
					t.Fatalf("transcript = %+v", out)
				}
			case "activity":
				out := decodeLineageEvidence[activityEvidence](t, got)
				if out.Available || out.Authoritative || out.Complete || out.Error != errLogNotConfigured || len(out.Rows) != 0 {
					t.Fatalf("activity = %+v", out)
				}
			case "performance":
				out := decodeLineageEvidence[performanceEvidence](t, got)
				if out.Available || out.Authoritative || out.Complete || out.ScanComplete || out.Error != errLogNotConfigured || len(out.Turns) != 0 {
					t.Fatalf("performance = %+v", out)
				}
			case "network":
				out := decodeLineageEvidence[networkEvidence](t, got)
				if out.Available || out.Authoritative || out.Complete || out.ScanComplete || out.Error != errLogNotConfigured || len(out.Attempts) != 0 {
					t.Fatalf("network = %+v", out)
				}
			case "history":
				out := decodeLineageEvidence[historyCatalog](t, got)
				if !out.Authoritative || out.ScanComplete || out.RetentionComplete || !out.ProjectionComplete || out.Error != errLogNotConfigured || len(out.Sources) != 1 || out.Sources[0].Source != "current_snapshot" || !out.Sources[0].Authoritative || !out.Sources[0].ProjectionComplete {
					t.Fatalf("history = %+v", out)
				}
			case "manifest":
				out := decodeLineageEvidence[manifestEvidence](t, got)
				if out.Available || out.Authoritative || out.ScanComplete || out.RetentionComplete || !out.ProjectionComplete || out.Error != errLogNotConfigured || len(out.Rows) != 0 {
					t.Fatalf("manifest = %+v", out)
				}
			}
			if store.calls != 0 {
				t.Fatalf("%s view performed %d lineage reads, want 0", view, store.calls)
			}
		})
	}
}

func TestSessionDebuggerInspectSession_Scenario1_RootTokenTranscriptAvoidsLineageTraversal(t *testing.T) {
	base, root, _ := lineageFixture(t)
	store := &countingLineageStore{Store: base, err: errors.New("lineage must not be read")}
	inspect := New(root.ID, store, nil)

	got := execute(t, inspect, `{"view":"transcript","scope_handle":"root"}`)
	if got.IsError {
		t.Fatalf("root-token transcript failed: %s", got.Content)
	}
	out := decodeLineageEvidence[transcriptEvidence](t, got)
	if !out.Authoritative || !out.Complete || !out.ScanComplete || out.Truncated || out.Total != 0 || len(out.Messages) != 0 {
		t.Fatalf("transcript = %+v", out)
	}
	if store.calls != 0 {
		t.Fatalf("root-token transcript performed %d lineage reads, want 0", store.calls)
	}
}

func TestSessionDebuggerInspectSession_Scenario1_RelatedViewsRetainLineageScan(t *testing.T) {
	base, root, child := lineageFixture(t)
	unrelated := session.New("unrelated-secret", session.ModeDefault, root.EnvironmentRef, session.Limits{}, time.Unix(3, 0))
	if err := base.Save(context.Background(), unrelated); err != nil {
		t.Fatal(err)
	}
	log := memstore.NewEventLog()
	if err := log.Append(context.Background(), root.ID, session.Event{Type: session.EvSubagentStart, Subagent: &session.SubagentPayload{ParentCallID: "call", ChildID: string(child.ID), ChildIncarnation: child.Incarnation()}}); err != nil {
		t.Fatal(err)
	}
	store := &countingLineageStore{Store: base}
	inspect := New(root.ID, store, log)

	t.Run("related", func(t *testing.T) {
		got := execute(t, inspect, `{"view":"related"}`)
		if got.IsError {
			t.Fatalf("related view failed: %s", got.Content)
		}
		out := decodeLineageEvidence[relatedEvidence](t, got)
		if !out.Available || !out.Supported || !out.ScanComplete || !out.RetentionComplete || out.Truncated || len(out.Rows) != 1 || out.Rows[0].Handle == "" || out.Rows[0].Kind != session.SessionKindSubagent || out.Rows[0].Edge != "subagent" || out.Rows[0].Status != string(port.SessionLineageRetained) {
			t.Fatalf("related = %+v", out)
		}
		if store.calls == 0 || strings.Contains(got.Content, string(unrelated.ID)) {
			t.Fatalf("related did not retain the lineage boundary: calls=%d result=%s", store.calls, got.Content)
		}
	})

	t.Run("delegation", func(t *testing.T) {
		before := store.calls
		got := execute(t, inspect, `{"view":"delegation"}`)
		if got.IsError {
			t.Fatalf("delegation view failed: %s", got.Content)
		}
		out := decodeLineageEvidence[delegationEvidence](t, got)
		if !out.Authoritative || !out.ScanComplete || out.RetentionComplete || !out.ProjectionComplete || out.Error != "" || len(out.Rows) != 1 || out.Rows[0].Type != "subagent" || out.Rows[0].Event != session.EvSubagentStart || out.Rows[0].ScopeHandle == "" || out.Rows[0].Retention != string(port.SessionLineageRetained) || out.Rows[0].Conclusion != "absent" {
			t.Fatalf("delegation = %+v", out)
		}
		if store.calls <= before || strings.Contains(got.Content, string(unrelated.ID)) {
			t.Fatalf("delegation did not retain the lineage boundary: calls=%d result=%s", store.calls-before, got.Content)
		}
	})
}

func TestSessionDebuggerInspectSession_Scenario1_ScopeHandleRevalidatesLineage(t *testing.T) {
	base, root, child := lineageFixture(t)
	graph := New(root.ID, base, nil).(*inspectTool).scanLineage(context.Background(), root)
	if len(graph.Nodes) != 1 || graph.Nodes[0].Handle == "" {
		t.Fatalf("lineage graph = %+v", graph)
	}
	handle := graph.Nodes[0].Handle
	store := &countingLineageStore{Store: base}
	inspect := New(root.ID, store, nil)

	got := execute(t, inspect, `{"view":"status","scope_handle":"`+handle+`"}`)
	if got.IsError || store.calls == 0 {
		t.Fatalf("scoped status did not scan valid lineage: calls=%d result=%s", store.calls, got.Content)
	}
	if err := base.Delete(context.Background(), child.ID); err != nil {
		t.Fatal(err)
	}
	before := store.calls
	got = execute(t, inspect, `{"view":"status","scope_handle":"`+handle+`"}`)
	if !got.IsError || store.calls <= before {
		t.Fatalf("stale scope did not scan and fail closed: calls=%d result=%s", store.calls-before, got.Content)
	}
}

func TestSessionDebuggerInspectSession_Scenario1_DeterministicLineageTraversalBoundary(t *testing.T) {
	base, root, _ := lineageFixture(t)
	store := &countingLineageStore{Store: base, err: errors.New("lineage blocked")}
	inspect := New(root.ID, store, nil)

	if got := execute(t, inspect, `{"view":"status"}`); got.IsError {
		t.Fatalf("root status depended on lineage: %s", got.Content)
	}
	if store.calls != 0 {
		t.Fatalf("root status lineage calls = %d, want 0", store.calls)
	}
	related := execute(t, inspect, `{"view":"related"}`)
	if store.calls != 1 {
		t.Fatalf("related lineage calls = %d, want 1", store.calls)
	}
	out := decodeLineageEvidence[relatedEvidence](t, related)
	if out.ScanComplete || out.Error != "lineage index read failed" || out.RetentionComplete {
		t.Fatalf("related lineage failure = %+v", out)
	}
	delegation := execute(t, inspect, `{"view":"delegation"}`)
	if store.calls != 2 {
		t.Fatalf("delegation lineage calls = %d, want 2", store.calls)
	}
	delegationOut := decodeLineageEvidence[delegationEvidence](t, delegation)
	if delegationOut.Authoritative || delegationOut.ScanComplete || delegationOut.RetentionComplete || delegationOut.Error != errLogNotConfigured || len(delegationOut.Rows) != 0 {
		t.Fatalf("delegation unavailable result = %+v", delegationOut)
	}
}
