package agent

import (
	"strings"
	"testing"
)

func TestDelegationSpecsTeachSelectionBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		desc   string
		wants  []string
		avoids []string
	}{
		{
			name: "subagent",
			desc: (&SubagentTool{}).Spec().Description,
			wants: []string{
				"Delegate ONE focused, self-contained task",
				"one Subagent call per task in the SAME assistant turn",
				"eligible calls execute concurrently and return separate results",
				"unless a later task depends on an earlier result",
				"Serial execution applies only to a mode:\"read-write\" call",
			},
			avoids: []string{"It runs serially"},
		},
		{
			name: "subagent no-fs",
			desc: (&SubagentTool{noFSSpec: true}).Spec().Description,
			wants: []string{
				"Delegate ONE focused, self-contained task",
				"one Subagent call per task in the SAME assistant turn",
				"eligible calls execute concurrently and return separate results",
				"unless a later task depends on an earlier result",
			},
		},
		{
			name: "parallel",
			desc: (&ParallelTool{}).Spec().Description,
			wants: []string{
				"managed fork-join group for one or more isolated writable or competing branches",
				"built-in 'all', 'first', or 'judge'/'best' result selection",
				"Do NOT use Parallel merely for independent READ-ONLY investigations",
				"one Subagent call per task in the SAME assistant turn",
				"For a direct-write single task, prefer Subagent with mode:\"read-write\"",
				"one-branch Parallel call remains appropriate",
				"files cannot later be inspected, copied, or merged",
				"include any needed patch or details in its summary",
			},
			avoids: []string{
				"copy any changes you need into your reply before the call returns",
				"Parallel is for running 2+",
			},
		},
		{
			name: "team",
			desc: (&TeamTool{}).Spec().Description,
			wants: []string{
				"stateful, multi-round team",
				"coordinate through a shared task list and mailbox",
				"lead must synthesize the final report",
				"independent result-only fan-out",
				"one read-only Subagent call per task in the SAME assistant turn",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, want := range tt.wants {
				if !strings.Contains(tt.desc, want) {
					t.Errorf("description missing %q\ngot=%q", want, tt.desc)
				}
			}
			for _, avoid := range tt.avoids {
				if strings.Contains(tt.desc, avoid) {
					t.Errorf("description retains incorrect wording %q\ngot=%q", avoid, tt.desc)
				}
			}
		})
	}
}
