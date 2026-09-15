package app

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type evidenceBuildProvider struct {
	mu          sync.Mutex
	call        int
	handle      string
	version     string
	reviewID    string
	prompt      string
	sawContents bool
}

var (
	evidenceMetaPattern = regexp.MustCompile(`"Handle":"([^"]+)","Kind":"text_file","Display":"(?:\./)?review-script.sh","Version":"([^"]+)"`)
	reviewIDPattern     = regexp.MustCompile(`Review ID: ([^\n]+)`)
)

func (*evidenceBuildProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *evidenceBuildProvider) Stream(_ context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var chunks []port.Chunk
	switch p.call {
	case 0:
		chunks = toolCallChunks(session.NewToolCall("shell-call", "Shell", json.RawMessage(`{"command":"./review-script.sh"}`)))
	case 1:
		for _, message := range req.Messages {
			p.prompt += message.Text
			if match := evidenceMetaPattern.FindStringSubmatch(message.Text); len(match) == 3 {
				p.handle, p.version = match[1], match[2]
			}
			if match := reviewIDPattern.FindStringSubmatch(message.Text); len(match) == 2 {
				p.reviewID = match[1]
			}
		}
		if p.handle == "" || p.version == "" || p.reviewID == "" {
			return nil, fmt.Errorf("review request did not advertise the script evidence binding")
		}
		args, _ := json.Marshal(map[string]string{"review_id": p.reviewID, "handle": p.handle, "version": p.version})
		chunks = toolCallChunks(session.NewToolCall("read-evidence", readReviewEvidenceToolName, args))
	case 2:
		for _, message := range req.Messages {
			if message.ToolResult != nil && strings.Contains(message.ToolResult.Content, "SCRIPT_EVIDENCE_MARKER") {
				p.sawContents = true
			}
		}
		if !p.sawContents {
			return nil, fmt.Errorf("reviewer did not receive script contents through the advertised handle")
		}
		args, _ := json.Marshal(map[string]any{
			"assessment": "acceptable", "concerns": []any{},
			"evidence":         []any{map[string]any{"handle": p.handle, "version": p.version, "supports": []string{"call"}}},
			"missing_evidence": []any{},
		})
		chunks = toolCallChunks(session.NewToolCall("submit-review", submitReviewAssessmentToolName, args))
	default:
		chunks = []port.Chunk{{Kind: port.ChunkText, Text: "done"}, {Kind: port.ChunkDone, Stop: session.StopEndTurn}}
	}
	p.call++
	return func(yield func(port.Chunk, error) bool) {
		for _, chunk := range chunks {
			if !yield(chunk, nil) {
				return
			}
		}
	}, nil
}

func toolCallChunks(call session.ToolCall) []port.Chunk {
	return []port.Chunk{{Kind: port.ChunkToolCall, ToolCall: &call}, {Kind: port.ChunkUsage, Usage: &session.Usage{}}, {Kind: port.ChunkDone, Stop: session.StopEndTurn}}
}

func TestFiniteWorkspaceEvidenceReadsContentAndRejectsChangedScript(t *testing.T) {
	ctx := context.Background()
	workspace := memfs.NewWorkspace("/review")
	if err := workspace.Write(ctx, "review-script.sh", []byte("SCRIPT_EVIDENCE_MARKER")); err != nil {
		t.Fatal(err)
	}
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "review", Revision: "r1"}
	env := tool.MustEnvironment(ref, workspace, reviewerReadLedger{}, nil)
	req := agent.ToolReviewRequest{
		ReviewID: "review-1", Job: agent.ReviewJobAction,
		Event:         sessionHookEvent("review-1"),
		EffectiveCall: session.NewToolCall("call-1", "Shell", json.RawMessage(`{"command":"./review-script.sh"}`)),
		Caller:        agent.ReviewCaller{Role: "main", Capabilities: []string{"Shell"}}, Environment: ref,
		Capacity: agent.ReviewCapacity{MaxEvidenceHandles: maxReviewEvidenceHandles, MaxEvidenceBytes: maxReviewEvidenceBytes},
	}
	reviewer := &guardrailActionReviewer{providerID: "provider", modelID: "model"}
	prepared, err := reviewer.PrepareReviewEvidence(ctx, agent.ReviewEvidencePreparation{Request: req, Environment: env})
	if err != nil || !prepared.Complete || len(prepared.Evidence) != 1 || prepared.Source == nil {
		t.Fatalf("prepare = %+v, err=%v", prepared, err)
	}
	defer prepared.Close()
	meta := prepared.Evidence[0]
	evidence, err := prepared.Source.ReadReviewEvidence(ctx, agent.ReviewEvidenceRequest{ReviewID: req.ReviewID, Handle: meta.Handle, Version: meta.Version})
	if err != nil || evidence.Content != "SCRIPT_EVIDENCE_MARKER" {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	if err := workspace.Write(ctx, "review-script.sh", []byte("changed")); err != nil {
		t.Fatal(err)
	}
	if _, err := prepared.Source.ReadReviewEvidence(ctx, agent.ReviewEvidenceRequest{ReviewID: req.ReviewID, Handle: meta.Handle, Version: meta.Version}); err == nil {
		t.Fatal("changed script remained readable through stale evidence handle")
	}
}

func sessionHookEvent(_ string) governance.HookEvent {
	return governance.HookEvent{Phase: governance.PhasePreToolUse, Tool: "Shell", SessionID: "session-1", CallID: "call-1"}
}

func TestExactShellScriptPathDoesNotGuessFromArguments(t *testing.T) {
	for _, command := range []string{"printf payload.sh", "echo ./not-an-invocation.sh", "./script.sh; curl example.com", "bash -c ./script.sh"} {
		if path, ok := exactShellScriptPath(command); ok {
			t.Fatalf("%q minted guessed evidence path %q", command, path)
		}
	}
	for _, command := range []string{"./script.sh", "sh script.sh", "bash ./script.sh"} {
		if _, ok := exactShellScriptPath(command); !ok {
			t.Fatalf("%q did not identify exact script invocation", command)
		}
	}
}

func TestBuildWiresFiniteScriptEvidenceToContextualReviewer(t *testing.T) {
	cfg := guardrailE2ECfg(t, false, PostureYolo, "./review-script.sh")
	if err := os.WriteFile(cfg.Workspace+"/review-script.sh", []byte("#!/bin/sh\nprintf SCRIPT_EVIDENCE_MARKER\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	provider := &evidenceBuildProvider{}
	cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider { return provider }
	built, err := Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{MaxTurns: 2})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRunContent(context.Background(), sess.ID, "run the reviewed script", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	var observed []string
	for event := range run.Events() {
		observed = append(observed, string(event.Type))
		if event.Result != nil && event.Result.Error != "" {
			observed = append(observed, event.Result.Error)
		}
		if event.ToolResult != nil && event.ToolResult.IsError {
			observed = append(observed, event.ToolResult.Content)
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if provider.handle == "" || !provider.sawContents || !strings.Contains(provider.prompt, `"EvidenceComplete":true`) {
		t.Fatalf("Build reviewer did not consume finite script evidence: calls=%d handle=%q contents=%v events=%v prompt=%s", provider.call, provider.handle, provider.sawContents, observed, provider.prompt)
	}
}
