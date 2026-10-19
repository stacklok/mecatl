//go:build e2e

package e2e_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/e2e/harness"
)

// subagentWritableSpecs is the live counterpart to the offline writable-Subagent
// tests: a real model run that delegates a write task with mode:"read-write" and
// asserts the child's nested, guided edit actually LANDS in the parent workspace.
// With direct-write the child writes the REAL parent workspace IN PLACE —
// no fork, no merge — exactly as the main agent does; the nested output file therefore
// appears in the parent tree directly. The writable Subagent is the "delegate one task
// and land its edits" path, so it earns the real-filesystem regression assertion.
//
// The child reaches the nested output instruction only after its Read enters that
// directory and activates the nested AGENTS guidance. Writable mode is default-wired
// (no flag), so nothing special is enabled here.
func subagentWritableSpecs() {
	ginkgo.Describe("subagent writable mode", func() {
		ginkgo.It("uses nested AGENTS guidance to land a mode:read-write subagent's edit in the parent workspace",
			ginkgo.SpecTimeout(6*time.Minute),
			func(ctx ginkgo.SpecContext) {
				// A dedicated mecated over its OWN git-inited scratch tree (git is the
				// rollback layer for the direct-write child). Writable Subagent is
				// default-on, no flag.
				loc, err := harness.NewLocalWith()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "spawning the writable-subagent mecated failed")
				defer func() { _ = loc.Close() }()

				const (
					nestedDir  = "nested-child-guidance"
					inputFile  = "input.txt"
					outputFile = "output.txt"
					marker     = "NESTEDAGENTS-4B9E28C1"
				)
				workspace := loc.Workspace()
				nestedPath := filepath.Join(workspace, nestedDir)
				gomega.Expect(os.MkdirAll(nestedPath, 0o755)).To(gomega.Succeed(), "creating nested guidance directory failed")
				gomega.Expect(os.WriteFile(filepath.Join(nestedPath, inputFile), []byte("nested input\n"), 0o644)).To(gomega.Succeed(),
					"seeding nested input failed")
				gomega.Expect(os.WriteFile(filepath.Join(nestedPath, "AGENTS.md"), []byte("# Nested guidance\n\nAfter reading input.txt, use the Write tool to create output.txt. The file must contain the exact text "+marker+" followed by one newline.\n"), 0o644)).To(gomega.Succeed(),
					"seeding nested AGENTS guidance failed")

				driver := harness.NewDriver(loc)
				report := func(res *harness.RunResult, runErr error) string {
					return harness.Summary(res, runErr, loc.LogTail(4096))
				}

				res, err := driver.Run(ctx, harness.RunOpts{
					Scenario: "subagent-writable", Timeout: 6 * time.Minute,
					ApproveTools: []string{"Subagent"}, // backup; the CLI config allows it
				}, `Use the tool named "Subagent" exactly once, with mode = "read-write" and a prompt that first directs the child to use Read on `+nestedDir+`/`+inputFile+`, then after its successful result directs it to follow the project instructions and use Write on `+nestedDir+`/`+outputFile+`. Call no other tool yourself. When the Subagent tool returns, reply with the single word done.`)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), report(res, err))
				gomega.Expect(res).NotTo(gomega.BeNil(), report(res, err))

				// The parent makes exactly one writable Subagent call and no other tool
				// calls. The child activity is checked from its saved transcript below.
				var parentCalls []client.ToolCallMsg
				for _, msg := range res.Msgs {
					if call, ok := msg.(client.ToolCallMsg); ok {
						parentCalls = append(parentCalls, call)
					}
				}
				gomega.Expect(parentCalls).To(gomega.HaveLen(1), "the parent called tools other than its one Subagent\n"+report(res, err))
				call := parentCalls[0]
				gomega.Expect(call.Name).To(gomega.Equal("Subagent"), "the parent's only tool call was not Subagent\n"+report(res, err))
				var subagentArgs struct {
					Prompt string `json:"prompt"`
					Mode   string `json:"mode"`
				}
				gomega.Expect(json.Unmarshal([]byte(call.Args), &subagentArgs)).To(gomega.Succeed(), "the Subagent arguments were not JSON\n"+report(res, err))
				gomega.Expect(subagentArgs.Mode).To(gomega.Equal("read-write"), "the Subagent was not writable\n"+report(res, err))
				gomega.Expect(subagentArgs.Prompt).NotTo(gomega.ContainSubstring(marker), "the delegated prompt contained nested-only guidance\n"+report(res, err))
				tr := res.ToolResult(call.ID)
				gomega.Expect(tr).NotTo(gomega.BeNil(), report(res, err))
				gomega.Expect(tr.IsError).To(gomega.BeFalse(), "Subagent tool result errored\n"+report(res, err))

				// Correlate the saved child session to this parent call and require a
				// clean child terminal event before inspecting its authoritative history.
				var childID string
				for _, start := range res.SubagentMsgs(client.SubagentStart) {
					if start.ParentCallID == call.ID {
						childID = start.ChildID
					}
				}
				gomega.Expect(childID).NotTo(gomega.BeEmpty(), "no child session correlated to the Subagent call\n"+report(res, err))
				cleanEnd := false
				for _, end := range res.SubagentMsgs(client.SubagentEnd) {
					if end.ParentCallID == call.ID && end.ChildID == childID && !end.IsError && end.Stop == "end_turn" {
						cleanEnd = true
					}
				}
				gomega.Expect(cleanEnd).To(gomega.BeTrue(), "the correlated child did not end cleanly\n"+report(res, err))

				child, transcriptErr := loc.Client().GetSessionTranscript(ctx, childID)
				gomega.Expect(transcriptErr).NotTo(gomega.HaveOccurred(), "loading the child transcript failed\n"+report(res, err))
				gomega.Expect(child.Complete).To(gomega.BeTrue(), "the child transcript was not complete\n"+report(res, err))
				gomega.Expect(child.Relationship.ParentSessionID).To(gomega.Equal(res.SessionID), "the child transcript has the wrong parent session\n"+report(res, err))
				gomega.Expect(child.Relationship.CallID).To(gomega.Equal(call.ID), "the child transcript has the wrong parent call\n"+report(res, err))

				// Only the intended nested Read and Write are allowed. In particular,
				// this rejects an explicit AGENTS.md read or a Shell/search/list workaround
				// that could discover the marker without scoped guidance.
				readResult := -1
				readCalls := map[string]int{}
				var writeAssistants []int
				for messageIndex, message := range child.Messages {
					if message.Role != "assistant" {
						continue
					}
					for _, childCall := range message.ToolCalls {
						var args struct {
							Path string `json:"path"`
						}
						gomega.Expect(json.Unmarshal([]byte(childCall.Args), &args)).To(gomega.Succeed(),
							"child tool arguments were not JSON\n"+report(res, err))
						path := args.Path
						if !filepath.IsAbs(path) {
							path = filepath.Join(workspace, path)
						}
						path = filepath.Clean(path)
						rel, relErr := filepath.Rel(workspace, path)
						gomega.Expect(relErr).NotTo(gomega.HaveOccurred(), "child tool path could not be normalized\n"+report(res, err))
						gomega.Expect(rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))).To(gomega.BeFalse(),
							"child tool path escaped the workspace\n"+report(res, err))

						isInputRead := childCall.Name == "Read" && path == filepath.Join(nestedPath, inputFile)
						isOutputWrite := childCall.Name == "Write" && path == filepath.Join(nestedPath, outputFile)
						gomega.Expect(isInputRead || isOutputWrite).To(gomega.BeTrue(),
							"child used an unexpected discovery or write tool\n"+report(res, err))
						if isInputRead {
							readCalls[childCall.ID] = messageIndex
						}
						if isOutputWrite {
							writeAssistants = append(writeAssistants, messageIndex)
						}
					}
				}
				for messageIndex, message := range child.Messages {
					if message.ToolResult == nil {
						continue
					}
					gomega.Expect(message.Role).To(gomega.Equal("tool"), "child tool result has the wrong role\n"+report(res, err))
					assistantIndex, isInputRead := readCalls[message.ToolResult.CallID]
					if isInputRead && !message.ToolResult.IsError && messageIndex > assistantIndex && readResult == -1 {
						readResult = messageIndex
						gomega.Expect(message.ToolResult.Content).NotTo(gomega.ContainSubstring(marker), "the successful input Read result contained nested-only guidance\n"+report(res, err))
					}
				}
				gomega.Expect(readResult).To(gomega.BeNumerically(">=", 0), "child did not successfully receive the nested Read result\n"+report(res, err))
				gomega.Expect(writeAssistants).NotTo(gomega.BeEmpty(), "child did not write the nested output\n"+report(res, err))
				for _, writeAssistant := range writeAssistants {
					gomega.Expect(writeAssistant).To(gomega.BeNumerically(">", readResult), "child Write was not in a later assistant message after the successful Read\n"+report(res, err))
				}

				// The regression assertion: the child wrote the real parent workspace in
				// place, following the nested guidance discovered by its Read.
				got, readErr := os.ReadFile(filepath.Join(nestedPath, outputFile))
				gomega.Expect(readErr).NotTo(gomega.HaveOccurred(),
					"the writable subagent's nested file is absent from the parent workspace — the direct-write edit did not land\n"+report(res, err))
				gomega.Expect(string(got)).To(gomega.Equal(marker+"\n"),
					"the nested guidance output is wrong — got %q\n%s", got, report(res, err))
			})
	})
}
