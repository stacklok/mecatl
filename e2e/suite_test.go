//go:build e2e

// Package e2e_test is mecatl's LIVE end-to-end suite: it spawns ./bin/mecated
// against OpenRouter (or dials MECATL_E2E_TARGET) and drives real model runs
// over the gRPC Converse stream through cmd/mecatui/client — the same wire path
// the TUI uses. Run it with `task e2e` (requires OPENROUTER_API_KEY). Every
// scenario writes a JSONL transcript artifact under the suite's scratch root.
//
// LAYOUT NOTE: the feature specs live in this package (e2e/*_test.go) rather
// than a separate e2e/features/ package: ginkgo randomizes top-level containers
// and a second package would need its own suite bootstrap + its own server, so
// the suite registers every feature through ONE top-level Ordered container
// (provider smoke first — the canary that gates the rest — and metrics after
// the delegation scenarios, which its assertions depend on).
package e2e_test

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/e2e/harness"
)

// TIMEOUT ARCHITECTURE: three nested bounds, outermost last to fire.
//
//  1. Per-RUN: the driver's RunOpts.Timeout (cancel + bounded drain → the
//     TIMEOUT classification in the failure report, transcript intact).
//  2. Per-SPEC: a ginkgo SpecTimeout = driver timeout + 30s on every spec that
//     drives a run (the body takes a SpecContext, so the driver's ctx is
//     cancelled and the run still ends through the clean path). FlakeAttempts
//     specs get a FRESH SpecTimeout per attempt (verified against ginkgo
//     v2.30.0 internal/group.go attemptSpec).
//  3. Suite: go test -timeout 60m (Taskfile). Worst-case spec wall time =
//     Σ(driver timeout × attempts): provider 2m×2×2 + skills 3m×2×2 +
//     memory 2m×2×2 + subagents 6m + parallel 6m + teams 8m + soul 2m +
//     approve-after-kill 4m×2 ≈ 58m — under 60m, and the canary gate skips
//     everything after a dead default lane, so the pathological all-timeout case
//     cannot stack.
//
// The go-test panic path (no AfterSuite, no ledger) is therefore unreachable
// short of a harness deadlock — which Pdeathsig in harness.Local guards the
// spawned mecated against.

func TestE2E(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "mecatl live e2e suite")
}

var (
	target harness.Target
	driver *harness.Driver

	// last run per spec, for the failure report.
	lastRun *harness.RunResult
	lastErr error

	// canary gate: the provider smoke must pass before anything else runs.
	canaryDone, canaryOK bool

	// subagentChildActivity is set by the subagents spec when it actually
	// observed clean child runs. The metrics spec gates its role="subagent"
	// assertion on it, so a model-behaviour failure in the subagents scenario
	// is reported ONCE, not double-reported as a missing metrics series too.
	subagentChildActivity bool

	// usage ledger for the end-of-suite cost estimate.
	usageMu      sync.Mutex
	usageByModel = map[string]client.Usage{}
)

var _ = ginkgo.BeforeSuite(func() {
	if os.Getenv("MECATL_E2E_TARGET") == "" && os.Getenv("OPENROUTER_API_KEY") == "" {
		ginkgo.AbortSuite("OPENROUTER_API_KEY is not set.\n\n" +
			"The live e2e suite drives real model runs through OpenRouter. Export\n" +
			"OPENROUTER_API_KEY in your shell (e.g. from your key file; never on a\n" +
			"logged command line) and re-run `task e2e` — see e2e/README.md.\n\n" +
			"Or point the suite at an existing server with MECATL_E2E_TARGET=host:port.")
	}
	t, err := harness.NewTarget()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "building the e2e target failed")
	target = t
	driver = harness.NewDriver(target)
})

var _ = ginkgo.AfterSuite(func() {
	reportUsage()
	if target != nil {
		gomega.Expect(target.Close()).To(gomega.Succeed())
	}
})

