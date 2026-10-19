//go:build e2e

package e2e_test

import (
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/e2e/harness"
)

// guardrailsSpecs covers issue #159 (the guardrails slot-enable UX overhaul) LIVE:
// a mecated spawned with a slot-ONLY guardrails config — `--model-slot guardrail=cheap`
// + `--model-alias cheap=<checker-model>`, NO `--guardrails-model` — must (A) emit the
// build-once `guardrails: ON` startup line naming the RESOLVED slot model (the must-fix
// #1: the line reports the resolved checker, not an absent gate value), and (B) actually
// run the checker against the live provider on a tool call the default advisory set
// observes (WebSearch) without wedging. The offline twin pins the gate + the posture-line
// branches (internal/app); THIS spec adds what only a live provider can show: the
// tool-less checker engine built on the slot model serves a real PreToolUse/PostToolUse
// verdict round-trip against the live backend and the session completes cleanly.
//
// OWN SPAWN: owns its own mecated (NOT the shared suite target) because it needs the
// --model-slot guardrail= + --model-alias flags. Local-only by construction. The
// session model stays DefaultModel() (anthropic/claude-haiku-4.5); the checker runs on
// the cheap slot model — a DIFFERENT model, proving the slot routes the checker call.
func guardrailsSpecs() {
	ginkgo.Describe("guardrails slot-enable", func() {
		ginkgo.It("enables the checker via a slot-only config and runs it live on a tool call",
			ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(6*time.Minute),
			func(ctx ginkgo.SpecContext) {
				if !target.IsLocal() {
					ginkgo.Skip("remote target: cannot spawn with --model-slot guardrail= / --model-alias")
				}

				// The checker model: a real, cheap model on the OpenRouter lane. The
				// session runs on DefaultModel(); the guardrail checker must route here.
				checker := envOrDefault("MECATL_E2E_GUARDRAIL_MODEL", "openai/gpt-4.1-mini")

				// Slot-ONLY enable: NO --guardrails-model. Binding the slot alone enables
				// the checker (issue #159, the router-parity configure = enable).
				spawn, err := harness.NewLocalWith(
					"--model-alias", "cheap="+checker,
					"--model-slot", "guardrail=cheap",
				)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawn local mecated with --model-slot guardrail=")
				defer func() { _ = spawn.Close() }()

				drv := harness.NewDriver(spawn)
				logTail := func() string { return "\n--- mecated log tail ---\n" + spawn.LogTail(8192) }

				// (A) The build-once posture line must be ON and name the RESOLVED slot
				// model (not an absent gate value). It is emitted at startup, so it is
				// already in the log by the time the server is ready.
				startLog := spawn.LogTail(8192)
				gomega.Expect(startLog).To(gomega.ContainSubstring("guardrails: ON"),
					"the build-once posture line must report ON for a slot-only config (issue #159 must-fix #2)"+logTail())
				gomega.Expect(startLog).To(gomega.ContainSubstring("via slot `guardrail`"),
					"the posture line must name the slot provenance"+logTail())
				gomega.Expect(startLog).To(gomega.ContainSubstring(checker),
					"the posture line must name the RESOLVED slot model "+checker+" (issue #159 must-fix #1)"+logTail())
				gomega.Expect(startLog).NotTo(gomega.ContainSubstring("guardrails: OFF"),
					"a slot-only config must NOT report OFF"+logTail())

				// (B) Drive a WebSearch tool call — the default advisory set observes
				// WebSearch (observe-only: findings logged, content unchanged) — so the
				// checker runs a real PreToolUse + PostToolUse verdict round-trip on the
				// slot model against the live provider, and the session completes cleanly.
				// WebSearch needs a backend; the spawned mecated uses the default ladder.
				res, runErr := drv.Run(ctx, harness.RunOpts{
					Scenario: "guardrail-slot", Timeout: 90 * time.Second,
					ApproveTools: []string{"WebSearch"}, // backup; the CLI floor ASKs for it
				}, `Search the web for "openrouter models" and reply with the single word done.`)
				gomega.Expect(runErr).NotTo(gomega.HaveOccurred(), "guardrail-slot transport error"+logTail())
				gomega.Expect(res.Result).NotTo(gomega.BeNil(), "guardrail-slot produced no terminal result"+logTail())

				// A clean run carrying no error is the live proof the checker (on the slot
				// model) served its verdict round-trip without wedging the turn. A checker
				// model the provider rejected, or a guardrail that mis-fired (block on an
				// observe-only default), would surface as a failed/errored run.
				gomega.Expect(strings.TrimSpace(res.Result.Error)).To(gomega.BeEmpty(),
					"a clean run carries no error — the slot-model checker may have rejected the call or mis-fired"+logTail())
			})
	})
}
