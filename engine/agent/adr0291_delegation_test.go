package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestADR_0291_DelegationSchemasCannotSelectPlacement(t *testing.T) {
	t.Parallel()
	privateRoot := "/private/placement/root"
	ref := session.EnvironmentRef{Kind: "remote", ID: "placement-7", Revision: "revision-3"}
	env := tool.MustEnvironment(ref, memfs.NewWorkspace(privateRoot), memledger.New(), nil)
	child, err := newSubagentSessionInEnvironment("subagent-1", session.ModeDefault, env, session.Limits{}, time.Unix(1, 0), "parent", session.NewIncarnationID(), "call")
	if err != nil {
		t.Fatal(err)
	}
	if child.EnvironmentRef != ref {
		t.Fatalf("child ref = %+v, want exact parent/fork ref %+v", child.EnvironmentRef, ref)
	}
	for name, schema := range map[string]json.RawMessage{"Subagent": subagentSchema, "Parallel": parallelSchema, "Team": teamSchema} {
		var doc struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(schema, &doc); err != nil {
			t.Fatalf("%s schema: %v", name, err)
		}
		for _, forbidden := range []string{"workspace", "path", "selector", "placement_id", "environment_ref"} {
			if _, ok := doc.Properties[forbidden]; ok {
				t.Errorf("%s model-facing schema accepts forbidden placement argument %q", name, forbidden)
			}
		}
	}
}

func TestADR_0291_DelegationObservabilityContainsNoPlacementPath(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(session.ParallelPayload{})
	for _, forbidden := range []string{"Workspace", "WinnerWorkspace", "EnvironmentRef", "Selector"} {
		if _, ok := typ.FieldByName(forbidden); ok {
			t.Errorf("ParallelPayload still publicly projects %s", forbidden)
		}
	}
	privateRoot := "/private/fork/root"
	result := joinFirstResult([]branchResult{{
		index: 0, label: "branch-1", childRoot: privateRoot,
		artifact: "artifact-opaque", childID: "parallel-call-0", summary: "done",
	}}, 0, false)
	if strings.Contains(result, privateRoot) {
		t.Fatalf("delegation result leaked physical root: %q", result)
	}
	if !strings.Contains(result, "artifact-opaque") {
		t.Fatalf("delegation result omitted opaque artifact handle: %q", result)
	}
}
