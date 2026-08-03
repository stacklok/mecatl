//go:build e2e

package e2e_test

import (
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/e2e/harness"
)

// ruleSpecs is scenario #329. The DETERMINISTIC assertion is the composition
// fact mecated logs at build time when rules are discovered ("rules ENABLED");
// the BEHAVIOURAL marker ("reply starts with RULES-OK") is model-dependent — a
// path-scoped rule applied to a plain question may or may not fire — so it lives
// in a QUARANTINE-labelled spec that records its outcome but never fails the
// suite (the e2e/soul_test.go pattern).
func ruleSpecs() {
	ginkgo.Describe("rules", func() {
		ginkgo.It("logs the rules-enabled composition fact at build", func() {
			if !target.IsLocal() {
				ginkgo.Skip("remote target: cannot read the server's log")
			}
			// The fact is logged once at app.Build (startup), so the captured
			// log already holds it — no run needed. Conventional discovery is
			// always-on (no flag); the workspace lane fixture
			// (<workspace>/.claude/rules/testing.md) is admitted because the
			// harness passes --trust-project.
			tail := target.LogTail(64 * 1024)
			gomega.Expect(tail).To(gomega.ContainSubstring("rules ENABLED"),
				"the rules composition diagnostic was not logged at startup; the workspace rule fixture or the always-on conventional discovery is broken")
			gomega.Expect(tail).To(gomega.ContainSubstring("testing"),
				"the rules ENABLED log must name the discovered 'testing' rule")
		})

		ginkgo.It("withholds project-tier rules on an untrusted workspace", func() {
			// The trust gate is the ONE boundary for always-on conventional
			// discovery (ADR 0081 §5): a second mecated spawned with
			// --trust-project=false (Go's flag pkg: last wins over the standard
			// args' --trust-project) must WITHHOLD the workspace lane
			// (<workspace>/.claude/rules/testing.md) — the WARN is the
			// deterministic composition fact, and "rules ENABLED" must NOT
			// appear (the harness's fake-HOME user lanes carry no rules, so
			// withholding the project tier leaves zero sources).
			untrusted, err := harness.NewLocalWith("--trust-project=false")
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawning an untrusted mecated failed")
			defer func() { _ = untrusted.Close() }()

			tail := untrusted.LogTail(64 * 1024)
			gomega.Expect(tail).To(gomega.ContainSubstring("project-tier rules WITHHELD (untrusted workspace)"),
				"an untrusted workspace must log the project-tier-withheld WARN; the trust gate on rules discovery is broken")
			gomega.Expect(tail).NotTo(gomega.ContainSubstring("rules ENABLED"),
				"rules were ENABLED on an untrusted workspace — a malicious repo's .claude/rules would reach the model")
		})

		ginkgo.It("behavioural marker: reply carries the rule's RULES-OK prefix",
			ginkgo.Label("quarantine"), ginkgo.SpecTimeout(150*time.Second),
			func(ctx ginkgo.SpecContext) {
				// QUARANTINE: records the outcome, NEVER fails the suite — model
				// adherence to a path-scoped rule on a plain (non-file) question
				// is not a harness contract.
				res, err := driver.Run(ctx, harness.RunOpts{Scenario: "rules-behavioural", Timeout: 2 * time.Minute},
					"Briefly, how should Go test files be written?")
				switch {
				case err != nil:
					ginkgo.AddReportEntry("rules behavioural marker (quarantine)",
						"run failed (not counted): "+err.Error())
				case res.Result == nil:
					ginkgo.AddReportEntry("rules behavioural marker (quarantine)",
						"no terminal result (not counted)")
				default:
					trackUsage(res)
					text := strings.TrimSpace(res.AssistantText())
					if strings.HasPrefix(text, "RULES-OK") {
						ginkgo.AddReportEntry("rules behavioural marker (quarantine)",
							"PRESENT: the reply carried the RULES-OK prefix")
					} else {
						ginkgo.AddReportEntry("rules behavioural marker (quarantine)",
							"ABSENT: the reply did not carry the RULES-OK prefix (model adherence to a path-scoped rule, not a harness failure). transcript: "+res.TranscriptPath)
					}
				}
			})
	})
}
