package acp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/acp"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type attachmentTool struct {
	scriptTool
	runs atomic.Int32
}

func (t *attachmentTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.runs.Add(1)
	return session.NewToolResult(call.ID, "new work executed"), nil
}

type attachmentPolicy struct {
	port.PermissionPolicy
	learns atomic.Int32
}

func (p *attachmentPolicy) Learn(id session.SessionID, call session.ToolCall) {
	p.learns.Add(1)
	p.PermissionPolicy.Learn(id, call)
}

type attachmentLearning struct{ calls atomic.Int32 }

func (l *attachmentLearning) Observe(context.Context, learning.Trajectory) error {
	l.calls.Add(1)
	return nil
}

type attachmentBroker struct{ calls atomic.Int32 }

func (b *attachmentBroker) AttachSession(context.Context, session.SessionID) (brokercontract.Attachment, brokercontract.AttachOutcome, error) {
	b.calls.Add(1)
	return nil, "", brokercontract.ErrStateUnavailable
}
func (b *attachmentBroker) DeleteSession(context.Context, session.SessionID) (brokercontract.DeleteOutcome, error) {
	b.calls.Add(1)
	return brokercontract.DeleteDeleted, nil
}

type attachmentCommands struct {
	fakeLister
	activations atomic.Int32
	lists       atomic.Int32
}

func (c *attachmentCommands) Activate(context.Context, session.SessionID, *session.Principal, string) error {
	c.activations.Add(1)
	return nil
}
func (c *attachmentCommands) Borrow(context.Context, session.SessionID, *session.Principal, string) (server.CommandSourceBinding, func(), error) {
	return c, func() {}, nil
}
func (c *attachmentCommands) List(ctx context.Context) ([]prompt.Command, error) {
	c.lists.Add(1)
	return c.fakeLister.List(ctx)
}

type attachmentMCP struct {
	initializes atomic.Int32
	lists       atomic.Int32
	calls       atomic.Int32
}

func newAttachmentMCP(t *testing.T) (string, *attachmentMCP) {
	t.Helper()
	counts := &attachmentMCP{}
	remote := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "attachment-fixture", Version: "1"}, nil)
	remote.AddTool(&mcpsdk.Tool{Name: "probe", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		counts.calls.Add(1)
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "probe"}}}, nil
	})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return remote }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, "read fixture request", http.StatusBadRequest)
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(data))
			var request struct{ Method string }
			if err := json.Unmarshal(data, &request); err != nil {
				http.Error(w, "decode fixture request", http.StatusBadRequest)
				return
			}
			switch request.Method {
			case "initialize":
				counts.initializes.Add(1)
			case "tools/list":
				counts.lists.Add(1)
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(httpServer.Close)
	return httpServer.URL, counts
}

// Keep the old bytes in the actual store so session/load, not just fixture setup,
// exercises the decoding/migration boundary on its first read.
func abandonedAttachmentStore(t *testing.T, name string, ref session.EnvironmentRef) (*jsonlstore.Store, *session.Session) {
	t.Helper()
	fixture := name
	if name == "legacy-unproven-authorization" {
		fixture = "legacy-authorizing"
	}
	raw, err := os.ReadFile("../../../engine/adapter/sessnap/testdata/" + fixture + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if name == "legacy-unproven-authorization" {
		raw = bytes.Replace(raw, []byte(`"id": "provider-auth-call"`), []byte(`"id": "missing-control-call"`), 1)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	wire["environment_ref"], err = json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	wire["kind"], wire["mode"] = json.RawMessage(`"main"`), json.RawMessage(`"default"`)
	if strings.HasPrefix(name, "legacy-") {
		var messages []json.RawMessage
		if err := json.Unmarshal(wire["messages"], &messages); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, json.RawMessage(`{"role":"tool","tool_result":{"CallID":"missing-orphan","Content":"standalone prior result","Parts":[{"Kind":"image","MIMEType":"image/png","Data":"AQID"}]}}`))
		wire["messages"], err = json.Marshal(messages)
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err = json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := sessnap.Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.State != session.StateIdle {
		t.Fatalf("fixture did not abandon continuation: %s", migrated.State)
	}
	dir := t.TempDir()
	store, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), migrated); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(dir, "sid-v1", "*.session.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("snapshot paths: %v %v", paths, err)
	}
	current, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(current, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["snapshot"] = raw
	current, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[0], current, 0600); err != nil {
		t.Fatal(err)
	}
	return store, migrated
}

