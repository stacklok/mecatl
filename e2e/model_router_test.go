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

// modelRouterSpecs covers the semantic subagent model router LIVE: a mecated whose operator settings.yaml defines a models.router taxonomy
// (the taxonomy is the enable — no flag needed) must, on a real plain Subagent delegation,
// classify the task and mint the child on the chosen category's model.
//
// OWN SPAWN: like modelSlotSpecs it owns its OWN mecated (NOT the shared suite target)
// because it needs a router taxonomy in its config, which the shared target is not started
// with. Local-only by construction.
//
// HOW THE ASSERTION WORKS. The harness observes the routed model on the wire:
// `RoutedCategory`/`RoutedModel` ride the `subagent.start` event payload
// (`routed_category`/`routed_model` proto fields, #97 / #110), so the spec
// asserts the OBSERVABLE facts that together prove the feature is wired and did not
// wedge: (A) the build-once "subagent model router ACTIVE" INFO names the category
// count + classifier model (logModelRouterFacts) — i.e. the router was wired, not
// silently OFF; (B) a plain Subagent delegation actually RAN and the run completed
// cleanly — i.e. the classifier call + the routed child both ran against the live
// provider without wedging; and (C) the `subagent.start` event carries the routed
// model the router classified this delegation to (the per-delegation wire assertion).
// The offline test (internal/app TestRouterRoutesChildToClassifiedModelE2E) reads the
// child's Model off a mock observer; this is the live wire confirmation.
//
// CLASSIFIER KNOB. The classifier slot (slots.router) is a SEPARATE knob
// (MECATL_E2E_ROUTER_CLASSIFIER_MODEL, default google/gemini-2.5-flash — alias
// router-cat) from the cheap category-target models, so the classifier is a model that
// demonstrably emits the one-line JSON verdict on OpenRouter rather than reusing the
// category lane (a prior openai/gpt-4.1-mini classifier returned near-empty completions —
// a fail-soft miss that inherits the default model, which empties the routed wire fields).
// The team/parallel router specs share this knob.
func modelRouterSpecs() {
	ginkgo.Describe("subagent model router", func() {
		ginkgo.It("classifies a plain Subagent delegation and routes it to a category model",
			ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(8*time.Minute),
			func(ctx ginkgo.SpecContext) {
				if !target.IsLocal() {
					ginkgo.Skip("remote target: cannot spawn with a router taxonomy in its config")
				}

				// The router taxonomy lives in the OPERATOR-TIER settings.yaml; write a
				// temp one and point mecated's config at it. Both categories map to real,
				// cheap OpenRouter-lane models so a routed child actually runs.
				small := envOrDefault("MECATL_E2E_ROUTER_SMALL_MODEL", "openai/gpt-4.1-mini")
				large := envOrDefault("MECATL_E2E_ROUTER_LARGE_MODEL", "openai/gpt-4.1")
				// The CLASSIFIER slot is a SEPARATE knob from the category targets: it must
				// reliably emit the one-line JSON verdict on OpenRouter, or RunModelRouter
				// gets a verdict-less turn → fail-soft miss → inherit the default model
				// (openai/gpt-4.1-mini returned near-empty completions as the classifier).
				// Default to a cheap model that demonstrably emits the JSON verdict; the
				// category targets stay cheap (they only run the routed child).
				classifier := envOrDefault("MECATL_E2E_ROUTER_CLASSIFIER_MODEL", "google/gemini-2.5-flash")
				dir, err := os.MkdirTemp("", "mecatl-router-e2e-*")
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
					"        description: trivial single-step lookups and file reads\n" +
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

				// (A) The build-once router fact must be present at startup.
				startLog := spawn.LogTail(8192)
				gomega.Expect(startLog).To(gomega.ContainSubstring("subagent model router ACTIVE"),
					"the build-once router fact must narrate the active router"+logTail())

				// (B) Drive a plain Subagent delegation; assert it ran and ended cleanly.
				res, runErr := drv.Run(ctx, harness.RunOpts{
					Scenario: "router-delegation", Timeout: 6 * time.Minute,
				}, `Call the Subagent tool exactly once with the goal "Read the file FRUIT.txt with the Read tool and reply with the fruit it names." Call no other tool. After the result returns, reply with the single word done.`)
				gomega.Expect(runErr).NotTo(gomega.HaveOccurred(), "router delegation transport error"+logTail())
				gomega.Expect(res.Result).NotTo(gomega.BeNil(), "router delegation produced no terminal result"+logTail())

				calls := res.ToolCalls("Subagent")
				gomega.Expect(len(calls)).To(gomega.BeNumerically(">=", 1),
					"expected at least one Subagent call (the routed delegation)"+logTail())
				gomega.Expect(res.Stop()).To(gomega.Equal("end_turn"),
					"the run did not end cleanly (stop="+res.Stop()+") — the classifier or routed child may have wedged"+logTail())
				gomega.Expect(strings.TrimSpace(res.Result.Error)).To(gomega.BeEmpty(),
					"a clean routed run carries no error"+logTail())

				// (C) The subagent.start event carries the routed model the classifier
				// picked for this delegation. The task ("Read the file … reply with the
				// fruit") is a trivial single-step lookup, so the router should classify it
				// to the "small" category (the cheap lane). Assert the routed model rides
				// the wire and matches the small-category model — the per-delegation proof.
				starts := res.SubagentMsgs(client.SubagentStart)
				gomega.Expect(len(starts)).To(gomega.BeNumerically(">=", 1),
					"expected at least one subagent.start event (the routed delegation)"+logTail())
				start := starts[0]
				gomega.Expect(start.RoutedModel).To(gomega.Equal(small),
					"the routed child should be on the small-category model"+logTail())
				gomega.Expect(start.RoutedCategory).To(gomega.Equal("small"),
					"the router should classify the trivial lookup as 'small'"+logTail())
			})
	})
}
