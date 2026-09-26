package executioncontroller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/util/workqueue"
	clocktesting "k8s.io/utils/clock/testing"
)

func TestReconcileRefusesForeignExistingPVCWithoutPersistingUID(t *testing.T) {
	ctx := context.Background()
	env := testEnvironment()
	env.SetFinalizers([]string{environmentFinalizer})
	foreign := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "workspace-test", Namespace: "ns", UID: types.UID("foreign"), Labels: map[string]string{"execution.mecatl.dev/environment": env.GetName(), "execution.mecatl.dev/revision": "rev"}}}
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	k := kubefake.NewSimpleClientset(foreign)
	r := NewReconciler(d, k, "ns", testProfiles())
	if err := r.Reconcile(ctx, env.GetName()); err == nil {
		t.Fatal("foreign PVC ownership mismatch was not returned")
	}
	got, err := d.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, env.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if uid := textNested(got.Object, "status", "pvc", "uid"); uid != "" {
		t.Fatalf("foreign PVC UID persisted as authoritative: %q", uid)
	}
	pods, err := k.CoreV1().Pods("ns").List(ctx, metav1.ListOptions{})
	if err != nil || len(pods.Items) != 0 {
		t.Fatalf("executor created over foreign PVC: pods=%d err=%v", len(pods.Items), err)
	}
}

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
	if p.Spec.RuntimeClassName == nil || *p.Spec.RuntimeClassName != "sandboxed" || c.Resources.Requests.Cpu().IsZero() || c.Resources.Requests.Memory().IsZero() || c.Resources.Requests.StorageEphemeral().IsZero() || c.Resources.Limits.StorageEphemeral().IsZero() {
		t.Fatal("runtime class or resource bounds are missing")
	}
	if p.Spec.Volumes[1].EmptyDir == nil || p.Spec.Volumes[1].EmptyDir.SizeLimit == nil || p.Spec.Volumes[1].EmptyDir.SizeLimit.String() != "256Mi" {
		t.Fatal("tmp volume is not profile-bounded")
	}
}
func TestTerminatingPodIsUnavailableDuringReconcile(t *testing.T) {
	ctx := t.Context()
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
	pod, err := k.CoreV1().Pods("ns").Get(ctx, "executor-test", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	now := metav1.Now()
	pod.DeletionTimestamp = &now
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if _, err := k.CoreV1().Pods("ns").Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx, env.GetName()); err == nil {
		t.Fatal("terminating Pod was not rejected")
	}
	got, err := d.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, env.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if conditionTrue(got, "Ready") {
		t.Fatalf("terminating pod remained ready: %v", got.Object["status"])
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

func TestActiveOperationExpiryRequeuesWithoutAnotherEvent(t *testing.T) {
	ctx := t.Context()
	env := testEnvironment()
	env.SetFinalizers([]string{environmentFinalizer})
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	kubeClient := kubefake.NewSimpleClientset()
	r := NewReconciler(dynamicClient, kubeClient, "ns", testProfiles())
	initialQueue := r.queue
	t.Cleanup(initialQueue.ShutDown)

	// Establish a fully reconciled, ready environment so only operation expiry can
	// cause the transition below.
	if err := r.Reconcile(ctx, env.GetName()); err != nil {
		t.Fatal(err)
	}
	pod, err := kubeClient.CoreV1().Pods("ns").Get(ctx, "executor-test", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if _, err := kubeClient.CoreV1().Pods("ns").UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx, env.GetName()); err != nil {
		t.Fatal(err)
	}
	initialQueue.ShutDown()

	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(100 * time.Millisecond)
	current, err := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, env.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedMap(current.Object, map[string]any{
		"id": "operation", "expiresAt": deadline.Format(time.RFC3339Nano),
	}, "status", "activeOperation"); err != nil {
		t.Fatal(err)
	}
	if _, err := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").UpdateStatus(ctx, current, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	fakeClock := clocktesting.NewFakeClock(now)
	delayingQueue := workqueue.NewTypedDelayingQueueWithConfig[string](workqueue.TypedDelayingQueueConfig[string]{Clock: fakeClock})
	replacementQueue := workqueue.NewTypedRateLimitingQueueWithConfig(
		workqueue.DefaultTypedControllerRateLimiter[string](),
		workqueue.TypedRateLimitingQueueConfig[string]{DelayingQueue: delayingQueue},
	)
	t.Cleanup(replacementQueue.ShutDown)
	r.queue = replacementQueue
	r.now = fakeClock.Now
	if err := r.Reconcile(ctx, env.GetName()); err != nil {
		t.Fatal(err)
	}
	beforeExpiry, err := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, env.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !conditionTrue(beforeExpiry, "Ready") || textNested(beforeExpiry.Object, "status", "fenceState") != fenceHealthy {
		t.Fatalf("future operation lease disturbed ready state: %v", beforeExpiry.Object["status"])
	}

	waitUntil := time.Now().Add(time.Second)
	for fakeClock.Waiters() < 2 && time.Now().Before(waitUntil) {
		time.Sleep(time.Millisecond)
	}
	if fakeClock.Waiters() < 2 {
		t.Fatal("reconcile did not register the operation-expiry deadline with the delayed queue")
	}

	done := make(chan struct{})
	go func() {
		r.worker(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		replacementQueue.ShutDown()
		<-done
	})

	fakeClock.Step(99 * time.Millisecond)
	preDeadline, err := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, env.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !conditionTrue(preDeadline, "Ready") || textNested(preDeadline.Object, "status", "fenceState") != fenceHealthy {
		t.Fatalf("operation fenced before its deadline: %v", preDeadline.Object["status"])
	}

	fakeClock.Step(time.Millisecond)
	workerDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(workerDeadline) {
		got, getErr := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, env.GetName(), metav1.GetOptions{})
		if getErr != nil {
			t.Fatal(getErr)
		}
		if textNested(got.Object, "status", "fenceState") == "FenceUnknown" {
			if textNested(got.Object, "status", "activeOperation", "id") != "operation" || conditionTrue(got, "Ready") {
				t.Fatalf("expiry did not retain unresolved operation identity and clear readiness: %v", got.Object["status"])
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("worker did not reconcile the active-operation deadline without another Kubernetes event")
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
	lifecycle := newEnv.DeepCopy()
	_ = unstructured.SetNestedMap(lifecycle.Object, map[string]any{"id": "retire", "phase": "Quiescing"}, "status", "lifecycleOperation")
	lifecycleQueue := NewReconciler(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), kubefake.NewSimpleClientset(), "ns", testProfiles())
	lifecycleQueue.enqueueUpdate(newEnv, lifecycle)
	if lifecycleQueue.queue.Len() != 1 {
		t.Fatalf("lifecycle start was not queued: len=%d", lifecycleQueue.queue.Len())
	}
	advanced := lifecycle.DeepCopy()
	_ = unstructured.SetNestedField(advanced.Object, "WaitingForTermination", "status", "lifecycleOperation", "phase")
	phaseQueue := NewReconciler(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), kubefake.NewSimpleClientset(), "ns", testProfiles())
	phaseQueue.enqueueUpdate(lifecycle, advanced)
	if phaseQueue.queue.Len() != 1 {
		t.Fatalf("lifecycle phase change was not queued: len=%d", phaseQueue.queue.Len())
	}
	controllerStatus := advanced.DeepCopy()
	_ = unstructured.SetNestedField(controllerStatus.Object, "pod", "status", "pod", "name")
	r.enqueueUpdate(advanced, controllerStatus)
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

func TestStartupDoesNotFenceLivePeerOperation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := testEnvironment()
	_ = unstructured.SetNestedMap(env.Object, map[string]any{"id": "old", "operation": "file.replace"}, "status", "activeOperation")
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	r := NewReconciler(d, profileResourceClient(), "ns", testProfiles())
	peer := NewReconciler(d, profileResourceClient(), "ns", testProfiles())
	if r.Ready() || peer.Ready() {
		t.Fatal("reconciler reported ready before cache synchronization")
	}
	if err := r.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := peer.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if !r.Ready() || !peer.Ready() {
		t.Fatal("reconcilers did not report ready after cache synchronization")
	}
	got, err := d.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, env.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if textNested(got.Object, "status", "fenceState") != "Healthy" || textNested(got.Object, "status", "activeOperation", "id") != "old" {
		t.Fatalf("startup mutated a potentially live peer operation: status=%v", got.Object["status"])
	}
}
func TestInitializeRefusesMissingRuntimeClassBeforeCreatingPods(t *testing.T) {
	kube := kubefake.NewSimpleClientset(&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "standard"}})
	r := NewReconciler(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), kube, "ns", testProfiles())
	err := r.Initialize(t.Context())
	if err == nil || !strings.Contains(err.Error(), `profile preflight: RuntimeClass "sandboxed" unavailable`) {
		t.Fatalf("Initialize error = %v", err)
	}
	if r.Ready() {
		t.Fatal("reconciler reported ready after failed profile preflight")
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "create" && action.GetResource().Resource == "pods" {
			t.Fatal("profile preflight created a Pod")
		}
	}
}

