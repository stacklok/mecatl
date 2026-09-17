package executioncontroller

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestPodTerminalRequiresEveryDeclaredContainerByName(t *testing.T) {
	terminated := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}
	base := &corev1.Pod{Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Name: "init"}}, Containers: []corev1.Container{{Name: "executor"}}}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded, InitContainerStatuses: []corev1.ContainerStatus{{Name: "init", State: terminated}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "executor", State: terminated}}}}
	if !podTerminal(base) {
		t.Fatal("exact terminated init and regular containers were rejected")
	}
	cases := map[string]func(*corev1.Pod){
		"init waiting": func(p *corev1.Pod) {
			p.Status.InitContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}
		},
		"init running": func(p *corev1.Pod) {
			p.Status.InitContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
		},
		"missing init": func(p *corev1.Pod) { p.Status.InitContainerStatuses = nil },
		"duplicate regular": func(p *corev1.Pod) {
			p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, p.Status.ContainerStatuses[0])
		},
		"wrong regular name": func(p *corev1.Pod) { p.Status.ContainerStatuses[0].Name = "other" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			pod := base.DeepCopy()
			mutate(pod)
			if podTerminal(pod) {
				t.Fatal("invalid container evidence proved terminal")
			}
		})
	}
}

func lifecycleAdminEnvironment(schema int64, refs []any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment",
		"metadata": map[string]any{"name": "env", "namespace": "ns", "uid": "env-uid", "finalizers": []any{environmentFinalizer}},
		"spec":     map[string]any{"schemaVersion": schema, "revision": "rev", "ownerHash": "owner", "clientHash": hashText("client"), "profile": "go", "profileDigest": "sha256:profile", "desired": "Active"},
		"status": map[string]any{"schemaVersion": schema, "epoch": int64(4), "grantGeneration": int64(1), "fenceState": fenceHealthy, "references": refs,
			"pvc": map[string]any{"name": "workspace", "uid": "pvc-uid"}, "pod": map[string]any{"name": "executor", "uid": "pod-uid"},
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
	}}
}

func terminalExecutor() *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "executor", Namespace: "ns", UID: types.UID("pod-uid"), Finalizers: []string{executorFinalizer}, Labels: map[string]string{"execution.mecatl.dev/environment": "env"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "executor"}}}, Status: corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{{Name: "executor", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137}}}}}}
}

func retainedPVC() *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: "ns", UID: types.UID("pvc-uid"), Labels: map[string]string{"execution.mecatl.dev/environment": "env"}}}
}

func adminRequestFixture() adminLifecycleRequest {
	return adminLifecycleRequest{Environment: executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, OwnerHash: "owner", Client: "client", ExpectedEpoch: 4, ExpectedPodUID: "pod-uid", ExpectedPVCUID: "pvc-uid", OperationID: "admin-operation"}
}

