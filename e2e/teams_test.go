//go:build e2e

package e2e_test

import (
	"fmt"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/e2e/harness"
)

// teamSpecs is scenario 6: a small team whose goal steers each worker to record
// one finding. Assertions: the team.* event family fired (team.start +
// REQUIRED recorded findings in the snapshots + team.end) and the Team tool
// result carries the "Team id:" line (the model-facing discovery channel for
// InspectMember).
func teamSpecs() {
	ginkgo.Describe("teams", func() {
		ginkgo.It("runs a two-worker team that records findings and reports the team id", ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(510*time.Second), func(ctx ginkgo.SpecContext) {
			res := runScenario(ctx, harness.RunOpts{
				Scenario:     "teams",
				ApproveTools: []string{"Team"}, // the built-in floor ASKs for Team; the CLI config allows it, this is the backup
				Timeout:      8 * time.Minute,
			}, `Call the Team tool exactly once, with goal = "Produce a two-line fruit report. Worker alpha must call RecordFinding exactly once recording the word apple. Worker beta must call RecordFinding exactly once recording the word banana. The lead waits for both findings and then writes the two-line report." and members = [ {"name": "lead", "role": "Coordinate: create one task for alpha and one for beta, wait for both findings, then synthesise the two-line fruit report."}, {"name": "alpha", "role": "Call RecordFinding exactly once recording the word apple, then message the lead that you are done."}, {"name": "beta", "role": "Call RecordFinding exactly once recording the word banana, then message the lead that you are done."} ]. Call no other tool. When the Team tool returns, reply with the single word done.`)

			gomega.Expect(res.TeamMsgs(client.TeamStart)).NotTo(gomega.BeEmpty(),
				"no team.start observed\n"+failureReport())

			// Coordination evidence: the prompt FORCES two RecordFinding calls,
			// so findings in the ledger snapshots are REQUIRED — an empty
			// snapshot must not pass. Tasks are counted INSIDE the snapshots
			// (len(m.Tasks), never the snapshot-message count: an empty
			// snapshot message would otherwise satisfy the assertion) and are
			// reported as supplementary context only.
			findings := 0
			for _, m := range res.TeamMsgs(client.TeamFindings) {
				findings += len(m.Findings)
			}
			for _, m := range res.TeamMsgs(client.TeamEnd) {
				findings += len(m.Findings)
			}
			tasks := 0
			for _, m := range res.TeamMsgs(client.TeamTasks) {
				tasks += len(m.Tasks)
			}
			gomega.Expect(findings).To(gomega.BeNumerically(">", 0),
				fmt.Sprintf("expected recorded findings in the team.findings/team.end snapshots (the goal forces two RecordFinding calls); saw %d findings, %d snapshot tasks\n",
					findings, tasks)+failureReport())

			gomega.Expect(res.TeamMsgs(client.TeamEnd)).NotTo(gomega.BeEmpty(),
				"no team.end observed\n"+failureReport())

			calls := res.ToolCalls("Team")
			gomega.Expect(calls).NotTo(gomega.BeEmpty(), failureReport())
			// NOTE: the 'Team id:' line rides BOTH result paths — the lead's
			// synthesis report AND the degraded joinTeamFallback (see
			// renderTeamResult). A fallback-path pass is accepted here: the
			// contract under test is the model-facing id discovery channel,
			// not synthesis quality (the findings assertion above covers
			// coordination).
			sawTeamID := false
			for _, c := range calls {
				if tr := res.ToolResult(c.ID); tr != nil && strings.Contains(tr.Content, "Team id:") {
					sawTeamID = true
				}
			}
			gomega.Expect(sawTeamID).To(gomega.BeTrue(),
				"Team tool result did not carry the 'Team id:' line\n"+failureReport())
		})
	})
}
