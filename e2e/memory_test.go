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

// memorySpecs covers scenarios 8 + 9: the cross-project user model
// (RememberUser → <user-model-dir>/memory.json) and the per-project memory
// (Remember → <memory-dir>/memory.json). Both tools are floor-scoped Allows, so
// no ask round-trip is involved. The file-level side-effect assertion is
// local-target only (Skip on remote).
func memorySpecs() {
	ginkgo.Describe("memory", func() {
		remember := func(ctx ginkgo.SpecContext, scenario, toolName, prompt, fileMustContain string, dir harness.StateKind) {
			ginkgo.GinkgoHelper()
			res := runScenario(ctx, harness.RunOpts{Scenario: scenario, Timeout: 2 * time.Minute}, prompt)

			calls := res.ToolCalls(toolName)
			gomega.Expect(calls).NotTo(gomega.BeEmpty(),
				"no "+toolName+" tool.call observed\n"+failureReport())
			ok := false
			for _, c := range calls {
				if tr := res.ToolResult(c.ID); tr != nil && !tr.IsError {
					ok = true
				}
			}
			gomega.Expect(ok).To(gomega.BeTrue(),
				toolName+" never resolved without error\n"+failureReport())

			stateDir := target.StateDir(dir)
			if stateDir == "" {
				ginkgo.Skip("remote target: cannot read the server-side store file")
			}
			path := filepath.Join(stateDir, "memory.json")
			gomega.Eventually(func() string {
				data, _ := os.ReadFile(path)
				return string(data)
			}, 10*time.Second, 500*time.Millisecond).Should(
				gomega.ContainSubstring(fileMustContain),
				"the stored fact never appeared in "+path+"\n"+failureReport())
		}

		// PROMPT PHRASING NOTE: probe-verified clean of the upstream
		// prompt-filter triggers ("durable fact about me: my name is" +
		// trailing "Do not call any other tools" deterministically
		// content_filtered — see e2e/README.md).
		ginkgo.It("stores a user-model fact via RememberUser",
			ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(150*time.Second),
			func(ctx ginkgo.SpecContext) {
				remember(ctx, "memory-user", "RememberUser",
					`Store one durable operator fact with the RememberUser tool: the operator goes by the name Ozz. Then reply with the single word done. Call no other tool.`,
					"Ozz", harness.StateUserModel)
			})

		ginkgo.It("stores a project memory via Remember",
			ginkgo.FlakeAttempts(2), ginkgo.SpecTimeout(150*time.Second),
			func(ctx ginkgo.SpecContext) {
				remember(ctx, "memory-project", "Remember",
					`Store one project fact with the Remember tool: the project codename is quetzal. Then reply with the single word done. Call no other tool.`,
					"quetzal", harness.StateMemory)
			})
	})
}
