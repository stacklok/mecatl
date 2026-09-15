package app

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

type evidenceBuildProvider struct {
	mu          sync.Mutex
	call        int
	handle      string
	handles     []string
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
			for _, match := range evidenceMetaPattern.FindAllStringSubmatch(message.Text, -1) {
				if len(match) == 3 {
					p.handles = append(p.handles, match[1])
					p.version = match[2]
				}
			}
			if match := reviewIDPattern.FindStringSubmatch(message.Text); len(match) == 2 {
				p.reviewID = match[1]
			}
		}
		if len(p.handles) == 0 || p.version == "" || p.reviewID == "" {
			return nil, fmt.Errorf("review request did not advertise the script evidence binding")
		}
		p.handle = p.handles[0]
		args, _ := json.Marshal(map[string]string{"review_id": p.reviewID, "handle": p.handle, "version": p.version})
		chunks = toolCallChunks(session.NewToolCall("read-evidence", readReviewEvidenceToolName, args))
	case 2:
		for _, message := range req.Messages {
			if message.ToolResult != nil && strings.Contains(message.ToolResult.Content, "SCRIPT_EVIDENCE_MARKER") {
				p.sawContents = true
			}
		}
		if len(p.handles) > 1 {
			args, _ := json.Marshal(map[string]string{"review_id": p.reviewID, "handle": p.handles[1], "version": p.version})
			chunks = toolCallChunks(session.NewToolCall("read-evidence-2", readReviewEvidenceToolName, args))
			break
		}
		chunks = p.submitChunks()
	case 3:
		for _, message := range req.Messages {
			if message.ToolResult != nil && strings.Contains(message.ToolResult.Content, "SCRIPT_EVIDENCE_TAIL") {
				p.sawContents = true
			}
		}
		chunks = p.submitChunks()
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

func (p *evidenceBuildProvider) submitChunks() []port.Chunk {
	if !p.sawContents {
		call := session.NewToolCall("submit-missing", submitReviewAssessmentToolName, json.RawMessage(`{"assessment":"unresolved","concerns":[],"evidence":[],"missing_evidence":[{"ref":"script","kind":"content","handle":"","reason":"script contents missing"}]}`))
		return toolCallChunks(call)
	}
	uses := make([]any, 0, len(p.handles))
	for _, handle := range p.handles {
		uses = append(uses, map[string]any{"handle": handle, "version": p.version, "supports": []string{"call"}})
	}
	args, _ := json.Marshal(map[string]any{"assessment": "acceptable", "concerns": []any{}, "evidence": uses, "missing_evidence": []any{}})
	return toolCallChunks(session.NewToolCall("submit-review", submitReviewAssessmentToolName, args))
}

func toolCallChunks(call session.ToolCall) []port.Chunk {
	return []port.Chunk{{Kind: port.ChunkToolCall, ToolCall: &call}, {Kind: port.ChunkUsage, Usage: &session.Usage{}}, {Kind: port.ChunkDone, Stop: session.StopEndTurn}}
}

type countingEvidenceWorkspace struct {
	tool.Workspace
	stats, reads int
}

func (w *countingEvidenceWorkspace) Stat(ctx context.Context, path string) (tool.FileInfo, error) {
	w.stats++
	return w.Workspace.Stat(ctx, path)
}
func (w *countingEvidenceWorkspace) ReadVersionBounded(ctx context.Context, path string, maxBytes int64) ([]byte, tool.FileVersion, error) {
	w.reads++
	return w.Workspace.(tool.BoundedWorkspaceReader).ReadVersionBounded(ctx, path, maxBytes)
}