func profileResourceClient() *kubefake.Clientset {
	return kubefake.NewSimpleClientset(
		&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "sandboxed"}},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "standard"}},
	)
}

func testProfiles() *Profiles {
	spec := ProfileSpec{Image: "example@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", StorageClass: "standard", StorageSize: "1Gi", CPURequest: "100m", MemoryRequest: "64Mi", CPULimit: "1", MemoryLimit: "1Gi", EphemeralStorageRequest: "64Mi", EphemeralStorageLimit: "1Gi", TmpSizeLimit: "256Mi", RuntimeClassName: "sandboxed", MaxFileBytes: 1024, MaxCommandBytes: 1024, MaxCommandDuration: time.Minute, MaxEnvironments: 100}
	profile, err := validateProfile("go", spec)
	if err != nil {
		panic(err)
	}
	profile.Spec = spec
	profile.Digest = "sha256:profile"
	return &Profiles{byName: map[string]resolvedProfile{"go": profile}}
}
func testEnvironment() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": "exec-test", "namespace": "ns", "uid": string(types.UID("uid"))}, "spec": map[string]any{"schemaVersion": int64(2), "profile": "go", "profileDigest": "sha256:profile", "revision": "rev", "desired": "Active"}, "status": map[string]any{"schemaVersion": int64(2), "epoch": int64(1), "references": []any{}, "fenceState": "Healthy"}}}
}
