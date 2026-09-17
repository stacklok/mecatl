package executioncontroller

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func profileSlots(t *testing.T, kube *kubefake.Clientset) []string {
	t.Helper()
	cm, err := kube.CoreV1().ConfigMaps("ns").Get(t.Context(), profileAllocationConfigMap, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var slots []string
	if err := executionenv.DecodeStrict([]byte(cm.Data[profileAllocationKey("go")]), &slots); err != nil {
		t.Fatal(err)
	}
	return slots
}

func TestDefinitiveEnvironmentCreateFailureReleasesProfileSlot(t *testing.T) {
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{ExecutionEnvironmentGVR: "ExecutionEnvironmentList"})
	dynamicClient.PrependReactor("create", "executionenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: ExecutionEnvironmentGVR.Group, Resource: ExecutionEnvironmentGVR.Resource}, "env", errors.New("policy"))
	})
	kube := kubefake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: profileAllocationConfigMap, Namespace: "ns"}, Data: map[string]string{}})
	store := NewStore(dynamicClient, "ns", testProfiles(), nil).WithKubeClient(kube)
	if _, err := store.EnsurePending(t.Context(), "client", "owner", "binding", "go", "fp", "op"); err == nil {
		t.Fatal("definitive create rejection succeeded")
	}
	if slots := profileSlots(t, kube); len(slots) != 0 {
		t.Fatalf("definitive create rejection leaked slots: %v", slots)
	}
}

func TestAmbiguousEnvironmentCreateFailureRetainsProfileSlot(t *testing.T) {
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{ExecutionEnvironmentGVR: "ExecutionEnvironmentList"})
	dynamicClient.PrependReactor("create", "executionenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("transport outcome unknown")
	})
	kube := kubefake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: profileAllocationConfigMap, Namespace: "ns"}, Data: map[string]string{}})
	store := NewStore(dynamicClient, "ns", testProfiles(), nil).WithKubeClient(kube)
	if _, err := store.EnsurePending(t.Context(), "client", "owner", "binding", "go", "fp", "op"); err == nil {
		t.Fatal("ambiguous create failure succeeded")
	}
	if slots := profileSlots(t, kube); len(slots) != 1 {
		t.Fatalf("ambiguous create outcome did not retain conservative capacity: %v", slots)
	}
}

func TestRetainedDeleteWaitsForSlotReleaseBeforeRemovingCRFinalizer(t *testing.T) {
	env := lifecycleAdminEnvironment(2, []any{})
	setConditionObject(env, "Retired", true, "WorkspaceRetained", "retained")
	setConditionObject(env, "ExecutorTerminated", true, "TerminalPodProof", "proved")
	_ = unstructured.SetNestedMap(env.Object, map[string]any{"id": "delete", "type": "DeleteRetiredEnvironment", "phase": "DeletingPVC", "expectedPVCUID": "pvc-uid", "createdAt": time.Now().UTC().Format(time.RFC3339Nano)}, "status", "lifecycleOperation")
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	allocationID := env.GetName()
	raw := `["` + allocationID + `"]`
	kube := kubefake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: profileAllocationConfigMap, Namespace: "ns"}, Data: map[string]string{profileAllocationKey("go"): raw}})
	var fail atomic.Bool
	fail.Store(true)
	kube.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		if fail.Load() {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, profileAllocationConfigMap, errors.New("outage"))
		}
		return false, nil, nil
	})
	r := NewReconciler(dynamicClient, kube, "ns", testProfiles())
	if err := r.Reconcile(t.Context(), env.GetName()); err != nil {
		t.Fatalf("failed to persist deallocation phase: %v", err)
	}
	if err := r.Reconcile(t.Context(), env.GetName()); err == nil {
		t.Fatal("slot authority outage did not stop deletion")
	}
	got, err := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), env.GetName(), metav1.GetOptions{})
	if err != nil || !contains(got.GetFinalizers(), environmentFinalizer) {
		t.Fatalf("CR was not retained for retry: finalizers=%v err=%v", got.GetFinalizers(), err)
	}
	fail.Store(false)
	if err := r.Reconcile(t.Context(), env.GetName()); err != nil {
		t.Fatal(err)
	}
	if slots := profileSlots(t, kube); len(slots) != 0 {
		t.Fatalf("retry did not release slot: %v", slots)
	}
}

