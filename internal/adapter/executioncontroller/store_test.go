package executioncontroller

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestRenewRunRejectsExpiredClaimAtBoundary(t *testing.T) {
	now := time.Date(2026, 9, 17, 4, 0, 0, 0, time.UTC)
	env := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": "env", "namespace": "ns"}, "spec": map[string]any{"schemaVersion": int64(2), "revision": "rev", "ownerHash": "owner", "clientHash": hashText("client"), "desired": "Active"}, "status": map[string]any{"schemaVersion": int64(2), "epoch": int64(7), "grantGeneration": int64(3), "fenceState": fenceHealthy, "references": []any{}, "activeRun": map[string]any{"bindingID": "binding", "runID": "run", "claimID": "claim", "operationID": "acquire", "epoch": int64(7), "grantGeneration": int64(3), "expiresAt": now.Format(time.RFC3339Nano)}}}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(client, "ns", testProfiles(), nil)
	store.now = func() time.Time { return now }
	req := executionenv.RunClaimRequest{BindingID: "binding", RunID: "run", ClaimID: "claim", Epoch: 7, GrantGeneration: 3, OperationID: "renew", TTL: executionenv.MinRunTTL}
	if _, err := store.RenewRun(t.Context(), executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, "client", "owner", req); err == nil {
		t.Fatal("renewal at the expiry boundary resurrected the claim")
	}
	got, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if expiry := textNested(got.Object, "status", "activeRun", "expiresAt"); expiry != now.Format(time.RFC3339Nano) {
		t.Fatalf("expired claim changed to %q", expiry)
	}
}

func TestRenewRunReplaysExactOperationWithoutExtendingLease(t *testing.T) {
	now := time.Date(2026, 9, 17, 4, 0, 0, 0, time.UTC)
	env := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": "env", "namespace": "ns"}, "spec": map[string]any{"schemaVersion": int64(2), "revision": "rev", "ownerHash": "owner", "clientHash": hashText("client"), "desired": "Active"}, "status": map[string]any{"schemaVersion": int64(2), "epoch": int64(7), "grantGeneration": int64(3), "fenceState": fenceHealthy, "references": []any{}, "activeRun": map[string]any{"bindingID": "binding", "runID": "run", "claimID": "claim", "operationID": "acquire", "epoch": int64(7), "grantGeneration": int64(3), "expiresAt": now.Add(time.Minute).Format(time.RFC3339Nano)}}}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(client, "ns", testProfiles(), nil)
	clock := now
	store.now = func() time.Time { return clock }
	ref := executionenv.EnvironmentRef{ID: "env", Revision: "rev"}
	req := executionenv.RunClaimRequest{BindingID: "binding", RunID: "run", ClaimID: "claim", Epoch: 7, GrantGeneration: 3, OperationID: "renew", TTL: executionenv.MinRunTTL}
	first, err := store.RenewRun(t.Context(), ref, "client", "owner", req)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(10 * time.Second)
	replay, err := store.RenewRun(t.Context(), ref, "client", "owner", req)
	if err != nil || !replay.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("replay extended or rejected lease: first=%v replay=%v err=%v", first.ExpiresAt, replay.ExpiresAt, err)
	}
	req.TTL = time.Minute
	if _, err := store.RenewRun(t.Context(), ref, "client", "owner", req); err == nil {
		t.Fatal("same operation accepted conflicting inputs")
	}
	clock = first.ExpiresAt
	req.TTL = executionenv.MinRunTTL
	if _, err := store.RenewRun(t.Context(), ref, "client", "owner", req); err == nil {
		t.Fatal("expired renewal receipt resurrected claim")
	}
}

func TestStoreEnsureUsesStableLookupAndRejectsFingerprintDrift(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	profiles := &Profiles{byName: map[string]resolvedProfile{"go": {Digest: "sha256:profile", Spec: ProfileSpec{Image: "example@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", StorageClass: "standard", StorageSize: "1Gi", CPURequest: "100m", MemoryRequest: "64Mi", CPULimit: "1", MemoryLimit: "1Gi", EphemeralStorageRequest: "64Mi", EphemeralStorageLimit: "1Gi", TmpSizeLimit: "256Mi", RuntimeClassName: "sandboxed", MaxFileBytes: 1024, MaxCommandBytes: 1024, MaxCommandDuration: time.Minute, MaxEnvironments: 100}}, "other": {Digest: "sha256:other", Spec: ProfileSpec{Image: "example@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", StorageClass: "standard", StorageSize: "1Gi", CPURequest: "100m", MemoryRequest: "64Mi", CPULimit: "1", MemoryLimit: "1Gi", EphemeralStorageRequest: "64Mi", EphemeralStorageLimit: "1Gi", TmpSizeLimit: "256Mi", RuntimeClassName: "sandboxed", MaxFileBytes: 1024, MaxCommandBytes: 1024, MaxCommandDuration: time.Minute, MaxEnvironments: 100}}}}
	s := NewStore(client, "ns", profiles, nil)
	ctx := context.Background()
	a, err := s.Ensure(ctx, "spiffe://client", "owner", "binding", "go", "fp1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Ensure(ctx, "spiffe://client", "owner", "binding", "go", "fp1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Environment != b.Environment || a.Epoch != b.Epoch {
		t.Fatalf("identity changed: %+v %+v", a, b)
	}
	if _, err := s.Ensure(ctx, "spiffe://client", "owner", "binding", "other", "fp2"); err == nil {
		t.Fatal("profile drift adopted existing allocation")
	}
}
func TestAllocationNameExcludesMutableProfile(t *testing.T) {
	a := allocationName("client", "owner", "binding")
	b := allocationName("client", "owner", "binding")
	if a != b {
		t.Fatal("unstable allocation name")
	}
}

