package executionclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestNativeBuildResolvePersistedShellAskAfterRestart(t *testing.T) {
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
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
	owner := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	ownerSum := sha256.Sum256([]byte(owner.Issuer + "\x00" + owner.Subject))
	clientSum := sha256.Sum256([]byte("spiffe://example.test/mecak8s"))
	now := time.Now().UTC()
	const sessionID = session.SessionID("persisted-native-ask")
	ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env-persisted", Revision: "rev-1"}
	envObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": ref.ID, "namespace": "ns"},
		"spec":   map[string]any{"schemaVersion": int64(2), "revision": ref.Revision, "ownerHash": hex.EncodeToString(ownerSum[:]), "ownerIssuer": owner.Issuer, "ownerSubject": owner.Subject, "clientHash": hex.EncodeToString(clientSum[:]), "bindingID": string(sessionID), "profile": "coding", "profileDigest": profile.Digest, "desired": "Active"},
		"status": map[string]any{"schemaVersion": int64(2), "epoch": int64(1), "grantGeneration": int64(1), "fenceState": "Healthy", "pod": map[string]any{"name": "executor-pod"}, "references": []any{map[string]any{"bindingID": string(sessionID), "state": "Published", "operationID": "seed", "createdAt": now.Format(time.RFC3339Nano)}}, "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
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

	storeDir := t.TempDir()
	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	parked := session.New(sessionID, session.ModeDefault, ref, session.Limits{MaxTurns: 4}, now)
	if err := parked.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), parked); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(settings, []byte("permissions:\n  ask:\n    - Shell\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseConfig := func(model port.LLMProvider) app.Config {
		return app.Config{
			UseMock: true, MockProvider: model, Workspace: t.TempDir(), StoreDir: storeDir,
			UserModelDir: t.TempDir(), MemoryDir: t.TempDir(), NoSoul: true, Headless: true,
			RemoteExecution: true, TrustProject: true, OwnershipEnforced: true,
			PlacementProvider: provider, PlacementScope: "native-test", PermissionConfigs: []string{settings},
		}
	}
	ctx := session.WithPrincipal(t.Context(), owner)
	builtA, err := app.Build(t.Context(), baseConfig(mockllm.New(mockllm.ToolCallTurn(
		session.NewToolCall("shell", "Shell", json.RawMessage(`{"command":"true"}`)),
	))))
	if err != nil {
		t.Fatal(err)
	}
	run, err := builtA.Service.StartRunContent(ctx, sessionID, "run shell", nil)
	if err != nil {
		builtA.Close()
		t.Fatal(err)
	}
	var runID, askID string
	for event := range run.Events() {
		if event.Type == session.EvPermissionAsk && event.Ask != nil {
			runID, askID = event.RunID, event.Ask.AskID
			builtA.Service.Persist(ctx, sessionID)
			break
		}
	}
	if runID == "" || askID == "" {
		builtA.Close()
		t.Fatal("native Shell run did not persist an exact ask")
	}
	drained := make(chan struct{})
	go func() {
		for range run.Events() {
		}
		builtA.Service.FinishRun(sessionID, run)
		close(drained)
	}()
	builtA.Close()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("normal transport relay did not drain the parked run during shutdown")
	}

	continuationEntered := make(chan struct{})
	continueModel := make(chan struct{})
	builtB, err := app.Build(t.Context(), baseConfig(mockllm.NewWith([]mockllm.Option{
		mockllm.WithRequestObserver(func(port.LLMRequest) {
			close(continuationEntered)
			<-continueModel
		}),
	}, mockllm.TextTurn("done"))))
	if err != nil {
		t.Fatal(err)
	}
	defer builtB.Close()
	releaseModel := sync.OnceFunc(func() { close(continueModel) })
	defer releaseModel()
	foreign := session.WithPrincipal(t.Context(), &session.Principal{Issuer: owner.Issuer, Subject: "mallory", GrantType: session.GrantTypeUser})
	if _, err := builtB.Service.ResolveRunAsk(foreign, sessionID, runID, askID, session.VerdictAllowOnce); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("foreign resolve = %v, want concealed not found", err)
	}
	assertNoNativeActiveRun(t, dyn, ref.ID)

	if _, err := builtB.Service.ResolveRunAsk(ctx, sessionID, runID, askID, session.VerdictAllowOnce); err != nil {
		t.Fatal(err)
	}
	select {
	case <-continuationEntered:
	case <-time.After(time.Second):
		t.Fatal("resumed run did not reach its model continuation")
	}
	stored, err := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), ref.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	activeRun, found, err := unstructured.NestedMap(stored.Object, "status", "activeRun")
	if err != nil || !found {
		t.Fatalf("native claim not held during continuation: found=%t err=%v", found, err)
	}
	for field, want := range map[string]string{
		"runID": runID, "bindingID": string(sessionID), "ownerHash": hex.EncodeToString(ownerSum[:]),
	} {
		if got := activeRun[field]; got != want {
			t.Fatalf("native claim %s = %v, want %q", field, got, want)
		}
	}
	competing, err := provider.AcquireRun(ctx, server.ExecutionRunRequest{Ref: ref, Principal: owner, BindingID: sessionID, RunID: runID})
	if err == nil {
		_ = competing.Release(t.Context())
		t.Fatal("competing native claim acquired while resumed run was active")
	}
	releaseModel()
	waitForNoNativeActiveRun(t, dyn, ref.ID)
	executor.mu.Lock()
	operations := append([]executionenv.Operation(nil), executor.operations...)
	executor.mu.Unlock()
	if len(operations) != 1 || operations[0] != executionenv.OpCommandStart {
		t.Fatalf("native operations = %v, want one command dispatch", operations)
	}
}

func assertNoNativeActiveRun(t *testing.T, dyn *dynamicfake.FakeDynamicClient, id string) {
	t.Helper()
	stored, err := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), id, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := unstructured.NestedMap(stored.Object, "status", "activeRun"); err != nil || found {
		t.Fatalf("unexpected native claim: found=%t err=%v", found, err)
	}
}

func waitForNoNativeActiveRun(t *testing.T, dyn *dynamicfake.FakeDynamicClient, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		stored, err := dyn.Resource(executioncontroller.ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, id, metav1.GetOptions{})
		if err == nil {
			if _, found, nestedErr := unstructured.NestedMap(stored.Object, "status", "activeRun"); nestedErr == nil && !found {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("native claim was not released after detached relay drain")
		case <-ticker.C:
		}
	}
}