func TestEvidenceAuthorizationPrecedesBackendMetadata(t *testing.T) {
	ctx := context.Background()
	base := memfs.NewWorkspace("/review")
	if err := base.Write(ctx, "b", []byte("must-not-be-read")); err != nil {
		t.Fatal(err)
	}
	workspace := &countingEvidenceWorkspace{Workspace: base}
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "review", Revision: "r1"}
	env := tool.MustEnvironment(ref, workspace, reviewerReadLedger{}, nil)
	req := agent.ToolReviewRequest{ReviewID: "review-denied", Job: agent.ReviewJobAction, Event: sessionHookEvent("review-denied"), EffectiveCall: session.NewToolCall("read-a", "Read", json.RawMessage(`{"path":"b"}`)), Caller: agent.ReviewCaller{Role: "worker", Capabilities: []string{"Read"}}, Environment: ref, Capacity: agent.ReviewCapacity{MaxEvidenceHandles: maxReviewEvidenceHandles, MaxEvidenceBytes: maxReviewEvidenceBytes}}
	reviewer := &guardrailActionReviewer{providerID: "provider", modelID: "model"}
	prepared, err := reviewer.PrepareReviewEvidence(ctx, agent.ReviewEvidencePreparation{Request: req, Environment: env, Authorize: func(context.Context, session.ToolCall) error { return errors.New("originating worker lacks Read b") }})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if prepared.Complete || len(prepared.Evidence) != 0 || workspace.stats != 0 || workspace.reads != 0 {
		t.Fatalf("prepared=%+v stats=%d reads=%d; denied candidate touched backend", prepared, workspace.stats, workspace.reads)
	}
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
	prepared, err := reviewer.PrepareReviewEvidence(ctx, agent.ReviewEvidencePreparation{Request: req, Environment: env, Authorize: func(context.Context, session.ToolCall) error { return nil }})
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

type authorityBeforeReviewProvider struct {
	mu                     sync.Mutex
	mainCalls, reviewCalls int
}

func (*authorityBeforeReviewProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}
func (p *authorityBeforeReviewProvider) Stream(_ context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var chunks []port.Chunk
	if strings.Contains(req.System.Render(), contextualReviewerSystemPrompt) {
		p.reviewCalls++
		chunks = []port.Chunk{{Kind: port.ChunkDone, Stop: session.StopEndTurn}}
	} else if p.mainCalls == 0 {
		p.mainCalls++
		chunks = toolCallChunks(session.NewToolCall("grep-denied", "Grep", json.RawMessage(`{"path":"b","pattern":"x"}`)))
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

func TestBuildBoundAuthorityPrecedesContextualReviewer(t *testing.T) {
	ctx := context.Background()
	cfg := guardrailE2ECfg(t, false, PostureAuto, "")
	cfg.GuardrailsRules = []GuardrailRule{{Match: "Grep", Phases: []string{"pre"}, Mode: "block"}}
	setup, err := Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := setup.Service.CreateSession(ctx, session.ModeDefault, session.Limits{MaxTurns: 3})
	if err != nil {
		t.Fatal(err)
	}
	setup.Close()
	store, err := jsonlstore.New(cfg.StoreDir)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	authority, bound := persisted.BoundAuthority()
	if !bound {
		t.Fatal("Build session did not carry bound authority")
	}
	narrowed := authoritySnapshotWithNarrowedTools(t, persisted, authority.CapabilitySet.Tools, "Grep")
	if err := store.Save(ctx, narrowed); err != nil {
		t.Fatal(err)
	}
	provider := &authorityBeforeReviewProvider{}
	cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider { return provider }
	built, err := Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	run, err := built.Service.StartRun(ctx, sess.ID, "grep b")
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	built.Service.FinishRun(sess.ID, run)
	if provider.reviewCalls != 0 {
		t.Fatalf("contextual reviewer calls = %d, want zero before authority denial", provider.reviewCalls)
	}
	if provider.mainCalls != 2 {
		t.Fatalf("main calls = %d, want denied call followed by completion", provider.mainCalls)
	}
}

func TestBuildWiresFiniteScriptEvidenceToContextualReviewer(t *testing.T) {
	cfg := guardrailE2ECfg(t, false, PostureYolo, "./review-script.sh")
	script := "#!/bin/sh\n# SCRIPT_EVIDENCE_MARKER\nexit 0\n" + strings.Repeat("# pad line\n", 2_500) + "# SCRIPT_EVIDENCE_TAIL\n"
	if err := os.WriteFile(cfg.Workspace+"/review-script.sh", []byte(script), 0o700); err != nil {
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
	if len(provider.handles) != 2 || provider.handle == "" || !provider.sawContents || !strings.Contains(provider.prompt, `"EvidenceComplete":true`) {
		t.Fatalf("Build reviewer did not consume both finite script pages: calls=%d handles=%v contents=%v events=%v prompt=%s", provider.call, provider.handles, provider.sawContents, observed, provider.prompt)
	}
	if strings.Contains(provider.prompt, cfg.Workspace) {
		t.Fatalf("checker prompt leaked private environment path %q", cfg.Workspace)
	}
}