func TestSessionLoadAbandonedContinuationAttachesWithoutResuming(t *testing.T) {
	for _, fixture := range []string{"legacy-ambiguous-pending", "legacy-unproven-authorization", "candidate-held"} {
		for _, setupFailure := range []bool{false, true} {
			name := fixture + "/attached"
			if setupFailure {
				name = fixture + "/setup-error"
			}
			t.Run(name, func(t *testing.T) {
				root := resolvedTempDir(t)
				ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "attachment-fixture", Revision: "1"}
				store, migrated := abandonedAttachmentStore(t, fixture, ref)
				before, err := sessnap.Marshal(migrated)
				if err != nil {
					t.Fatal(err)
				}
				url, remote := newAttachmentMCP(t)
				write := &attachmentTool{scriptTool: scriptTool{name: "Write"}}
				read := &attachmentTool{scriptTool: scriptTool{name: "Read", readOnly: true}}
				shell := &attachmentTool{scriptTool: scriptTool{name: "Shell"}}
				llm := mockllm.New(mockllm.ToolCallTurn(call("fresh-provider", "Write", `{"path":"old.txt"}`)), mockllm.TextTurn("new prompt completed"))
				grants := permstore.New()
				policy := &attachmentPolicy{PermissionPolicy: permpolicy.NewPolicy(nil, grants)}
				learningObserver := &attachmentLearning{}
				broker := &attachmentBroker{}
				commands := &attachmentCommands{fakeLister: fakeLister{cmds: []server.Command{{Name: "review", Description: "Review attached workspace"}}}}
				var mounts, replays, reattaches atomic.Int32
				build := func(remoteTools []tool.Tool) *agent.Engine {
					catalog := tool.NewCatalog()
					for _, candidate := range append([]tool.Tool{write, read, shell}, remoteTools...) {
						catalog.MustRegister(candidate)
					}
					return agent.NewEngine(agent.Deps{LLM: llm, Catalog: catalog, Policy: policy, Store: store,
						LearningMode: learning.Review, LearningObserver: learningObserver})
				}
				setupErr := errors.New("synthetic attachment factory failure")
				factory := func(ctx context.Context, _ server.ProviderSelector, specs []mcp.ServerConfig, _ server.SessionProfile, workspace string, _ session.PermissionMode) (server.SessionEngineResult, error) {
					mounts.Add(1)
					if len(specs) != 1 || specs[0].URL != url || workspace != root {
						return server.SessionEngineResult{}, errors.New("incorrect attachment factory inputs")
					}
					mgr, err := mcp.NewManager(ctx, specs, nil, port.NopDiagnostics{})
					if err != nil {
						return server.SessionEngineResult{}, err
					}
					if setupFailure {
						_ = mgr.Close()
						return server.SessionEngineResult{}, setupErr
					}
					return server.SessionEngineResult{Engine: build(mgr.Tools()), Close: mgr.Close}, nil
				}
				svc := newServiceCfg(t, llm, nil, func(c *server.Config) {
					c.Engine, c.Store, c.EventLog = build(nil), store, store
					c.SharedEngineRoot, c.PlacementScope = root, "acp-test"
					c.PlacementProvider = acpPlacementProvider{root: root, ref: ref, reattachCalls: &reattaches}
					c.Commands, c.MCPBroker = commands, broker
					c.ReplayApprovals = func(context.Context, *session.Session) { replays.Add(1) }
					c.SessionEngine = factory
					c.SessionEngineWithTools = func(ctx context.Context, sel server.ProviderSelector, specs []mcp.ServerConfig, profile server.SessionProfile, workspace string, mode session.PermissionMode, _ []tool.Tool, _ []string) (server.SessionEngineResult, error) {
						return factory(ctx, sel, specs, profile, workspace, mode)
					}
				})
				defer svc.Close()
				e, cleanup := startAgent(t, svc, acp.WithResume(true))
				defer cleanup()
				params := map[string]any{"sessionId": string(migrated.ID), "cwd": root,
					"mcpServers": []any{map[string]any{"type": "http", "name": "attached", "url": url}}}
				cycles := 2
				if setupFailure {
					cycles = 1
				}
				for cycle := range cycles {
					if setupFailure {
						failure := e.callErr("session/load", params)
						if !bytes.Contains(failure, []byte(setupErr.Error())) || !bytes.Contains(failure, []byte(`"code":-32602`)) {
							t.Fatalf("attachment setup error was not surfaced by load: %s", failure)
						}
					} else {
						result := e.call("session/load", params)
						if !bytes.Contains(result, []byte(`"currentModeId":"default"`)) {
							t.Fatalf("load did not return usable mode state: %s", result)
						}
						assertAbandonedAttachmentUpdates(t, drainUpdates(e.notes), fixture, migrated.Conversation.Messages)
					}
					select {
					case request := <-e.reqs:
						t.Fatalf("reconnect issued historical permission request: %s", request.Method)
					default:
					}
					if mounts.Load() != int32(cycle+1) || remote.initializes.Load() != int32(cycle+1) || remote.lists.Load() != int32(cycle+1) || reattaches.Load() == 0 || commands.activations.Load() != int32(cycle+1) {
						t.Fatalf("attachment/discovery was deferred: mounts=%d initializes=%d lists=%d placement=%d activation=%d", mounts.Load(), remote.initializes.Load(), remote.lists.Load(), reattaches.Load(), commands.activations.Load())
					}
					if !setupFailure && commands.lists.Load() != int32(cycle+1) {
						t.Fatal("successful load did not perform explicit command discovery")
					}
					if err := svc.Approve(t.Context(), migrated.ID, "ask-ambiguous", session.VerdictAllowAlways); err == nil {
						t.Fatal("stale allow-always accepted")
					}
					if err := svc.Approve(t.Context(), migrated.ID, "obsolete-ask", session.VerdictAllowAlways); err == nil {
						t.Fatal("candidate continuation accepted")
					}
					control := server.MCPAuthorizationControl{SessionID: migrated.ID, AuthorizationID: "auth-legacy-1"}
					if _, err := svc.RecheckMCPAuthorization(t.Context(), migrated.ID, control); err == nil {
						t.Fatal("stale authorization accepted")
					}
					if _, err := svc.CancelMCPAuthorization(t.Context(), migrated.ID, control); err == nil {
						t.Fatal("stale authorization cancellation accepted")
					}
					if _, err := svc.RetryFailedRun(t.Context(), migrated.ID); err == nil {
						t.Fatal("stale retry accepted")
					}
					if llm.Calls() != 0 || write.runs.Load()+read.runs.Load()+shell.runs.Load()+remote.calls.Load() != 0 || broker.calls.Load() != 0 || replays.Load() != 0 || policy.learns.Load() != 0 || len(grants.Rules(migrated.ID)) != 0 || learningObserver.calls.Load() != 0 {
						t.Fatal("reconnect or stale control resumed work, claimed authorization, granted permission, or published learning")
					}
					loaded, err := store.Load(t.Context(), migrated.ID)
					if err != nil {
						t.Fatal(err)
					}
					after, err := sessnap.Marshal(loaded)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatal("attachment or stale controls changed normalized history/authority")
					}
					if err := store.Save(t.Context(), loaded); err != nil {
						t.Fatal(err)
					}
				}
				if setupFailure {
					return
				}
				promptDone := make(chan json.RawMessage, 1)
				go func() {
					promptDone <- e.call("session/prompt", map[string]any{"sessionId": string(migrated.ID), "prompt": []any{map[string]any{"type": "text", "text": "separately admitted new work"}}})
				}()
				select {
				case request := <-e.reqs:
					if request.Method != "session/request_permission" || !bytes.Contains(request.Params, []byte(`"title":"Write"`)) {
						t.Fatalf("new prompt did not use ordinary permissions: %s %s", request.Method, request.Params)
					}
					e.respond(request.ID, map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "allow_once"}})
				case <-time.After(5 * time.Second):
					t.Fatal("new work bypassed permission ask or could not run")
				}
				select {
				case result := <-promptDone:
					if !bytes.Contains(result, []byte(`"stopReason":"end_turn"`)) {
						t.Fatalf("new prompt: %s", result)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("new prompt did not complete")
				}
				if llm.Calls() != 2 || write.runs.Load() != 1 || read.runs.Load()+shell.runs.Load()+remote.calls.Load()+broker.calls.Load()+policy.learns.Load() != 0 || replays.Load() != 1 || learningObserver.calls.Load() != 1 {
					t.Fatalf("new prompt did not remain separate: model=%d writes=%d replays=%d learning=%d", llm.Calls(), write.runs.Load(), replays.Load(), learningObserver.calls.Load())
				}
			})
		}
	}
}

