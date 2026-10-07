//go:build e2e

package e2e_test

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/e2e/harness"
)

// modeModelSpecs covers mode→model re-resolution (the opusplan pattern) LIVE: a mecated spawned with `--model-slot plan=reasoning --model-alias
// reasoning=<model>` must wire the `plan` slot — proven by the build-once "model slot
// ACTIVE" fact naming the plan slot + the resolved model (logSlotConfigFacts).
//
// COVERAGE NOTE: the authoritative end-to-end for the plan↔execute FLIP is the OFFLINE
// twin TestModeFlipEndToEndModelObserved (internal/adapter/server), which uses the
// mockllm request observer to assert the provider saw the session model on turn 1 and
// the plan model on turn 2 across a real SetMode. The live harness driver currently
// exposes no SetMode primitive (RunOpts carries no Mode field and the driver issues no
// session/set_mode), so a live plan↔execute flip cannot be driven here yet — this spec
// asserts the slot WIRING (the only live-observable half) and the flip is a follow-up
// once the harness driver gains a SetMode verb. A plan slot that failed to resolve
// would WARN+degrade (no ACTIVE line) and fail this spec.
func modeModelSpecs() {
	ginkgo.Describe("mode model (plan slot)", func() {
		ginkgo.It("wires the plan slot to the reasoning model (opusplan)",
			ginkgo.SpecTimeout(4*time.Minute),
			func(ginkgo.SpecContext) {
				if !target.IsLocal() {
					ginkgo.Skip("remote target: cannot spawn with --model-slot")
				}
				// The plan-mode model: a real, cheap model on the OpenRouter lane standing
				// in for the strong-reasoning model an operator would bind in production.
				planModel := envOrDefault("MECATL_E2E_SLOT_PLAN_MODEL", "openai/gpt-4.1-mini")

				spawn, err := harness.NewLocalWith(
					"--model-alias", "reasoning="+planModel,
					"--model-slot", "plan=reasoning",
				)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawn local mecated with --model-slot plan=reasoning")
				defer func() { _ = spawn.Close() }()

				logTail := func() string { return "\n--- mecated log tail ---\n" + spawn.LogTail(8192) }
				startLog := spawn.LogTail(8192)
				// The build-once plan-slot fact must narrate the active slot + resolved model.
				gomega.Expect(startLog).To(gomega.ContainSubstring("model slot ACTIVE"),
					"the build-once slot fact must narrate the active plan slot"+logTail())
				gomega.Expect(startLog).To(gomega.ContainSubstring(planModel),
					"the plan slot fact must name the resolved reasoning model "+planModel+logTail())
			})
	})
}
