//go:build e2e

package e2e_test

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/e2e/harness"
)

// parallelSpecs is scenario 5: one Parallel call with two branches, asserted
// purely from the parallel.* event family — parallel.start with BranchCount=2,
// per-branch events, and parallel.end.
func parallelSpecs() {
	ginkgo.Describe("parallel", func() {
		ginkgo.It("runs a two-branch Parallel fan-out to completion", ginkgo.SpecTimeout(390*time.Second), func(ctx ginkgo.SpecContext) {
			res := runScenario(ctx, harness.RunOpts{
				Scenario:     "parallel-construct",
				ApproveTools: []string{"Parallel"}, // backup; the CLI permission config already allows Parallel
				Timeout:      6 * time.Minute,
			}, `Use the tool named "Parallel" — not the Subagent tool — exactly once, with these arguments: tasks = ["Reply with the single word RED. Call no tool.", "Reply with the single word BLUE. Call no tool."] and join = "all". Never call Subagent and call no other tool. When the Parallel tool returns, reply with the single word done.`)

			starts := res.ParallelMsgs(client.ParallelStart)
			gomega.Expect(starts).NotTo(gomega.BeEmpty(), "no parallel.start observed\n"+failureReport())
			gomega.Expect(starts[0].BranchCount).To(gomega.Equal(2),
				"expected a 2-branch fan-out\n"+failureReport())

			branchStarts := res.ParallelMsgs(client.ParallelBranchStart)
			branches := map[int]bool{}
			for _, b := range branchStarts {
				branches[b.BranchIndex] = true
			}
			gomega.Expect(len(branches)).To(gomega.BeNumerically(">=", 2),
				"expected >=2 distinct branch starts\n"+failureReport())

			ends := res.ParallelMsgs(client.ParallelEnd)
			gomega.Expect(ends).NotTo(gomega.BeEmpty(), "no parallel.end observed\n"+failureReport())

			calls := res.ToolCalls("Parallel")
			gomega.Expect(calls).NotTo(gomega.BeEmpty(), failureReport())
			tr := res.ToolResult(calls[0].ID)
			gomega.Expect(tr).NotTo(gomega.BeNil(), failureReport())
			gomega.Expect(tr.IsError).To(gomega.BeFalse(), "Parallel tool result errored\n"+failureReport())
		})

		// Issue #30 — the RUNTIME-DISCOVERABILITY axis. The offline test proves
		// the 'branch id:' line is IN the Parallel result text and a scripted
		// InspectSubagent call works; only a REAL model proves the model NOTICES
		// that line and USES it. The prompt runs a small 2-branch fan-out, then
		// steers the model to read branch 0's id off the result text and inspect
		// it. Assertions are STRUCTURAL (live-model nondeterminism): a Parallel run
		// happened, an InspectSubagent call carrying a 'parallel-'-prefixed id
		// happened, and that inspect returned a non-error transcript — NOT exact
		// text. InspectSubagent is a floor-Allow read-only child-observability tool
		// (no ask round-trip), so no ApproveTools entry is needed for it; Parallel
		// stays a backup (the CLI config already allows it).
		ginkgo.It("reads a branch id off the Parallel result and inspects that branch's transcript", ginkgo.SpecTimeout(390*time.Second), func(ctx ginkgo.SpecContext) {
			res := runScenario(ctx, harness.RunOpts{
				Scenario:     "parallel-inspect",
				ApproveTools: []string{"Parallel"}, // backup; the CLI permission config already allows Parallel
				Timeout:      6 * time.Minute,
			}, `Do these two steps in order, using no other tools.
Step 1: Call the tool named "Parallel" — not the Subagent tool — exactly once, with these arguments: tasks = ["Reply with the single word RED. Call no tool.", "Reply with the single word BLUE. Call no tool."] and join = "all".
Step 2: The Parallel result lists each branch with a line of the form "branch id: parallel-...". Take the branch id shown for branch 0 (the first branch) and call the InspectSubagent tool exactly once with agent_id set to that exact branch id, to read that branch's transcript.
When InspectSubagent returns, reply with the single word done.`)

			// A real Parallel run must have happened (the precondition for the
			// branch id to exist in the result text).
			calls := res.ToolCalls("Parallel")
			gomega.Expect(calls).NotTo(gomega.BeEmpty(), "no Parallel tool.call observed\n"+failureReport())
			ptr := res.ToolResult(calls[0].ID)
			gomega.Expect(ptr).NotTo(gomega.BeNil(), failureReport())
			gomega.Expect(ptr.IsError).To(gomega.BeFalse(), "Parallel tool result errored\n"+failureReport())

			// The model NOTICED the 'branch id:' line and USED it: an
			// InspectSubagent call carrying a 'parallel-'-prefixed id (the
			// branch-id grammar is "parallel-<callID>-<index>").
			inspects := res.ToolCalls("InspectSubagent")
			gomega.Expect(inspects).NotTo(gomega.BeEmpty(),
				"no InspectSubagent tool.call observed (the model did not act on the branch id)\n"+failureReport())
			inspected := false
			for _, c := range inspects {
				if !strings.Contains(c.Args, "parallel-") {
					continue
				}
				// The inspect must have returned that branch's bounded transcript:
				// a non-error result with content (a forged/unknown id yields an
				// IsError "not an inspectable child session" result instead).
				if tr := res.ToolResult(c.ID); tr != nil && !tr.IsError && strings.TrimSpace(tr.Content) != "" {
					inspected = true
				}
			}
			gomega.Expect(inspected).To(gomega.BeTrue(),
				"no successful InspectSubagent of a parallel- branch id observed (the branch transcript was not pulled)\n"+failureReport())
		})

		// ADR 0039 — the auto-merge fast path. A SINGLE-BRANCH join=first
		// Parallel run auto-merges the winner's diff back into the parent
		// workspace (default-on, no flag), so a delegated implementer's edits land
		// without a manual copy/merge step. The live proof: the branch writes a
		// sentinel file in its isolated fork, and after the Parallel call returns
		// the sentinel file EXISTS in the PARENT workspace (loc.Workspace()).
		// Without auto-merge the fork is preserved (or torn down) but the parent
		// tree is untouched — the sentinel would be absent. This is the regression
		// guard for the capability the operator asked for ("everything through
		// sub-agents" with edits that actually land).
		ginkgo.It("auto-merges a single-branch join=first winner's file into the parent workspace",
			ginkgo.SpecTimeout(6*time.Minute),
			func(ctx ginkgo.SpecContext) {
				// A dedicated mecated over its OWN scratch tree (the harness
				// git-inits the workspace + commits the fixtures, so the force-copy
				// fork has a base to diff against). Auto-merge is default-on, so no
				// flag is needed.
				loc, err := harness.NewLocalWith()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawning the auto-merge mecated failed")
				defer func() { _ = loc.Close() }()

				// The sentinel the branch will write. Distinctive enough that no
				// model would invent it; the file name is unique in the workspace.
				const (
					sentinelFile = "AUTOMERGE-SENTINEL.txt"
					sentinelBody = "AUTOMERGE-4E2C9A1B\n"
				)

				driver := harness.NewDriver(loc)
				report := func(res *harness.RunResult, runErr error) string {
					return harness.Summary(res, runErr, loc.LogTail(4096))
				}

				// One branch, join=first: the auto-merge eligibility condition.
				// The branch writes the sentinel file via Shell, then the Parallel
				// call returns and the auto-merge applies the fork's diff back.
				res, err := driver.Run(ctx, harness.RunOpts{
					Scenario: "parallel-auto-merge", Timeout: 6 * time.Minute,
					ApproveTools: []string{"Parallel"}, // backup; the CLI config allows it
				}, `Use the tool named "Parallel" — not the Subagent tool — exactly once, with these arguments: tasks = ["Use the Shell tool to create a file named `+sentinelFile+` containing exactly the text `+sentinelBody+` (no trailing newline beyond the one in that text). Call no other tool. After the file is written, reply with the single word done."] and join = "first". Never call Subagent and call no other tool. When the Parallel tool returns, reply with the single word done.`)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), report(res, err))
				gomega.Expect(res).NotTo(gomega.BeNil(), report(res, err))

				// A Parallel call must have happened with a single branch.
				calls := res.ToolCalls("Parallel")
				gomega.Expect(calls).NotTo(gomega.BeEmpty(), "no Parallel tool.call observed\n"+report(res, err))
				starts := res.ParallelMsgs(client.ParallelStart)
				gomega.Expect(starts).NotTo(gomega.BeEmpty(), "no parallel.start observed\n"+report(res, err))
				gomega.Expect(starts[0].BranchCount).To(gomega.Equal(1),
					"expected a 1-branch fan-out (the auto-merge eligibility condition)\n"+report(res, err))

				// The Parallel result must NOT be an error (a merge conflict would
				// surface here — that's a real failure, not a regression).
				tr := res.ToolResult(calls[0].ID)
				gomega.Expect(tr).NotTo(gomega.BeNil(), report(res, err))
				gomega.Expect(tr.IsError).To(gomega.BeFalse(),
					"Parallel tool result errored (auto-merge conflict?)\n"+report(res, err))

				// THE regression assertion: the sentinel file landed in the PARENT
				// workspace. Without auto-merge the fork's writes never reach the
				// parent tree, so this file would not exist.
				got, readErr := os.ReadFile(filepath.Join(loc.Workspace(), sentinelFile))
				gomega.Expect(readErr).NotTo(gomega.HaveOccurred(),
					"the auto-merged sentinel file is absent from the parent workspace — auto-merge did not land the winner's diff\n"+report(res, err))
				gomega.Expect(string(got)).To(gomega.ContainSubstring("AUTOMERGE-4E2C9A1B"),
					"the auto-merged sentinel file content is wrong — got %q\n%s", got, report(res, err))
			})
	})
}