func TestOldSchemaFailsClosedAndExplicitMigrationConvertsReferences(t *testing.T) {
	env := lifecycleAdminEnvironment(0, []any{"session-a"})
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	kube := kubefake.NewSimpleClientset(terminalExecutor(), retainedPVC())
	store := NewStore(dynamicClient, "ns", testProfiles(), nil).WithKubeClient(kube)
	_, err := store.Attach(t.Context(), executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, "client", "owner", "session-a")
	var controlled *executionenv.Error
	if !errors.As(err, &controlled) || controlled.Code != executionenv.CodeNotReady {
		t.Fatalf("old schema attach error=%v", err)
	}
	q := adminRequestFixture()
	q.ExpectedSchema = 0
	if err := store.MigrateEnvironment(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateEnvironment(t.Context(), q); err != nil {
		t.Fatalf("migration retry was not idempotent: %v", err)
	}
	got, err := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if intNested(got.Object, "spec", "schemaVersion") != 2 || intNested(got.Object, "status", "schemaVersion") != 2 || intNested(got.Object, "status", "epoch") != 5 {
		t.Fatalf("migration did not advance schema and epoch: %v", got.Object)
	}
	refs, err := referenceRecords(got)
	if err != nil || len(refs) != 1 || refs[0].BindingID != "session-a" || refs[0].State != executionenv.ReferencePublished {
		t.Fatalf("migrated refs=%+v err=%v", refs, err)
	}
}

func TestUnknownSchemaMigrationRejectedWithoutRewrite(t *testing.T) {
	env := lifecycleAdminEnvironment(7, []any{})
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(dynamicClient, "ns", testProfiles(), nil).WithKubeClient(kubefake.NewSimpleClientset(terminalExecutor(), retainedPVC()))
	q := adminRequestFixture()
	q.ExpectedSchema = 7
	if err := store.MigrateEnvironment(t.Context(), q); err == nil {
		t.Fatal("unknown schema migrated")
	}
	got, _ := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if intNested(got.Object, "spec", "schemaVersion") != 7 || intNested(got.Object, "status", "epoch") != 4 {
		t.Fatalf("unknown schema was rewritten: %v", got.Object)
	}
}

func TestExpiredOperationLeaseFencesWithoutClearingIdentity(t *testing.T) {
	env := lifecycleAdminEnvironment(2, []any{})
	_ = unstructured.SetNestedMap(env.Object, map[string]any{"id": "peer-op", "operation": "file.replace", "claimID": "claim", "runID": "run", "epoch": int64(4), "holderID": "peer", "renewedAt": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), "expiresAt": time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)}, "status", "activeOperation")
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	r := NewReconciler(dynamicClient, kubefake.NewSimpleClientset(), "ns", testProfiles())
	if err := r.Reconcile(t.Context(), "env"); err != nil {
		t.Fatal(err)
	}
	got, _ := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if textNested(got.Object, "status", "fenceState") != "FenceUnknown" || textNested(got.Object, "status", "activeOperation", "id") != "peer-op" {
		t.Fatalf("expired operation was not retained and fenced: %v", got.Object["status"])
	}
}