type terminalErrorExecutor struct{ called int }

type blockingExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (e *blockingExecutor) Execute(context.Context, string, executionenv.ExecutorRequest) (executionenv.ExecutorResponse, error) {
	close(e.started)
	<-e.release
	return executionenv.ExecutorResponse{}, nil
}

func (e *terminalErrorExecutor) Execute(context.Context, string, executionenv.ExecutorRequest) (executionenv.ExecutorResponse, error) {
	e.called++
	return executionenv.ExecutorResponse{}, &executionenv.Error{Code: executionenv.CodeNotFound, Message: "path not found"}
}
func TestCancelledStoreOperationStaysActiveUntilBackendStopsThenFences(t *testing.T) {
	env := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": "env", "namespace": "ns"}, "spec": map[string]any{"schemaVersion": int64(2), "revision": "rev", "ownerHash": "owner", "clientHash": hashText("client"), "profile": "go", "profileDigest": "sha256:profile", "desired": "Active"}, "status": map[string]any{"schemaVersion": int64(2), "epoch": int64(1), "grantGeneration": int64(1), "fenceState": "Healthy", "references": []any{map[string]any{"bindingID": "binding", "state": "Published", "operationID": "seed", "createdAt": time.Now().UTC().Format(time.RFC3339Nano)}}, "activeRun": map[string]any{"bindingID": "binding", "runID": "run", "claimID": "claim", "operationID": "acquire", "epoch": int64(1), "grantGeneration": int64(1), "expiresAt": time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}, "pod": map[string]any{"name": "pod"}, "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	exec := &blockingExecutor{started: make(chan struct{}), release: make(chan struct{})}
	s := NewStore(client, "ns", testProfiles(), exec)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.StartCommand(ctx, "client", "owner", executionenv.CommandStartRequest{Context: executionenv.RequestContext{Environment: executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, BindingID: "binding", RunID: "run", ClaimID: "claim", Epoch: 1, GrantGeneration: 1}, Command: "sleep 10"})
		done <- err
	}()
	<-exec.started
	cancel()
	active, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(context.Background(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if textNested(active.Object, "status", "activeOperation", "id") == "" {
		t.Fatal("active operation was released while executor was still live")
	}
	close(exec.release)
	var pe *executionenv.Error
	if err := <-done; !errors.As(err, &pe) || pe.Code != executionenv.CodeFenceUnknown {
		t.Fatalf("completion error=%v", err)
	}
	fenced, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(context.Background(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if textNested(fenced.Object, "status", "activeOperation", "id") == "" || textNested(fenced.Object, "status", "fenceState") != "FenceUnknown" {
		t.Fatalf("uncertain operation identity was not retained: status=%v", fenced.Object["status"])
	}
	_, err = s.Attach(context.Background(), executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, "client", "owner", "binding")
	if !errors.As(err, &pe) || pe.Code != executionenv.CodeNotReady {
		t.Fatalf("attach after fencing error=%v", err)
	}
}

func TestControlledExecutorErrorClearsOperationWithoutFencing(t *testing.T) {
	env := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": "env", "namespace": "ns"}, "spec": map[string]any{"schemaVersion": int64(2), "revision": "rev", "ownerHash": "owner", "clientHash": hashText("client"), "profile": "go", "profileDigest": "sha256:profile", "desired": "Active"}, "status": map[string]any{"schemaVersion": int64(2), "epoch": int64(1), "grantGeneration": int64(1), "fenceState": "Healthy", "references": []any{map[string]any{"bindingID": "binding", "state": "Published", "operationID": "seed", "createdAt": time.Now().UTC().Format(time.RFC3339Nano)}}, "activeRun": map[string]any{"bindingID": "binding", "runID": "run", "claimID": "claim", "operationID": "acquire", "epoch": int64(1), "grantGeneration": int64(1), "expiresAt": time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}, "pod": map[string]any{"name": "pod"}, "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	exec := &terminalErrorExecutor{}
	s := NewStore(client, "ns", testProfiles(), exec)
	_, err := s.File(context.Background(), "client", "owner", executionenv.FileRequest{Context: executionenv.RequestContext{Environment: executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, BindingID: "binding", RunID: "run", ClaimID: "claim", Epoch: 1, GrantGeneration: 1}, Operation: executionenv.OpFileRead, Path: "missing"})
	var pe *executionenv.Error
	if !errors.As(err, &pe) || pe.Code != executionenv.CodeNotFound {
		t.Fatalf("error=%v", err)
	}
	got, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(context.Background(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if exec.called != 1 || textNested(got.Object, "status", "activeOperation", "id") != "" || textNested(got.Object, "status", "fenceState") != "Healthy" {
		t.Fatalf("status=%v calls=%d", got.Object["status"], exec.called)
	}
}
