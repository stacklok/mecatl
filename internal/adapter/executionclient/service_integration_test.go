package executionclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/executionenv"
)

type recordingExecutor struct {
	mu         sync.Mutex
	operations []executionenv.Operation
	executed   chan executionenv.ExecutorRequest
	release    <-chan struct{}
}

func (e *recordingExecutor) Execute(ctx context.Context, _ string, req executionenv.ExecutorRequest) (executionenv.ExecutorResponse, error) {
	e.mu.Lock()
	e.operations = append(e.operations, req.Operation)
	e.mu.Unlock()
	if e.executed != nil {
		select {
		case e.executed <- req:
		case <-ctx.Done():
			return executionenv.ExecutorResponse{}, ctx.Err()
		}
		select {
		case <-e.release:
		case <-ctx.Done():
			return executionenv.ExecutorResponse{}, ctx.Err()
		}
	}
	switch req.Operation {
	case executionenv.OpFileResolveAuthority:
		return executionenv.ExecutorResponse{FileResponse: executionenv.FileResponse{AuthorityTarget: "/workspace/main.go", AuthorityWorkspace: "/workspace"}}, nil
	case executionenv.OpFileRead:
		return executionenv.ExecutorResponse{FileResponse: executionenv.FileResponse{Data: []byte("package main\n"), Version: "v1"}}, nil
	case executionenv.OpCommandStart:
		return executionenv.ExecutorResponse{Command: &executionenv.CommandStatusResponse{CommandID: req.CommandID, State: executionenv.CommandSucceeded, Stdout: []byte("out\xff"), Stderr: []byte("err\xe2"), TerminalReceipt: "complete"}}, nil
	default:
		return executionenv.ExecutorResponse{}, &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "unsupported test operation"}
	}
}