func TestRecoverRequiresExactTerminalPodAndPersistsProof(t *testing.T) {
	env := lifecycleAdminEnvironment(2, []any{})
	_ = unstructured.SetNestedField(env.Object, "FenceUnknown", "status", "fenceState")
	_ = unstructured.SetNestedMap(env.Object, map[string]any{"id": "uncertain", "claimID": "claim", "epoch": int64(4)}, "status", "activeOperation")
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(dynamicClient, "ns", testProfiles(), nil).WithKubeClient(kubefake.NewSimpleClientset(terminalExecutor(), retainedPVC()))
	q := adminRequestFixture()
	q.OperationID = "recover-op"
	if err := store.RecoverEnvironment(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	got, _ := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if textNested(got.Object, "status", "fenceState") != fenceHealthy || textNested(got.Object, "status", "activeOperation", "id") != "" || textNested(got.Object, "status", "terminationProof", "podUID") != "pod-uid" {
		t.Fatalf("recovery proof/status=%v", got.Object["status"])
	}
}

func TestRecoveredTerminalProofCanStartExactReplacement(t *testing.T) {
	env := lifecycleAdminEnvironment(2, []any{})
	_ = unstructured.SetNestedField(env.Object, "FenceUnknown", "status", "fenceState")
	setConditionObject(env, "Ready", false, "FenceUnknown", "holder lost")
	_ = unstructured.SetNestedMap(env.Object, map[string]any{"id": "uncertain", "claimID": "claim", "epoch": int64(4)}, "status", "activeOperation")
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(dynamicClient, "ns", testProfiles(), nil).WithKubeClient(kubefake.NewSimpleClientset(terminalExecutor(), retainedPVC()))
	q := adminRequestFixture()
	q.OperationID = "recover-op"
	if err := store.RecoverEnvironment(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	q.OperationID = "replace-op"
	if err := store.ReplaceExecutor(t.Context(), q); err != nil {
		t.Fatalf("exact replacement after terminal recovery: %v", err)
	}
	got, _ := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if textNested(got.Object, "status", "lifecycleOperation", "id") != "replace-op" {
		t.Fatalf("replacement operation missing after recovery: %v", got.Object["status"])
	}
}

func TestRecoverMissingPodRemainsFenceUnknown(t *testing.T) {
	env := lifecycleAdminEnvironment(2, []any{})
	_ = unstructured.SetNestedField(env.Object, "FenceUnknown", "status", "fenceState")
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(dynamicClient, "ns", testProfiles(), nil).WithKubeClient(kubefake.NewSimpleClientset(retainedPVC()))
	if err := store.RecoverEnvironment(t.Context(), adminRequestFixture()); err == nil {
		t.Fatal("missing executor accepted as termination proof")
	}
	got, _ := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if textNested(got.Object, "status", "fenceState") != "FenceUnknown" || textNested(got.Object, "status", "terminationProof", "podUID") != "" {
		t.Fatalf("missing executor changed fenced state: %v", got.Object["status"])
	}
}

func TestReplacementQuiescesAndPendingReferenceBlocksRetirement(t *testing.T) {
	published := map[string]any{"bindingID": "session", "state": "Published", "operationID": "publish", "createdAt": time.Now().UTC().Format(time.RFC3339Nano)}
	env := lifecycleAdminEnvironment(2, []any{published})
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(dynamicClient, "ns", testProfiles(), nil)
	q := adminRequestFixture()
	if err := store.ReplaceExecutor(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	got, _ := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if textNested(got.Object, "status", "lifecycleOperation", "phase") != "Quiescing" || conditionTrue(got, "Ready") {
		t.Fatalf("replacement did not consume admission: %v", got.Object["status"])
	}
	if _, err := store.AcquireRun(t.Context(), q.Environment, "client", "owner", "session", "run", "acquire", time.Minute); err == nil {
		t.Fatal("run admitted during replacement")
	}

	pending := map[string]any{"bindingID": "pending", "state": "PendingDelete", "operationID": "delete", "createdAt": time.Now().UTC().Format(time.RFC3339Nano)}
	env = lifecycleAdminEnvironment(2, []any{pending})
	store = NewStore(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env), "ns", testProfiles(), nil)
	if err := store.RetireExact(t.Context(), q); err == nil {
		t.Fatal("pending reference did not block retirement")
	}
}

func TestReplacementPersistsTerminalProofBeforeRemovingPodFinalizer(t *testing.T) {
	env := lifecycleAdminEnvironment(2, []any{})
	_ = unstructured.SetNestedMap(env.Object, map[string]any{"id": "replace", "type": "ReplaceExecutor", "phase": "WaitingForTermination", "expectedEpoch": int64(4), "expectedPodUID": "pod-uid", "expectedPVCUID": "pvc-uid", "createdAt": time.Now().UTC().Format(time.RFC3339Nano)}, "status", "lifecycleOperation")
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	kube := kubefake.NewSimpleClientset(terminalExecutor(), retainedPVC())
	r := NewReconciler(dynamicClient, kube, "ns", testProfiles())
	if err := r.Reconcile(t.Context(), "env"); err != nil {
		t.Fatal(err)
	}
	got, _ := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if textNested(got.Object, "status", "terminationProof", "podUID") != "pod-uid" || textNested(got.Object, "status", "lifecycleOperation", "phase") != "RemovingPodFinalizer" {
		t.Fatalf("terminal proof was not persisted first: %v", got.Object["status"])
	}
	pod, _ := kube.CoreV1().Pods("ns").Get(t.Context(), "executor", metav1.GetOptions{})
	if !contains(pod.Finalizers, executorFinalizer) {
		t.Fatal("executor finalizer was removed in the proof-persistence transition")
	}
	if err := r.Reconcile(t.Context(), "env"); err != nil {
		t.Fatal(err)
	}
	pod, err := kube.CoreV1().Pods("ns").Get(t.Context(), "executor", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if contains(pod.Finalizers, executorFinalizer) {
		t.Fatal("executor finalizer remained after durable exact proof")
	}
	got, _ = dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if textNested(got.Object, "status", "lifecycleOperation", "phase") != "WaitingForPodDeletion" {
		t.Fatalf("phase=%v", got.Object["status"])
	}
}

func TestWrongPVCUIDCannotStartRetainedDeletion(t *testing.T) {
	env := lifecycleAdminEnvironment(2, []any{})
	setConditionObject(env, "Retired", true, "WorkspaceRetained", "retained")
	setConditionObject(env, "ExecutorTerminated", true, "TerminalPodProof", "proved")
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(dynamicClient, "ns", testProfiles(), nil)
	q := adminRequestFixture()
	q.ExpectedPVCUID = "wrong"
	if err := store.DeleteRetiredEnvironment(t.Context(), q); err == nil {
		t.Fatal("wrong PVC UID admitted for deletion")
	}
	got, _ := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(context.Background(), "env", metav1.GetOptions{})
	if textNested(got.Object, "status", "lifecycleOperation", "id") != "" {
		t.Fatalf("wrong UID created delete operation: %v", got.Object["status"])
	}
}