func TestConcurrentProfileReservationsEnforceHardLimit(t *testing.T) {
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{ExecutionEnvironmentGVR: "ExecutionEnvironmentList"})
	ledger := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: profileAllocationConfigMap, Namespace: "ns", ResourceVersion: "1"}, Data: map[string]string{}}
	kubes := []*kubefake.Clientset{kubefake.NewSimpleClientset(ledger), kubefake.NewSimpleClientset(ledger)}
	var mu sync.Mutex
	current := ledger.DeepCopy()
	barrier := make(chan struct{})
	var reads atomic.Int32
	for _, kube := range kubes {
		kube.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
			mu.Lock()
			out := current.DeepCopy()
			mu.Unlock()
			if reads.Add(1) == 2 {
				close(barrier)
			}
			if reads.Load() <= 2 {
				<-barrier
			}
			return true, out, nil
		})
		kube.PrependReactor("update", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
			candidate := action.(k8stesting.UpdateAction).GetObject().(*corev1.ConfigMap)
			mu.Lock()
			defer mu.Unlock()
			if candidate.ResourceVersion != current.ResourceVersion {
				return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, candidate.Name, errors.New("resource version changed"))
			}
			rv, _ := strconv.Atoi(current.ResourceVersion)
			current = candidate.DeepCopy()
			current.ResourceVersion = strconv.Itoa(rv + 1)
			return true, current.DeepCopy(), nil
		})
	}
	profiles := testProfiles()
	profile := profiles.byName["go"]
	profile.Spec.MaxEnvironments = 1
	profiles.byName["go"] = profile
	stores := []*Store{NewStore(dynamicClient, "ns", profiles, nil).WithKubeClient(kubes[0]), NewStore(dynamicClient, "ns", profiles, nil).WithKubeClient(kubes[1])}
	results := make(chan error, 2)
	for i := range stores {
		go func(i int) {
			_, err := stores[i].EnsurePending(context.Background(), "client", "owner", "binding-"+strconv.Itoa(i), "go", "fp-"+strconv.Itoa(i), "op-"+strconv.Itoa(i))
			results <- err
		}(i)
	}
	admitted, exhausted := 0, 0
	for range 2 {
		err := <-results
		var controlled *executionenv.Error
		if err == nil {
			admitted++
		} else if errors.As(err, &controlled) && controlled.Code == executionenv.CodeResourceExhausted {
			exhausted++
		} else {
			t.Fatalf("unexpected result: %v", err)
		}
	}
	if admitted != 1 || exhausted != 1 {
		t.Fatalf("admitted=%d exhausted=%d", admitted, exhausted)
	}
}

func TestAcquireRunAndRevokeUseResourceVersionCAS(t *testing.T) {
	now := time.Date(2026, 9, 17, 5, 0, 0, 0, time.UTC)
	env := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": "env", "namespace": "ns", "resourceVersion": "1"}, "spec": map[string]any{"schemaVersion": int64(2), "revision": "rev", "ownerHash": "owner", "clientHash": hashText("client"), "desired": "Active"}, "status": map[string]any{"schemaVersion": int64(2), "epoch": int64(1), "grantGeneration": int64(1), "fenceState": fenceHealthy, "references": []any{map[string]any{"bindingID": "binding", "state": "Published", "operationID": "seed", "createdAt": now.Format(time.RFC3339Nano)}}, "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}}
	clients := []*dynamicfake.FakeDynamicClient{
		dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env),
		dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env),
	}
	var mu sync.Mutex
	current := env.DeepCopy()
	firstReads := make(chan struct{})
	var reads, conflicts atomic.Int32
	for _, client := range clients {
		client.PrependReactor("get", "executionenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
			mu.Lock()
			out := current.DeepCopy()
			mu.Unlock()
			if reads.Add(1) == 2 {
				close(firstReads)
			}
			if reads.Load() <= 2 {
				<-firstReads
			}
			return true, out, nil
		})
		client.PrependReactor("update", "executionenvironments", func(action k8stesting.Action) (bool, runtime.Object, error) {
			candidate := action.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured)
			mu.Lock()
			defer mu.Unlock()
			if candidate.GetResourceVersion() != current.GetResourceVersion() {
				conflicts.Add(1)
				return true, nil, apierrors.NewConflict(schema.GroupResource{Group: ExecutionEnvironmentGVR.Group, Resource: ExecutionEnvironmentGVR.Resource}, candidate.GetName(), errors.New("resource version changed"))
			}
			rv, _ := strconv.Atoi(current.GetResourceVersion())
			candidate = candidate.DeepCopy()
			candidate.SetResourceVersion(strconv.Itoa(rv + 1))
			current = candidate
			return true, candidate.DeepCopy(), nil
		})
	}
	first := NewStore(clients[0], "ns", testProfiles(), nil)
	second := NewStore(clients[1], "ns", testProfiles(), nil)
	first.now, second.now = func() time.Time { return now }, func() time.Time { return now }
	ref := executionenv.EnvironmentRef{ID: "env", Revision: "rev"}
	results := make(chan error, 2)
	go func() {
		_, err := first.AcquireRun(context.Background(), ref, "client", "owner", "binding", "run", "acquire", executionenv.MinRunTTL)
		results <- err
	}()
	go func() {
		_, err := second.RevokeEnvironment(context.Background(), ref, "client", "owner", 1, "revoke")
		results <- err
	}()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("CAS retry did not converge: %v", err)
		}
	}
	if conflicts.Load() == 0 {
		t.Fatal("test did not force a resource-version conflict")
	}
	mu.Lock()
	final := current.DeepCopy()
	mu.Unlock()
	if generation := intNested(final.Object, "status", "grantGeneration"); generation != 2 {
		t.Fatalf("grant generation=%d, want 2", generation)
	}
	if claim, _, _, ok := activeRunFrom(final); ok && generationMatches(final, claim.GrantGeneration) && claim.GrantGeneration != 2 {
		t.Fatalf("stale claim remained current after revoke: %+v", claim)
	}
}