func assertAbandonedAttachmentUpdates(t *testing.T, updates []map[string]any, fixture string, messages []session.Message) {
	t.Helper()
	proven := make(map[string]bool)
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			proven[string(call.ID)] = true
		}
	}
	if fixture == "legacy-ambiguous-pending" && len(proven) != 0 {
		t.Fatal("ambiguous sibling calls acquired fabricated authoritative links")
	}
	const notice = "The prior continuation was abandoned during upgrade. Prior tool effects and completion may be unknown."
	notices, palette, standalone, image, unavailable := 0, false, false, false, false
	for _, update := range updates {
		if update["sessionUpdate"] == "tool_call" || update["sessionUpdate"] == "tool_call_update" {
			id, _ := update["toolCallId"].(string)
			if !proven[id] {
				t.Fatalf("history projected an unproven invocation card: %v", update)
			}
		}
		if update["sessionUpdate"] == "available_commands_update" {
			palette = true
		}
		if update["sessionUpdate"] != "agent_message_chunk" {
			continue
		}
		if _, attached := update["toolCallId"]; attached {
			t.Fatal("standalone history acquired a fabricated tool attachment")
		}
		content, _ := update["content"].(map[string]any)
		if content["type"] == "text" {
			text, _ := content["text"].(string)
			if text == notice {
				notices++
			}
			standalone = standalone || strings.Contains(text, "standalone prior result") || strings.Contains(text, "unattached result")
			unavailable = unavailable || text == "Historical attachment unavailable"
		}
		if content["type"] == "image" && content["data"] == "AQID" {
			image = true
			if _, linked := content["uri"]; linked {
				t.Fatal("standalone historical media acquired an external attachment URI")
			}
		}
	}
	if notices != 1 || !palette || !standalone || !image || !unavailable {
		t.Fatalf("%s lost attachment discovery, notice, or unattached text/media: notices=%d palette=%v standalone=%v image=%v unavailable=%v", fixture, notices, palette, standalone, image, unavailable)
	}
	encoded, err := json.Marshal(updates)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"provider-duplicate", "provider-auth-call", "provider-call-reused", "provider-a", "private-binding", "auth-legacy-1", "encrypted-replay-v0", "identity_hold", "obsolete-ask"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("attachment replay leaked private legacy continuation identity %q", private)
		}
	}
}
