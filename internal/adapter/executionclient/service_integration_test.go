package executionclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
	"github.com/stacklok/mecatl/internal/adapter/server"
	adaptertools "github.com/stacklok/mecatl/internal/adapter/tools"
	"github.com/stacklok/mecatl/internal/executionenv"
)

type recordingExecutor struct{ calls atomic.Int64 }

func (e *recordingExecutor) Execute(_ context.Context, _ string, req executionenv.ExecutorRequest) (executionenv.ExecutorResponse, error) {
	e.calls.Add(1)
	switch req.Operation {
	case executionenv.OpFileResolveAuthority:
		return executionenv.ExecutorResponse{FileResponse: executionenv.FileResponse{AuthorityTarget: "/workspace/main.go", AuthorityWorkspace: "/workspace"}}, nil
	case executionenv.OpFileRead:
		return executionenv.ExecutorResponse{FileResponse: executionenv.FileResponse{Data: []byte("package main\n"), Version: "v1"}}, nil
	case executionenv.OpCommandStart:
		return executionenv.ExecutorResponse{Command: &executionenv.CommandStatusResponse{CommandID: req.CommandID, State: executionenv.CommandSucceeded, Stdout: []byte("ok\n"), TerminalReceipt: "complete"}}, nil
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
	catalog.MustRegister(adaptertools.ReadTool{})
	catalog.MustRegister(adaptertools.NewShellTool())
	llm := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("read", "Read", json.RawMessage(`{"path":"main.go"}`))),
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
	for range run.Events() {
	}
	if executor.calls.Load() != 3 {
		t.Fatalf("executor calls=%d, want authority+read+shell", executor.calls.Load())
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
	svc.FinishRun(sess.ID, continued)
}
