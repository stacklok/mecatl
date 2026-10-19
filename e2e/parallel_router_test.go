//go:build e2e

package e2e_test

import (
	"os"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/e2e/harness"
)

// parallelRouterSpecs covers the semantic model router (extended to PARALLEL
// BRANCHES by issue #100) LIVE: a mecated whose operator settings.yaml
// defines a models.router taxonomy (the taxonomy is the enable) must, on a real Parallel
// fan-out, classify each branch's task and mint that branch on the chosen category's model.
//
// OWN SPAWN: like modelRouterSpecs / teamRouterSpecs it owns its OWN mecated (NOT the
// shared suite target) because it needs a router taxonomy in its config, which the shared
// target is not started with. Local-only by construction.
//
// HOW THE ASSERTION WORKS. The harness observes the routed model on the wire:
// `RoutedCategory`/`RoutedModel` ride the `parallel.branch{branch_start}` event payload
// (`routed_category`/`routed_model` proto fields on the `Parallel` message,
// issue #100), so the spec asserts the OBSERVABLE facts that together prove the parallel
// family's routing FIRED and did not wedge:
//
//	(A) the build-once "subagent model router ACTIVE" INFO (logModelRouterFacts) — the
//	    router was wired, not silently OFF. The SAME routeTask closure that fact narrates
//	    is the one threaded into the Parallel tool's branch-routing path
//	    (ParallelTool.maybeRouteBranchModel → caps.routeTask), so an ACTIVE router IS the
//	    branch router.
//
//	(B) a Parallel call with two branches steering toward different categories actually
//	    RAN and JOINED — parallel.start (BranchCount=2) + per-branch starts + parallel.end
//	    fired, the Parallel result is non-error, and the run ended cleanly — i.e. each
//	    branch's classifier call plus the routed branch runs all executed against the live
//	    provider without wedging.
//
//	(C) each routed branch_start carries the routed model the classifier picked for that
//	    branch — the PER-BRANCH WIRE assertion. Branch 0's task is a trivial single-step
//	    lookup, so it should classify to the "small" category (the cheap lane); branch 1's
//	    task is a deep multi-step analysis, so it should classify to "large". This replaces
//	    the older "subagent routed" log-substring proxy with a deterministic, per-branch
//	    wire check (the field is populated only on a successful classification — the same
//	    signal, but checkable per branch).
//
// CLASSIFIER KNOB. The routed wire fields are populated only on a SUCCESSFUL
// classification: the classifier must emit the one-line JSON verdict, which RunModelRouter
// parses whole-output-single-object (modelrouter.go); a verdict-less/empty classifier turn
// is a clean fail-soft MISS that empties the routed wire fields. So the assertion is only
// as reliable as the CLASSIFIER MODEL. The classifier slot is therefore a SEPARATE knob
// (MECATL_E2E_ROUTER_CLASSIFIER_MODEL, default google/gemini-2.5-flash — alias router-cat →
// slots.router) from the cheap category-target models: a small reliable model that
// demonstrably emits the JSON verdict, NOT reused from the category lane (a prior
// openai/gpt-4.1-mini classifier returned near-empty completions and emptied the wire
// fields, the live flake the knob fixes). The DETERMINISTIC per-branch proof also exists
// offline (TestParallelRoutesBranchesToCategoryModelsE2E reads each branch's Model off a
// mock observer); this live test proves the same path works against a real provider — kept
// FlakeAttempts(2) for residual provider jitter, not classifier emptiness.
func parallelRouterSpecs() {
	ginkgo.Describe("parallel model router", func() {
		ginkgo.It("routes parallel branches through the classifier and joins cleanly",
			ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(7*time.Minute),
			func(ctx ginkgo.SpecContext) {
				if !target.IsLocal() {
					ginkgo.Skip("remote target: cannot spawn with a router taxonomy in its config")
				}

				// The router taxonomy lives in the OPERATOR-TIER settings.yaml; write a
				// temp one and point mecated's config at it. Both categories map to real,
				// cheap OpenRouter-lane models so a routed branch actually runs.
				small := envOrDefault("MECATL_E2E_ROUTER_SMALL_MODEL", "openai/gpt-4.1-mini")
				large := envOrDefault("MECATL_E2E_ROUTER_LARGE_MODEL", "openai/gpt-4.1")
				// The CLASSIFIER slot is a SEPARATE knob from the category targets: it must
				// reliably emit the one-line JSON verdict on OpenRouter, or RunModelRouter
				// gets a verdict-less turn → fail-soft miss → NO "subagent routed" line (the
				// live flake — openai/gpt-4.1-mini returned near-empty completions as the
				// classifier). Default to a cheap model that demonstrably emits the JSON
				// verdict; the category targets stay cheap (they only run the routed branch).
				classifier := envOrDefault("MECATL_E2E_ROUTER_CLASSIFIER_MODEL", "google/gemini-2.5-flash")
				dir, err := os.MkdirTemp("", "mecatl-parallel-router-e2e-*")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				defer func() { _ = os.RemoveAll(dir) }()
				settings := dir + "/settings.yaml"
				cfg := "" +
					"models:\n" +
					"  aliases:\n" +
					"    small-cat: " + small + "\n" +
					"    large-cat: " + large + "\n" +
					"    router-cat: " + classifier + "\n" +
					"  slots:\n" +
					"    router: router-cat\n" +
					"  router:\n" +
					"    default-category: small\n" +
					"    categories:\n" +
					"      - name: small\n" +
					"        description: trivial single-step lookups and one-word replies\n" +
					"        model: small-cat\n" +
					"      - name: large\n" +
					"        description: deep multi-step reasoning and analysis\n" +
					"        model: large-cat\n"
				gomega.Expect(os.WriteFile(settings, []byte(cfg), 0o600)).To(gomega.Succeed())

				// The taxonomy in settings.yaml enables the router — no flag.
				spawn, err := harness.NewLocalWith(
					"--permission-config", settings,
				)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawn local mecated with a router taxonomy")
				defer func() { _ = spawn.Close() }()

				drv := harness.NewDriver(spawn)
				logTail := func() string { return "\n--- mecated log tail ---\n" + spawn.LogTail(8192) }

				// (A) The build-once router fact must be present at startup — the same
				// closure it narrates is the one the Parallel tool consults per branch.
				startLog := spawn.LogTail(8192)
				gomega.Expect(startLog).To(gomega.ContainSubstring("subagent model router ACTIVE"),
					"the build-once router fact must narrate the active router"+logTail())

				// (B) Drive a Parallel fan-out with two branches whose tasks steer toward
				// different categories, joined with join="all" so both branches must
				// complete. Each branch is a routed path (no agent def pins a branch).
				res, runErr := drv.Run(ctx, harness.RunOpts{
					Scenario: "parallel-router-fanout", Timeout: 5 * time.Minute,
				}, `Use the tool named "Parallel" — not the Subagent tool — exactly once, with these arguments: tasks = ["A trivial single-step lookup: reply with the single word RED. Call no tool.", "A deep multi-step analysis task: reply with the single word BLUE. Call no tool."] and join = "all". Never call Subagent and call no other tool. When the Parallel tool returns, reply with the single word done.`)
				gomega.Expect(runErr).NotTo(gomega.HaveOccurred(), "parallel router fan-out transport error"+logTail())
				gomega.Expect(res.Result).NotTo(gomega.BeNil(), "parallel router fan-out produced no terminal result"+logTail())

				// The Parallel call ran with a 2-branch fan-out — each branch was started
				// (and, being def-less, classified).
				starts := res.ParallelMsgs(client.ParallelStart)
				gomega.Expect(starts).NotTo(gomega.BeEmpty(), "no parallel.start observed"+logTail())
				gomega.Expect(starts[0].BranchCount).To(gomega.Equal(2),
					"expected a 2-branch fan-out"+logTail())

				branchStarts := res.ParallelMsgs(client.ParallelBranchStart)
				branches := map[int]client.ParallelMsg{}
				for _, b := range branchStarts {
					branches[b.BranchIndex] = b
				}
				gomega.Expect(len(branches)).To(gomega.BeNumerically(">=", 2),
					"expected >=2 distinct branch starts (each a routed branch)"+logTail())

				// The fan-out JOINED (join="all" requires both branches to finish) — each
				// branch's classifier call plus the routed branch run all executed.
				gomega.Expect(res.ParallelMsgs(client.ParallelEnd)).NotTo(gomega.BeEmpty(),
					"no parallel.end observed — the fan-out did not join"+logTail())

				calls := res.ToolCalls("Parallel")
				gomega.Expect(calls).NotTo(gomega.BeEmpty(), "no Parallel tool.call observed"+logTail())
				tr := res.ToolResult(calls[0].ID)
				gomega.Expect(tr).NotTo(gomega.BeNil(), "no Parallel tool result observed"+logTail())
				gomega.Expect(tr.IsError).To(gomega.BeFalse(), "Parallel tool result errored"+logTail())

				// The run ended cleanly: nothing wedged against the live provider.
				gomega.Expect(res.Stop()).To(gomega.Equal("end_turn"),
					"the run did not end cleanly (stop="+res.Stop()+") — a branch classifier or a routed branch may have wedged"+logTail())
				gomega.Expect(strings.TrimSpace(res.Result.Error)).To(gomega.BeEmpty(),
					"a clean routed parallel run carries no error"+logTail())

				// (C) Each routed branch_start carries the routed model the classifier picked
				// for that branch — the PER-BRANCH WIRE assertion (RoutedCategory/RoutedModel
				// on the parallel.branch{branch_start} event). Branch 0's task is a
				// trivial single-step lookup → "small" (the cheap lane); branch 1's task is a
				// deep multi-step analysis → "large". The fields are populated only on a
				// successful classification, so this is the deterministic per-branch proof
				// that routing FIRED and selected the right model — replacing the older
				// "subagent routed" log-substring proxy.
				b0 := branches[0]
				gomega.Expect(b0.RoutedCategory).To(gomega.Equal("small"),
					"branch 0 (trivial lookup) should classify to 'small'"+logTail())
				gomega.Expect(b0.RoutedModel).To(gomega.Equal(small),
					"branch 0 should be minted on the small-category model"+logTail())
				b1 := branches[1]
				gomega.Expect(b1.RoutedCategory).To(gomega.Equal("large"),
					"branch 1 (deep multi-step analysis) should classify to 'large'"+logTail())
				gomega.Expect(b1.RoutedModel).To(gomega.Equal(large),
					"branch 1 should be minted on the large-category model"+logTail())
			})
	})
}
