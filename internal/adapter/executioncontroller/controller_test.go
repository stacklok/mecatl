package executioncontroller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestReconcileCreatesTokenlessNonRootPodAndRetainedPVC(t *testing.T) {
	ctx := context.Background()
	env := testEnvironment()
	scheme := runtime.NewScheme()
	d := dynamicfake.NewSimpleDynamicClient(scheme, env)
	k := kubefake.NewSimpleClientset()
	r := NewReconciler(d, k, "ns", testProfiles())
	if err := r.Reconcile(ctx, env.GetName()); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx, env.GetName()); err != nil {
		t.Fatal(err)
	}
	pvcs, _ := k.CoreV1().PersistentVolumeClaims("ns").List(ctx, metav1.ListOptions{})
	if len(pvcs.Items) != 1 {
		t.Fatalf("pvcs=%d", len(pvcs.Items))
	}
	if len(pvcs.Items[0].OwnerReferences) != 0 {
		t.Fatal("PVC must not have owner reference")
	}
	pods, _ := k.CoreV1().Pods("ns").List(ctx, metav1.ListOptions{})
	if len(pods.Items) != 1 {
		t.Fatalf("pods=%d", len(pods.Items))
	}
	p := pods.Items[0]
	if p.Spec.AutomountServiceAccountToken == nil || *p.Spec.AutomountServiceAccountToken {
		t.Fatal("service account token mounted")
	}
	if p.Spec.SecurityContext == nil || p.Spec.SecurityContext.RunAsNonRoot == nil || !*p.Spec.SecurityContext.RunAsNonRoot {
		t.Fatal("pod is not non-root")
	}
	c := p.Spec.Containers[0]
	if c.SecurityContext == nil || c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem || c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Fatal("container security context is not hardened")
	}
}
func TestRepeatedReconcileDoesNotRewriteUnchangedStatus(t *testing.T) {
	ctx := context.Background()
	env := testEnvironment()
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	k := kubefake.NewSimpleClientset()
	r := NewReconciler(d, k, "ns", testProfiles())
	if err := r.Reconcile(ctx, env.GetName()); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx, env.GetName()); err != nil {
		t.Fatal(err)
	}
	writes := statusUpdateCount(d.Actions())
	got, err := d.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, env.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transition := conditionTransition(got, "Ready")
	if transition == "" {
		t.Fatal("Ready condition has no transition timestamp")
	}
	if err := r.Reconcile(ctx, env.GetName()); err != nil {
		t.Fatal(err)
	}
	if extra := statusUpdateCount(d.Actions()) - writes; extra != 0 {
		t.Fatalf("unchanged reconcile performed %d status writes", extra)
	}
	got, err = d.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, env.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if current := conditionTransition(got, "Ready"); current != transition {
		t.Fatalf("lastTransitionTime changed from %q to %q", transition, current)
	}

	pods, err := k.CoreV1().Pods("ns").List(ctx, metav1.ListOptions{})
	if err != nil || len(pods.Items) != 1 {
		t.Fatalf("pods=%d err=%v", len(pods.Items), err)
	}
	pod := pods.Items[0].DeepCopy()
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if _, err := k.CoreV1().Pods("ns").UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx, env.GetName()); err != nil {
		t.Fatal(err)
	}
	got, err = d.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, env.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !conditionTrue(got, "Ready") {
		t.Fatalf("Ready did not follow Pod readiness: %v", got.Object["status"])
	}
	if statusUpdateCount(d.Actions()) != writes+1 {
		t.Fatalf("status writes=%d, want %d", statusUpdateCount(d.Actions()), writes+1)
	}
}

func TestRuntimeAndRelevantStatusUpdatesEnqueue(t *testing.T) {
	r := NewReconciler(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), kubefake.NewSimpleClientset(), "ns", testProfiles())
	r.enqueueRuntime(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"execution.mecatl.dev/environment": "exec-pod"}}})
	r.enqueueRuntime(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"execution.mecatl.dev/environment": "exec-pvc"}}})
	if r.queue.Len() != 2 {
		t.Fatalf("runtime queue length=%d", r.queue.Len())
	}

	oldEnv := testEnvironment()
	newEnv := oldEnv.DeepCopy()
	_ = unstructured.SetNestedField(newEnv.Object, "op", "status", "activeOperation", "id")
	r.enqueueUpdate(oldEnv, newEnv)
	if r.queue.Len() != 3 {
		t.Fatalf("relevant status update was not queued: len=%d", r.queue.Len())
	}
	controllerStatus := newEnv.DeepCopy()
	_ = unstructured.SetNestedField(controllerStatus.Object, "pod", "status", "pod", "name")
	r.enqueueUpdate(newEnv, controllerStatus)
	if r.queue.Len() != 3 {
		t.Fatalf("controller-owned status update was queued: len=%d", r.queue.Len())
	}
}

func statusUpdateCount(actions []k8stesting.Action) int {
	count := 0
	for _, action := range actions {
		if action.GetVerb() == "update" && action.GetSubresource() == "status" {
			count++
		}
	}
	return count
}

func conditionTransition(env *unstructured.Unstructured, name string) string {
	conditions, _, _ := unstructured.NestedSlice(env.Object, "status", "conditions")
	for _, value := range conditions {
		condition, ok := value.(map[string]any)
		if ok && text(condition, "type") == name {
			return text(condition, "lastTransitionTime")
		}
	}
	return ""
}

func TestStartupFencesInterruptedOperationBeforeReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := testEnvironment()
	_ = unstructured.SetNestedMap(env.Object, map[string]any{"id": "old", "operation": "file.replace"}, "status", "activeOperation")
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	r := NewReconciler(d, kubefake.NewSimpleClientset(), "ns", testProfiles())
	if r.Ready() {
		t.Fatal("reconciler reported ready before startup fencing")
	}
	if err := r.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if !r.Ready() {
		t.Fatal("reconciler did not report ready after startup fencing and cache sync")
	}
	got, err := d.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, env.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if textNested(got.Object, "status", "fenceState") != "FenceUnknown" || textNested(got.Object, "status", "activeOperation", "id") != "" {
		t.Fatalf("status=%v", got.Object["status"])
	}
}
func testProfiles() *Profiles {
	spec := ProfileSpec{Image: "example@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", StorageClass: "standard", StorageSize: "1Gi", CPURequest: "100m", MemoryRequest: "64Mi", CPULimit: "1", MemoryLimit: "1Gi", MaxFileBytes: 1024, MaxCommandBytes: 1024, MaxCommandDuration: time.Minute}
	profile, err := validateProfile("go", spec)
	if err != nil {
		panic(err)
	}
	profile.Spec = spec
	profile.Digest = "sha256:profile"
	return &Profiles{byName: map[string]resolvedProfile{"go": profile}}
}
func testEnvironment() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": "exec-test", "namespace": "ns", "uid": string(types.UID("uid"))}, "spec": map[string]any{"profile": "go", "profileDigest": "sha256:profile", "revision": "rev", "desired": "Active"}, "status": map[string]any{"epoch": int64(1), "references": []any{}, "fenceState": "Healthy"}}}
}
