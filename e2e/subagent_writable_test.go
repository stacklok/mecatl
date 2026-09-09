//go:build e2e

package e2e_test

import (
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/e2e/harness"
)

// subagentWritableSpecs is the live counterpart to the offline writable-Subagent
// tests: a real model run that delegates a write task with mode:"read-write" and
// asserts the child's edit actually LANDS in the parent workspace. With direct-write
// (ADR 0041) the child writes the REAL parent workspace IN PLACE — no fork, no
// merge — exactly as the main agent does; the sentinel file therefore appears in the
// parent tree directly. The writable Subagent is the "delegate one task and land its
// edits" path, so it earns the real-filesystem regression assertion.
//
// Without direct-write the child's writes would not reach the parent tree and the
// sentinel would be absent. Writable mode is default-wired (no flag), so nothing
// special is enabled here.
func subagentWritableSpecs() {
	ginkgo.Describe("subagent writable mode", func() {
		ginkgo.It("lands a mode:read-write subagent's edit in the parent workspace",
			ginkgo.SpecTimeout(6*time.Minute),
			func(ctx ginkgo.SpecContext) {
				// A dedicated mecated over its OWN git-inited scratch tree (git is the
				// rollback layer for the direct-write child). Writable Subagent is
				// default-on, no flag.
				loc, err := harness.NewLocalWith()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawning the writable-subagent mecated failed")
				defer func() { _ = loc.Close() }()

				// A sentinel distinctive enough that no model would invent it; the
				// file name is unique in the workspace.
				const (
					sentinelFile = "SUBAGENT-RW-SENTINEL.txt"
					sentinelBody = "SUBAGENTRW-7F1D3E80\n"
				)

				driver := harness.NewDriver(loc)
				report := func(res *harness.RunResult, runErr error) string {
					return harness.Summary(res, runErr, loc.LogTail(4096))
				}

				// One writable Subagent call: the child writes the sentinel via
				// Shell/Write DIRECTLY into the real parent workspace (no fork, no
				// merge — ADR 0041).
				res, err := driver.Run(ctx, harness.RunOpts{
					Scenario: "subagent-writable", Timeout: 6 * time.Minute,
					ApproveTools: []string{"Subagent"}, // backup; the CLI config allows it
				}, `Use the tool named "Subagent" exactly once, with these arguments: mode = "read-write" and prompt = "Use the Write tool (or Shell) to create a file named `+sentinelFile+` containing exactly the text `+sentinelBody+`. Then reply with the single word done." Call no other tool yourself. When the Subagent tool returns, reply with the single word done.`)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), report(res, err))
				gomega.Expect(res).NotTo(gomega.BeNil(), report(res, err))

				// A Subagent call must have happened and not errored.
				calls := res.ToolCalls("Subagent")
				gomega.Expect(calls).NotTo(gomega.BeEmpty(), "no Subagent tool.call observed\n"+report(res, err))
				tr := res.ToolResult(calls[0].ID)
				gomega.Expect(tr).NotTo(gomega.BeNil(), report(res, err))
				gomega.Expect(tr.IsError).To(gomega.BeFalse(),
					"Subagent tool result errored\n"+report(res, err))

				// THE regression assertion: the child's edit landed in the PARENT
				// workspace. With direct-write the child writes the real tree in place,
				// so the file exists directly; if the child were somehow isolated from
				// the parent tree it would be absent.
				got, readErr := os.ReadFile(filepath.Join(loc.Workspace(), sentinelFile))
				gomega.Expect(readErr).NotTo(gomega.HaveOccurred(),
					"the writable subagent's file is absent from the parent workspace — the direct-write edit did not land\n"+report(res, err))
				gomega.Expect(string(got)).To(gomega.ContainSubstring("SUBAGENTRW-7F1D3E80"),
					"the written file content is wrong — got %q\n%s", got, report(res, err))
			})
	})
}