// The ONE ordered root container: provider smoke gates everything; metrics runs
// after the delegation scenarios it asserts on. ContinueOnFailure keeps a
// mid-suite scenario failure from skipping the rest (the canary gate below is
// the only deliberate skip).
var _ = ginkgo.Describe("mecatl live e2e", ginkgo.Ordered, ginkgo.Serial, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeEach(func() {
		if canaryDone && !canaryOK &&
			!slices.Contains(ginkgo.CurrentSpecReport().Labels(), "canary") {
			ginkgo.Skip("provider canary failed — the OpenRouter lane is down (key/network/provider); skipping the dependent scenarios")
		}
	})

	// Failure report: attach the self-diagnosing summary (classification +
	// transcript path + stderr tail) to every failing spec.
	ginkgo.JustAfterEach(func() {
		if ginkgo.CurrentSpecReport().Failed() && (lastRun != nil || lastErr != nil) {
			ginkgo.AddReportEntry("e2e failure report",
				harness.Summary(lastRun, lastErr, target.LogTail(4096)))
		}
		lastRun, lastErr = nil, nil
	})

	providerSpecs()
	skillSpecs()
	subagentSpecs()
	dirtyForkSpecs()
	parallelSpecs()
	teamSpecs()
	metricsSpecs()
	memorySpecs()
	compactionSpecs()
	modelSlotSpecs()
	modelRouterSpecs()
	modeModelSpecs()
	webSearchSpecs()
	soulSpecs()
	approveAfterKillSpecs()
	snapshotFidelitySpecs()
	verdictReplaySpecs()
	mecatequiSpecs()
})

// runScenario drives one prompt and fails the spec (with the full failure
// report) on transport-level errors. Event/side-effect assertions stay in the
// specs. ctx is the spec's SpecContext: when the spec's SpecTimeout fires the
// driver cancels the run and drains, so the transcript + TIMEOUT
// classification land before ginkgo moves on.
func runScenario(ctx context.Context, opts harness.RunOpts, prompt string) *harness.RunResult {
	ginkgo.GinkgoHelper()
	res, err := driver.Run(ctx, opts, prompt)
	lastRun, lastErr = res, err
	if res != nil {
		trackUsage(res)
	}
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), failureReport())
	gomega.Expect(res.Result).NotTo(gomega.BeNil(), failureReport())
	return res
}

// failureReport renders the current run's self-diagnosing summary.
func failureReport() string {
	return harness.Summary(lastRun, lastErr, target.LogTail(4096))
}

// trackUsage accumulates the run's own usage plus the delegation families'
// child usage (children run their own sessions, so their tokens are NOT in the
// parent result's usage).
func trackUsage(res *harness.RunResult) {
	model := res.Resolved.ModelID
	if model == "" {
		model = harness.DefaultModel()
	}
	add := func(u client.Usage) {
		usageMu.Lock()
		defer usageMu.Unlock()
		cur := usageByModel[model]
		cur.InputTokens += u.InputTokens
		cur.OutputTokens += u.OutputTokens
		cur.CacheReadTokens += u.CacheReadTokens
		cur.CacheWriteTokens += u.CacheWriteTokens
		usageByModel[model] = cur
	}
	add(res.Usage())
	for _, s := range res.SubagentMsgs(client.SubagentEnd) {
		add(s.Usage)
	}
	for _, t := range res.TeamMsgs(client.TeamEnd) {
		add(t.Usage)
	}
	for _, p := range res.ParallelMsgs(client.ParallelEnd) {
		add(p.Usage)
	}
}

// modelPricing is the per-token USD price table for the two default lanes
// (verified against OpenRouter's /models at implementation time). Unknown
// models report tokens only.
var modelPricing = map[string][2]float64{ // {prompt, completion} USD per token
	"openai/gpt-4.1-mini":        {0.40e-6, 1.60e-6},
	"openai/gpt-4o-mini":         {0.15e-6, 0.60e-6},
	"anthropic/claude-3.5-haiku": {0.80e-6, 4.00e-6},
}

// reportUsage prints the cumulative token usage + a cost ESTIMATE per model.
func reportUsage() {
	usageMu.Lock()
	defer usageMu.Unlock()
	if len(usageByModel) == 0 {
		return
	}
	out := "live e2e usage (cumulative, incl. delegation children):\n"
	var total float64
	for model, u := range usageByModel {
		line := fmt.Sprintf("  %-32s in=%-8d out=%-8d cache_read=%-8d", model, u.InputTokens, u.OutputTokens, u.CacheReadTokens)
		if p, ok := modelPricing[model]; ok {
			cost := float64(u.InputTokens)*p[0] + float64(u.OutputTokens)*p[1]
			total += cost
			line += fmt.Sprintf("  ~$%.4f", cost)
		}
		out += line + "\n"
	}
	out += fmt.Sprintf("  estimated total: ~$%.4f\n", total)
	ginkgo.AddReportEntry("e2e usage and cost estimate", out)
	fmt.Fprint(ginkgo.GinkgoWriter, out)
}