func TestServiceUsesRealMTLSProviderStoreAndReleasesOnlyAfterDrain(t *testing.T) {
	profilePath := filepath.Join(t.TempDir(), "profiles.yaml")
	profileYAML := []byte("profiles:\n  coding:\n    image: example.test/executor@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n    storageClass: standard\n    storageSize: 1Gi\n    cpuRequest: 100m\n    memoryRequest: 128Mi\n    cpuLimit: 1\n    memoryLimit: 1Gi\n    ephemeralStorageRequest: 256Mi\n    ephemeralStorageLimit: 1Gi\n    tmpSizeLimit: 128Mi\n    runtimeClassName: gvisor\n    maxFileBytes: 5242880\n    maxCommandBytes: 1048576\n    maxCommandDuration: 1m\n    maxEnvironments: 10\n")
	if err := os.WriteFile(profilePath, profileYAML, 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err := executioncontroller.LoadProfiles(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{executioncontroller.ExecutionEnvironmentGVR: "ExecutionEnvironmentList"})
	executor := &recordingExecutor{}
	controllerStore := executioncontroller.NewStore(dyn, "ns", profiles, executor)
	profile, err := controllerStore.ValidateProfile(t.Context(), "coding")
	if err != nil {
		t.Fatal(err)
	}
	owner := executionenv.Owner{Issuer: "issuer", Subject: "alice"}
	ownerSum := sha256.Sum256([]byte(owner.Issuer + "\x00" + owner.Subject))
	clientSum := sha256.Sum256([]byte("spiffe://example.test/mecak8s"))
	now := time.Now().UTC()
	envObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": "env-real", "namespace": "ns"},
		"spec":   map[string]any{"schemaVersion": int64(2), "revision": "rev-1", "ownerHash": hex.EncodeToString(ownerSum[:]), "ownerIssuer": owner.Issuer, "ownerSubject": owner.Subject, "clientHash": hex.EncodeToString(clientSum[:]), "bindingID": "service-session", "profile": "coding", "profileDigest": profile.Digest, "desired": "Active"},
		"status": map[string]any{"schemaVersion": int64(2), "epoch": int64(1), "grantGeneration": int64(1), "fenceState": "Healthy", "pod": map[string]any{"name": "executor-pod"}, "references": []any{map[string]any{"bindingID": "service-session", "state": "Published", "operationID": "seed", "createdAt": now.Format(time.RFC3339Nano)}}, "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
	}}
	if _, err := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").Create(t.Context(), envObj, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	fx := startFixture(t, controllerStore, nil)
	defer fx.stop()
	client, err := New(fx.endpoint, fx.clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	provider, err := NewProvider(client, "coding")
	if err != nil {
		t.Fatal(err)
	}
	catalog := tool.NewCatalog()
	catalog.MustRegister(fstools.ReadTool{})
	catalog.MustRegister(agent.NewShellTool())
	var modelShellResult string
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		for _, message := range req.Messages {
			if result := message.ToolResult; result != nil && result.CallID == "shell" {
				modelShellResult = result.Content
			}
		}
	})},
		mockllm.ToolCallTurn(session.NewToolCall("read", "Read", json.RawMessage(`{"path":"main.go"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("background", "Shell", json.RawMessage(`{"command":"touch background-leak","background":true}`))),
		mockllm.ToolCallTurn(session.NewToolCall("shell", "Shell", json.RawMessage(`{"command":"true"}`))),
		mockllm.TextTurn("done"),
		mockllm.TextTurn("continued"),
	)
	engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: catalog, Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test"})
	sessions := memstore.New()
	svc, err := server.NewService(server.Config{Engine: engine, Store: sessions, PlacementProvider: provider, PlacementScope: "test", ExecutionAccess: provider, SharedEngineRoot: "/workspace", DefaultLimits: session.Limits{MaxTurns: 5}})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	principal := &session.Principal{Issuer: owner.Issuer, Subject: owner.Subject, GrantType: session.GrantTypeUser}
	sess := session.New("service-session", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env-real", Revision: "rev-1"}, session.Limits{MaxTurns: 5}, now)
	if err := sess.RestoreLabels(principal, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	ctx := session.WithPrincipal(t.Context(), principal)
	run, err := svc.StartRunContent(ctx, sess.ID, "inspect", nil)
	if err != nil {
		t.Fatal(err)
	}
	backgroundRejected := false
	var streamedShellResult string
	for event := range run.Events() {
		if result := event.ToolResult; result != nil && result.CallID == "shell" {
			streamedShellResult = result.Content
		}
		if result := event.ToolResult; result != nil && result.CallID == "background" {
			backgroundRejected = result.IsError && strings.Contains(result.Content, "not supported by this command runner")
		}
	}
	if !backgroundRejected {
		t.Fatal("remote background Shell did not return the named capability error")
	}
	if !utf8.ValidString(streamedShellResult) || !strings.Contains(streamedShellResult, "out�") || !strings.Contains(streamedShellResult, "err�") || modelShellResult != streamedShellResult {
		t.Fatalf("private protobuf bytes were not repaired identically for client/model: stream=%q model=%q", streamedShellResult, modelShellResult)
	}
	executor.mu.Lock()
	operations := append([]executionenv.Operation(nil), executor.operations...)
	executor.mu.Unlock()
	wantOperations := []executionenv.Operation{executionenv.OpFileRead, executionenv.OpCommandStart}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Fatalf("executor operations=%v, want %v", operations, wantOperations)
	}
	if _, err := svc.StartRunContent(ctx, sess.ID, "early", nil); err == nil {
		t.Fatal("replacement run acquired before relay release")
	}
	svc.FinishRun(sess.ID, run)
	stored, err := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env-real", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := unstructured.NestedMap(stored.Object, "status", "activeRun"); err != nil || found {
		t.Fatalf("native claim remained after actual drain release: found=%t err=%v", found, err)
	}
	continued, err := svc.StartRunContent(ctx, sess.ID, "continue", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range continued.Events() {
	}
	svc.Persist(ctx, sess.ID)
	svc.FinishRun(sess.ID, continued)

	stored, err = dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, "env-real", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := sessions.Load(ctx, sess.ID)
	if err != nil || len(before.Conversation.Messages) == 0 {
		t.Fatalf("source history missing: %v", err)
	}
	placement := server.SuccessorPlacement{Selector: "unsupported-worktree", SelectorPresent: true}
	// A well-formed selector and binding ID reach the native provider's unsupported
	// selection gate, not the Service's malformed-request validation.
	if _, err := provider.Bind(ctx, server.PlacementBindRequest{Selector: server.SelectWorktree(sess.ID, sess.EnvironmentRef, placement.Selector), BindingID: "unused-destination", Principal: principal}); !errors.Is(err, server.ErrInvalidPlacementSelection) {
		t.Fatalf("native selector gate: %v", err)
	}
	for _, operation := range []string{"clear", "fork"} {
		t.Run(operation+"-unsupported-selector", func(t *testing.T) {
			var destination session.SessionID
			var err error
			if operation == "clear" {
				destination, err = svc.ClearSessionSuccessor(ctx, sess.ID, placement)
			} else {
				destination, err = svc.ForkSessionSuccessor(ctx, server.ForkSuccessorRequest{Source: sess.ID, Placement: placement})
			}
			if !errors.Is(err, server.ErrInvalidPlacementSelection) || destination != "" {
				t.Fatalf("destination=%q error=%v", destination, err)
			}
			after, err := sessions.Load(ctx, sess.ID)
			if err != nil || after.EnvironmentRef != before.EnvironmentRef || !reflect.DeepEqual(after.Conversation, before.Conversation) {
				t.Fatalf("unsupported successor changed source ref/history: %v", err)
			}
			all, err := svc.ListSessions(ctx)
			if err != nil || len(all) != 1 || all[0].SessionID != string(sess.ID) {
				t.Fatalf("unsupported successor published a destination: %+v, %v", all, err)
			}
			current, err := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, "env-real", metav1.GetOptions{})
			if err != nil || !reflect.DeepEqual(current.Object, stored.Object) {
				t.Fatalf("unsupported successor mutated provider references: %v", err)
			}
		})
	}
}
