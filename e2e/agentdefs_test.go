//go:build e2e

package e2e_test

import (
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/e2e/harness"
)

// agentDefSpecs covers the named-agent-definition lane: a Subagent call with
// agent:"fruit-reader" must resolve the fixture def laid out at
// <workspace>/.claude/agents/fruit-reader.md (admitted because the harness
// passes --trust-project) and run the child on the def's SCOPED engine — the
// live proof that agent-def discovery (agentfs via the root alias) is wired
// end-to-end, mirroring skillSpecs' coverage of the skills lane.
//
// The assertions are EVENTS, never model prose: a Subagent call whose args
// name the def, a successful result carrying the agentId trailer, and a
// cleanly-ended child run.
func agentDefSpecs() {
	ginkgo.Describe("agent definitions", func() {
		ginkgo.It("runs a Subagent delegated to a named workspace agent def",
			ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(390*time.Second),
			func(ctx ginkgo.SpecContext) {
				res := runScenario(ctx, harness.RunOpts{
					Scenario: "agent-def",
					Timeout:  6 * time.Minute,
				}, `Call the Subagent tool exactly once with agent set to "fruit-reader" and prompt "Report which fruit FRUIT.txt names." Call no other tool. After the result returns, reply with the single word done.`)

				calls := res.ToolCalls("Subagent")
				gomega.Expect(calls).NotTo(gomega.BeEmpty(),
					"no Subagent tool.call observed\n"+failureReport())

				// The call must NAME the def — a bare-goal Subagent proves the
				// generic explorer, not the agentfs discovery lane.
				named := false
				for _, c := range calls {
					if strings.Contains(c.Args, "fruit-reader") {
						named = true
					}
				}
				gomega.Expect(named).To(gomega.BeTrue(),
					"no Subagent call naming agent fruit-reader observed\n"+failureReport())

				// The delegation must SUCCEED — an unresolvable def surfaces as
				// an error result listing the available agents.
				succeeded := false
				for _, c := range calls {
					if tr := res.ToolResult(c.ID); tr != nil && !tr.IsError &&
						strings.Contains(tr.Content, "agentId:") {
						succeeded = true
					}
				}
				gomega.Expect(succeeded).To(gomega.BeTrue(),
					"no successful Subagent(agent=fruit-reader) result observed\n"+failureReport())

				// The child must have ENDED CLEANLY (see subagentSpecs for why
				// the trailer alone is not enough).
				clean := 0
				for _, e := range res.SubagentMsgs(client.SubagentEnd) {
					if e.Stop == "end_turn" {
						clean++
					}
				}
				gomega.Expect(clean).To(gomega.BeNumerically(">=", 1),
					"expected the agent-def child to end stop=end_turn\n"+failureReport())

				// The def's scoped catalog is Read-only and the result must
				// carry the fixture's fact — proof the child ran the def's
				// engine against FRUIT.txt. The child's own Read call is NOT
				// observable here (child tool calls stay on the child stream,
				// gauntlet #7), so the assertion is on the result content, not
				// a parent-side Read event.
				fact := false
				for _, c := range calls {
					if tr := res.ToolResult(c.ID); tr != nil && !tr.IsError &&
						strings.Contains(tr.Content, "papaya") {
						fact = true
					}
				}
				gomega.Expect(fact).To(gomega.BeTrue(),
					"the agent-def result did not carry the FRUIT.txt fact\n"+failureReport())
				gomega.Expect(res.Approved).To(gomega.BeEmpty(),
					"no interactive approvals should be needed for a Read-only def\n"+failureReport())
			})
	})
}
