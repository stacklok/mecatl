package executioncontroller

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestRevokeEnvironmentFencesOldClaimWithoutChangingExecutionEpoch(t *testing.T) {
	env := runFixtureEnvironment(7, 3)
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(client, "ns", testProfiles(), &terminalErrorExecutor{})
	ref := executionenv.EnvironmentRef{ID: "env", Revision: "rev"}
	next, err := store.RevokeEnvironment(t.Context(), ref, "client", "owner", 3, "revoke-1")
	if err != nil || next != 4 {
		t.Fatalf("revoke=(%d,%v)", next, err)
	}
	got, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if intNested(got.Object, "status", "epoch") != 7 || intNested(got.Object, "status", "grantGeneration") != 4 {
		t.Fatalf("status=%v", got.Object["status"])
	}
	otherReplica := NewStore(client, "ns", testProfiles(), &terminalErrorExecutor{})
	_, err = otherReplica.File(t.Context(), "client", "owner", executionenv.FileRequest{Context: executionenv.RequestContext{Environment: ref, BindingID: "binding", RunID: "run", ClaimID: "claim", Epoch: 7, GrantGeneration: 3}, Operation: executionenv.OpFileRead, Path: "x"})
	var controlled *executionenv.Error
	if !errors.As(err, &controlled) || controlled.Code != executionenv.CodeConflict {
		t.Fatalf("old grant error=%v", err)
	}
	if _, err := store.RenewRun(t.Context(), ref, "client", "owner", executionenv.RunClaimRequest{Environment: ref, BindingID: "binding", RunID: "run", ClaimID: "claim", Epoch: 7, GrantGeneration: 3, OperationID: "renew", TTL: time.Minute}); !errors.As(err, &controlled) || controlled.Code != executionenv.CodeConflict {
		t.Fatalf("old renewal error=%v", err)
	}
	replayed, err := store.RevokeEnvironment(t.Context(), ref, "client", "owner", 3, "revoke-1")
	if err != nil || replayed != 4 {
		t.Fatalf("replayed revoke=(%d,%v)", replayed, err)
	}
	if _, err := store.RevokeEnvironment(t.Context(), ref, "client", "owner", 3, "revoke-2"); !errors.As(err, &controlled) || controlled.Code != executionenv.CodeConflict {
		t.Fatalf("stale CAS error=%v", err)
	}
}

func TestRevokeEnvironmentOverflowFailsClosed(t *testing.T) {
	env := runFixtureEnvironment(1, math.MaxInt64)
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(client, "ns", testProfiles(), nil)
	if _, err := store.RevokeEnvironment(context.Background(), executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, "client", "owner", math.MaxInt64, "overflow"); err == nil {
		t.Fatal("overflow accepted")
	}
}

func TestProfileEnvironmentAdmissionLimit(t *testing.T) {
	env := lifecycleEnvironment(time.Now().UTC())
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{ExecutionEnvironmentGVR: "ExecutionEnvironmentList"}, env)
	profiles := testProfiles()
	profile := profiles.byName["go"]
	profile.Spec.MaxEnvironments = 1
	profiles.byName["go"] = profile
	store := NewStore(client, "ns", profiles, nil).WithKubeClient(kubernetesfake.NewSimpleClientset())
	_, err := store.Ensure(t.Context(), "client", "owner", "new-binding", "go", "new-fingerprint")
	var controlled *executionenv.Error
	if !errors.As(err, &controlled) || controlled.Code != executionenv.CodeResourceExhausted {
		t.Fatalf("limit error=%v", err)
	}
}

func runFixtureEnvironment(epoch, generation uint64) *unstructured.Unstructured {
	now := time.Now().UTC()
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": "env", "namespace": "ns"}, "spec": map[string]any{"schemaVersion": int64(2), "revision": "rev", "ownerHash": "owner", "clientHash": hashText("client"), "profile": "go", "profileDigest": "sha256:profile", "desired": "Active"}, "status": map[string]any{"schemaVersion": int64(2), "epoch": int64(epoch), "grantGeneration": int64(generation), "fenceState": "Healthy", "references": []any{map[string]any{"bindingID": "binding", "state": "Published", "operationID": "seed", "createdAt": now.Format(time.RFC3339Nano)}}, "activeRun": map[string]any{"bindingID": "binding", "runID": "run", "claimID": "claim", "operationID": "acquire", "epoch": int64(epoch), "grantGeneration": int64(generation), "expiresAt": now.Add(time.Minute).Format(time.RFC3339Nano)}, "pod": map[string]any{"name": "pod"}, "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}}
}
