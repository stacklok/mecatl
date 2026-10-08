package app

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

func TestRemoteAndCustomPathLabelsDoNotMintLocalEvidenceOrTargets(t *testing.T) {
	for _, name := range []string{"mcp__remote__read", "CallMcpWithQuery", "Subagent", "custom_tool"} {
		call := session.NewToolCall("c", name, json.RawMessage(`{"path":"credentials.txt","source":"a","destination":"b"}`))
		if paths := reviewEvidencePaths(call); len(paths) != 0 {
			t.Fatalf("%s minted local evidence paths %v", name, paths)
		}
	}
	for name, args := range map[string]string{
		"Read": `{"path":"a"}`,
		"Copy": `{"source":"a","destination":"b"}`,
		"Move": `{"source":"a","destination":"b"}`,
	} {
		if paths := tool.LocalFileOperands(name, json.RawMessage(args)); len(paths) == 0 {
			t.Fatalf("%s lost local operands", name)
		}
	}
}

func TestNonTextResultEvidenceIsExplicitlyIncompleteAndNeverBase64Text(t *testing.T) {
	image := session.ToolResult{Content: "summary", Parts: []session.Content{{BlockKind: session.BlockImage, Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}}}}
	content, complete := reviewableResultContent(image)
	if complete || content != "summary" || strings.Contains(content, "iVBOR") {
		t.Fatalf("content=%q complete=%v", content, complete)
	}
	req := completeReviewRequest()
	binding := completeEvidenceBinding(req)
	candidate := reviewEvidenceCandidate{Kind: "tool_result", Version: "v1", Complete: false, Authorized: true, Binding: binding, Backend: memoryReviewEvidenceBackend{content: content}}
	_, metas, inventoryComplete := newFiniteReviewEvidenceSource(context.Background(), binding, []reviewEvidenceCandidate{candidate}, agent.ReviewCapacity{MaxEvidenceHandles: 16, MaxEvidenceBytes: 400_000})
	if inventoryComplete || len(metas) != 1 || metas[0].Complete {
		t.Fatalf("metas=%+v complete=%v", metas, inventoryComplete)
	}
}

func TestPagedWorkspaceEvidenceRejectsVersionChangeMidChain(t *testing.T) {
	ctx := context.Background()
	workspace := memfs.NewWorkspace("/paged")
	original := []byte(strings.Repeat("line\n", 6_000))
	if err := workspace.Write(ctx, "script.sh", original); err != nil {
		t.Fatal(err)
	}
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "paged", Revision: "v1"}
	req := completeReviewRequest()
	req.Environment = ref
	req.Event.SessionID = "paged-session"
	req.EffectiveCall = session.NewToolCall("call", tool.ShellToolName, json.RawMessage(`{"command":"./script.sh"}`))
	binding := completeEvidenceBinding(req)
	binding.Environment = ref
	binding.SessionID = "paged-session"
	data, version, total, err := workspace.ReadVersionRangeBounded(ctx, "script.sh", 0, maxReviewEvidenceRead, maxReviewEvidenceBytes)
	if err != nil || len(data) == 0 {
		t.Fatal(err)
	}
	encoded, err := tool.EncodeFileVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	backend := workspaceReviewEvidenceBackend{reader: workspace, path: "script.sh", size: total, version: encoded, authorize: func(context.Context) error { return nil }}
	source, metas, complete := newFiniteReviewEvidenceSource(ctx, binding, []reviewEvidenceCandidate{{Kind: "text_file", Display: "script.sh", Version: encoded, Complete: true, Authorized: true, Binding: binding, Backend: backend}}, req.Capacity)
	if !complete || len(metas) < 2 {
		t.Fatalf("metas=%+v complete=%v", metas, complete)
	}
	if _, err := source.ReadReviewEvidence(ctx, agent.ReviewEvidenceRequest{ReviewID: req.ReviewID, Handle: metas[0].Handle, Version: encoded}); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Write(ctx, "script.sh", []byte(strings.Repeat("changed\n", 6_000))); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ReadReviewEvidence(ctx, agent.ReviewEvidenceRequest{ReviewID: req.ReviewID, Handle: metas[1].Handle, Version: encoded}); err == nil {
		t.Fatal("changed source remained readable through continuation handle")
	}
}

type mediaRepairProvider struct{ mainCalls, reviewCalls int }

func (*mediaRepairProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}
func (p *mediaRepairProvider) Stream(_ context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	var chunks []port.Chunk
	if strings.Contains(req.System.Render(), contextualReviewerSystemPrompt) {
		p.reviewCalls++
		chunks = []port.Chunk{{Kind: port.ChunkText, Text: `{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`}, {Kind: port.ChunkDone, Stop: session.StopEndTurn}}
	} else if p.mainCalls == 0 {
		p.mainCalls++
		chunks = toolCallChunks(session.NewToolCall("image-call", "mcp__typed__good_image", json.RawMessage(`{}`)))
	} else {
		p.mainCalls++
		chunks = []port.Chunk{{Kind: port.ChunkText, Text: "done"}, {Kind: port.ChunkDone, Stop: session.StopEndTurn}}
	}
	return func(yield func(port.Chunk, error) bool) {
		for _, chunk := range chunks {
			if !yield(chunk, nil) {
				return
			}
		}
	}, nil
}

func TestBuildInboundImageCannotBeBlindlyReleasedByTextOnlyChecker(t *testing.T) {
	provider := &mediaRepairProvider{}
	cfg := guardrailE2ECfg(t, true, PostureAuto, "")
	cfg.MCPServers = []mcp.ServerConfig{{Name: "typed", URL: newTypedResultsMCPServer(t)}}
	cfg.GuardrailsRules = []GuardrailRule{{Match: "mcp__typed__good_image", Phases: []string{"post"}, Mode: "block"}}
	cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider { return provider }
	built, err := Build(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{MaxTurns: 2})
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRunContent(context.Background(), sess.ID, "fetch image", nil)
	if err != nil {
		t.Fatal(err)
	}
	asks := 0
	var released *session.ToolResult
	for event := range run.Events() {
		if event.Ask != nil && event.Ask.Guardrail != nil {
			asks++
			if event.Ask.Guardrail.Kind != session.GuardrailApprovalResultRelease {
				t.Fatalf("unexpected guardrail ask %+v", event.Ask.Guardrail)
			}
			if err := run.ResolveApproval(agent.ApprovalResolution{AskID: event.Ask.AskID, ReviewID: event.Ask.Guardrail.ReviewID, Kind: event.Ask.Guardrail.Kind, Verdict: session.VerdictAllowOnce}); err != nil {
				t.Fatal(err)
			}
		}
		if event.ToolResult != nil && event.ToolResult.CallID == "image-call" {
			result := *event.ToolResult
			released = &result
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if asks != 1 || provider.reviewCalls == 0 || released == nil || len(released.Parts) != 1 || released.Parts[0].BlockKind != session.BlockImage {
		t.Fatalf("asks=%d reviews=%d released=%+v", asks, provider.reviewCalls, released)
	}
}
