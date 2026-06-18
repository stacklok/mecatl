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

// dirtyForkSpecs is the live proof for ADR 0033: a read-only Subagent runs its
// Bash in an isolated git worktree forked from the session workspace, and the
// dirty-overlay (forker.WithDirtyOverlay) mirrors the operator's UNCOMMITTED
// working-tree state into that worktree. Before the fix the worktree was a clean
// `git worktree add HEAD` checkout, so a child asked to review the diff saw an
// empty `git status`/`git diff` and could not see the operator's in-progress
// work. This drives a real model through the real harness over a deliberately
// DIRTY workspace and asserts the child OBSERVED the uncommitted changes.
//
// The discriminator is the UNTRACKED-file sentinel: an untracked file simply
// does not exist in a clean HEAD checkout, so its presence in the child's
// reported `cat` output is unambiguous proof the overlay copied untracked,
// non-ignored files into the fork. The tracked-modification sentinel
// corroborates the `git diff --binary HEAD` apply leg. The assertion is on the
// Subagent RESULT text — the only model-visible channel for a child's findings
// (the subagent.* events are metadata-only by gauntlet #7) — which is exactly
// the surface the operator saw fail in the wild.
func dirtyForkSpecs() {
	ginkgo.Describe("dirty-fork overlay", func() {
		ginkgo.It("a read-only Subagent sees the operator's uncommitted working-tree changes",
			ginkgo.SpecTimeout(6*time.Minute),
			func(ctx ginkgo.SpecContext) {
				// A dedicated mecated over its OWN scratch tree: the harness
				// git-inits the workspace and commits the fixtures (clean HEAD),
				// and --trust-project gives the read-only Subagent its Bash +
				// worktree forker. We own Close.
				loc, err := harness.NewLocalWith()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawning the dirty-fork mecated failed")
				defer func() { _ = loc.Close() }()

				// Make the workspace DIRTY *after* the clean baseline commit:
				//   - a tracked modification (overwrite the committed FRUIT.txt)
				//   - an untracked, non-ignored new file
				// Both carry distinctive sentinels no model would invent.
				const (
					trackedSentinel   = "DIRTYTRACKED-7F3A2C9E"
					untrackedSentinel = "DIRTYUNTRACKED-9B4E1D6A"
					untrackedName     = "UNCOMMITTED-NOTE.txt"
				)
				ws := loc.Workspace()
				gomega.Expect(os.WriteFile(filepath.Join(ws, "FRUIT.txt"),
					[]byte("mango\n"+trackedSentinel+"\n"), 0o644)).To(gomega.Succeed(),
					"seeding the tracked modification failed")
				gomega.Expect(os.WriteFile(filepath.Join(ws, untrackedName),
					[]byte(untrackedSentinel+"\n"), 0o644)).To(gomega.Succeed(),
					"seeding the untracked file failed")

				driver := harness.NewDriver(loc)
				report := func(res *harness.RunResult, runErr error) string {
					return harness.Summary(res, runErr, loc.LogTail(4096))
				}

				res, err := driver.Run(ctx, harness.RunOpts{Scenario: "dirty-fork", Timeout: 6 * time.Minute},
					`Call the Subagent tool exactly once. Set its goal to: "Use the Bash tool to run `+"`git status --short`"+`, then print the contents of FRUIT.txt and `+untrackedName+` using `+"`cat`"+`. Reply with the exact lines you observed, copying any unusual tokens verbatim." Place no other tool call. After the Subagent result returns, reply with the single word done.`)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), report(res, err))
				gomega.Expect(res).NotTo(gomega.BeNil(), report(res, err))

				calls := res.ToolCalls("Subagent")
				gomega.Expect(calls).NotTo(gomega.BeEmpty(),
					"no Subagent tool.call observed\n"+report(res, err))

				// The child must have ENDED CLEANLY — the agentId trailer rides
				// every terminal incl. error, so a silently-erroring child would
				// otherwise slip past the content assertion below.
				ends := res.SubagentMsgs(client.SubagentEnd)
				cleanEnd := false
				for _, e := range ends {
					if !e.IsError && (e.Stop == "" || e.Stop == "end_turn" || e.Stop == "stop") {
						cleanEnd = true
					}
				}
				gomega.Expect(cleanEnd).To(gomega.BeTrue(),
					"no Subagent child ended cleanly\n"+report(res, err))

				// Concatenate every Subagent result body: the child's findings.
				var seen strings.Builder
				for _, c := range calls {
					if tr := res.ToolResult(c.ID); tr != nil {
						seen.WriteString(tr.Content)
						seen.WriteString("\n")
					}
				}
				got := seen.String()

				// The discriminator. Without the overlay the fork is a clean HEAD
				// checkout: the untracked file is absent and FRUIT.txt holds the
				// committed content, so NEITHER sentinel can appear.
				gomega.Expect(got).To(gomega.ContainSubstring(untrackedSentinel),
					"the Subagent did not observe the UNTRACKED file — the dirty overlay did not copy untracked work into the fork\n"+report(res, err))
				gomega.Expect(got).To(gomega.ContainSubstring(trackedSentinel),
					"the Subagent did not observe the TRACKED modification — the dirty overlay did not apply the working-tree diff into the fork\n"+report(res, err))
			})
	})
}
