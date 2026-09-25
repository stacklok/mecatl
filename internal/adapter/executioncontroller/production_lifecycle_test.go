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
	k8stesting "k8s.io/client-go/testing"

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
	profile, _ := testProfiles().get("go")
	noPriv, nonroot, ro := false, true, true
	uid := int64(65532)
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "executor", Namespace: "ns", UID: types.UID("pod-uid"), Finalizers: []string{executorFinalizer}, Labels: map[string]string{"execution.mecatl.dev/environment": "env", "execution.mecatl.dev/profile": hashText("go")[:16]}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "execution.mecatl.dev/v1alpha1", Kind: "ExecutionEnvironment", Name: "env", UID: types.UID("env-uid"), Controller: &nonroot}}}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &noPriv, RuntimeClassName: &profile.Spec.RuntimeClassName, RestartPolicy: corev1.RestartPolicyNever, SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &nonroot, RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Containers: []corev1.Container{{Name: "executor", Image: profile.Spec.Image, Command: []string{"/bin/sh", "-c", "trap : TERM INT; sleep infinity & wait"}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &noPriv, ReadOnlyRootFilesystem: &ro, RunAsNonRoot: &nonroot, RunAsUser: &uid, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: profile.CPURequest, corev1.ResourceMemory: profile.MemoryRequest, corev1.ResourceEphemeralStorage: profile.EphemeralStorageRequest}, Limits: corev1.ResourceList{corev1.ResourceCPU: profile.CPULimit, corev1.ResourceMemory: profile.MemoryLimit, corev1.ResourceEphemeralStorage: profile.EphemeralStorageLimit}}, VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "tmp", MountPath: "/tmp"}}}}, Volumes: []corev1.Volume{{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "workspace"}}}, {Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &profile.TmpSizeLimit}}}}}, Status: corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{{Name: "executor", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137}}}}}}
}

func retainedPVC() *corev1.PersistentVolumeClaim {
	profile, _ := testProfiles().get("go")
	mode := corev1.PersistentVolumeFilesystem
	return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: "ns", UID: types.UID("pvc-uid"), Labels: map[string]string{"execution.mecatl.dev/environment": "env", "execution.mecatl.dev/revision": "rev", "execution.mecatl.dev/allocation-uid": "env-uid"}}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &profile.Spec.StorageClass, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, VolumeMode: &mode, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: profile.StorageSize}}}}
}

func adminRequestFixture() adminLifecycleRequest {
	return adminLifecycleRequest{Environment: executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, OwnerHash: "owner", Client: "client", ExpectedEpoch: 4, ExpectedPodUID: "pod-uid", ExpectedPVCUID: "pvc-uid", OperationID: "admin-operation"}
}

func TestHealthyTerminalPodCanStartLifecycleWithoutReadyCondition(t *testing.T) {
	env := lifecycleAdminEnvironment(2, []any{})
	setConditionObject(env, "Ready", false, "PodTerminated", "executor exited naturally")
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(dynamicClient, "ns", testProfiles(), nil).WithKubeClient(kubefake.NewSimpleClientset(terminalExecutor(), retainedPVC()))
	q := adminRequestFixture()
	if err := store.RetireExact(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if err := store.RetireExact(t.Context(), q); err != nil {
		t.Fatalf("retry was not idempotent: %v", err)
	}
	got, _ := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if textNested(got.Object, "status", "terminationProof", operationIDField) != q.OperationID || textNested(got.Object, "status", "lifecycleOperation", "id") != q.OperationID {
		t.Fatalf("terminal proof and lifecycle operation were not persisted: %v", got.Object["status"])
	}
}

func TestHealthyTerminalLifecycleRejectsIncompleteEvidence(t *testing.T) {
	cases := map[string]func(*corev1.Pod){
		"missing pod": func(*corev1.Pod) {},
		"init running": func(p *corev1.Pod) {
			p.Spec.InitContainers = []corev1.Container{{Name: "init"}}
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "init", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			env := lifecycleAdminEnvironment(2, []any{})
			setConditionObject(env, "Ready", false, "PodTerminated", "executor exited naturally")
			dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			objects := []runtime.Object{retainedPVC()}
			if name != "missing pod" {
				pod := terminalExecutor()
				mutate(pod)
				objects = append(objects, pod)
			}
			store := NewStore(dynamicClient, "ns", testProfiles(), nil).WithKubeClient(kubefake.NewSimpleClientset(objects...))
			if err := store.RetireExact(t.Context(), adminRequestFixture()); err == nil {
				t.Fatal("incomplete terminal evidence admitted lifecycle")
			}
			got, _ := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
			if textNested(got.Object, "status", "terminationProof", "podUID") != "" || textNested(got.Object, "status", "lifecycleOperation", "id") != "" {
				t.Fatalf("rejected evidence changed lifecycle state: %v", got.Object["status"])
			}
		})
	}
}

func TestRetirementCompletionReceiptReplaysOnlyExactOperation(t *testing.T) {
	env := lifecycleAdminEnvironment(2, []any{})
	q := adminRequestFixture()
	_ = unstructured.SetNestedMap(env.Object, terminationProof(q, terminalExecutor()), "status", "terminationProof")
	_ = unstructured.SetNestedMap(env.Object, map[string]any{"id": q.OperationID, "type": "RetireEnvironment", "phase": "WaitingForPodDeletion", "expectedEpoch": int64(4), "expectedPodUID": q.ExpectedPodUID, "expectedPVCUID": q.ExpectedPVCUID, "createdAt": time.Now().UTC().Format(time.RFC3339Nano)}, "status", "lifecycleOperation")
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	r := NewReconciler(dynamicClient, kubefake.NewSimpleClientset(retainedPVC()), "ns", testProfiles())
	if err := r.finishRetirement(t.Context(), env, q.OperationID); err != nil {
		t.Fatal(err)
	}
	store := NewStore(dynamicClient, "ns", testProfiles(), nil)
	if err := store.RetireExact(t.Context(), q); err != nil {
		t.Fatalf("lost-reply retry failed: %v", err)
	}
	q.OperationID = "different-operation"
	if err := store.RetireExact(t.Context(), q); err == nil {
		t.Fatal("different operation silently succeeded against retired environment")
	}
	q = adminRequestFixture()
	q.OwnerHash = "other-owner"
	if err := store.RetireExact(t.Context(), q); err == nil {
		t.Fatal("wrong subject replayed retirement receipt")
	}
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
	fromSchema, found, err := unstructured.NestedInt64(got.Object, "status", "lastMigrationFromSchema")
	if err != nil || !found || fromSchema != 0 {
		t.Fatal("schema-zero migration lost exact receipt presence")
	}
	unstructured.RemoveNestedField(got.Object, "status", "lastMigrationFromSchema")
	if _, err := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").UpdateStatus(t.Context(), got, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateEnvironment(t.Context(), q); !errors.As(err, &controlled) || controlled.Code != executionenv.CodeConflict {
		t.Fatalf("old receipt without source schema replayed: %v", err)
	}
}

func TestMigrationReceiptExpiresAfterReconciledReplacement(t *testing.T) {
	for name, schema := range map[string]int64{"legacy": 0, "prototype": 1} {
		t.Run(name, func(t *testing.T) {
			env := lifecycleAdminEnvironment(schema, []any{"session-a"})
			pod, pvc := terminalExecutor(), retainedPVC()
			pod.Name, pvc.Name = resourceName("executor", "env"), resourceName("workspace", "env")
			pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = pvc.Name
			_ = unstructured.SetNestedField(env.Object, pod.Name, "status", "pod", "name")
			_ = unstructured.SetNestedField(env.Object, pvc.Name, "status", "pvc", "name")
			d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			k := kubefake.NewSimpleClientset(pod, pvc)
			// The fake API must retain a finalizer-protected Pod until the real
			// reconciler has recorded terminal proof and removed its finalizer.
			k.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
				obj, err := k.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), "ns", action.(k8stesting.DeleteAction).GetName())
				if err != nil {
					return true, nil, err
				}
				return len(obj.(*corev1.Pod).Finalizers) != 0, nil, nil
			})
			k.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
				created := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
				created.UID = "replacement-pod"
				created.Status = replacementExecutor(true).Status
				return false, nil, nil
			})
			store := NewStore(d, "ns", testProfiles(), nil).WithKubeClient(k)
			q := adminRequestFixture()
			q.ExpectedSchema = schema
			if err := store.MigrateEnvironment(t.Context(), q); err != nil {
				t.Fatal(err)
			}
			if err := store.MigrateEnvironment(t.Context(), q); err != nil {
				t.Fatalf("exact receipt replay before replacement: %v", err)
			}
			replace := q
			replace.OperationID = "replace-after-migration"
			replace.ExpectedEpoch++
			if err := store.ReplaceExecutor(t.Context(), replace); err != nil {
				t.Fatal(err)
			}
			r := NewReconciler(d, k, "ns", testProfiles())
			t.Cleanup(r.queue.ShutDown)
			// Drive every replacement phase, then an ordinary ready reconcile.
			for range 8 {
				if err := r.Reconcile(t.Context(), "env"); err != nil {
					t.Fatal(err)
				}
			}
			got, err := d.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !conditionTrue(got, "Ready") || textNested(got.Object, "status", "pod", "uid") != "replacement-pod" || textNested(got.Object, "status", "pvc", "uid") != q.ExpectedPVCUID || intNested(got.Object, "status", "epoch") != 6 {
				t.Fatalf("replacement did not complete normally: %v", got.Object["status"])
			}
			changed := q
			changed.ExpectedPodUID = "replacement-pod"
			for _, replay := range []adminLifecycleRequest{changed, q} {
				var controlled *executionenv.Error
				if err := store.MigrateEnvironment(t.Context(), replay); !errors.As(err, &controlled) || controlled.Code != executionenv.CodeConflict {
					t.Fatalf("expired migration replay with Pod %q: %v", replay.ExpectedPodUID, err)
				}
			}
			if textNested(got.Object, "status", "lastMigrationOperationID") != "" {
				t.Fatal("replacement retained migration operation receipt")
			}
			if _, found, err := unstructured.NestedInt64(got.Object, "status", "lastMigrationFromSchema"); err != nil || found {
				t.Fatal("replacement retained migration source schema receipt")
			}
		})
	}
}

func TestInsecurePrototypeMigrationRejectedWithoutRewrite(t *testing.T) {
	env := lifecycleAdminEnvironment(1, []any{"session-a"})
	pod := terminalExecutor()
	automount := true
	pod.Spec.AutomountServiceAccountToken = &automount
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(dynamicClient, "ns", testProfiles(), nil).WithKubeClient(kubefake.NewSimpleClientset(pod, retainedPVC()))
	q := adminRequestFixture()
	q.ExpectedSchema = 1
	if err := store.MigrateEnvironment(t.Context(), q); err == nil {
		t.Fatal("insecure prototype executor migrated")
	}
	got, _ := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if intNested(got.Object, "spec", "schemaVersion") != 1 || intNested(got.Object, "status", "schemaVersion") != 1 || intNested(got.Object, "status", "epoch") != 4 {
		t.Fatalf("insecure prototype was rewritten: %v", got.Object)
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

func TestCompletedReplacementReceiptReplaysExactOperation(t *testing.T) {
	env := lifecycleAdminEnvironment(2, []any{map[string]any{"bindingID": "binding", "state": "Published", "operationID": "seed", "createdAt": time.Now().UTC().Format(time.RFC3339Nano)}})
	q := adminRequestFixture()
	_ = unstructured.SetNestedMap(env.Object, map[string]any{operationIDField: q.OperationID, "previousPodUID": q.ExpectedPodUID, "replacementPodUID": "new-pod", "pvcUID": q.ExpectedPVCUID, "previousEpoch": int64(q.ExpectedEpoch), "replacementEpoch": int64(q.ExpectedEpoch + 1)}, "status", "lastReplacement")
	_ = unstructured.SetNestedField(env.Object, int64(q.ExpectedEpoch+1), "status", "epoch")
	_ = unstructured.SetNestedMap(env.Object, map[string]any{"name": "executor", "uid": "new-pod"}, "status", "pod")
	store := NewStore(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env), "ns", testProfiles(), nil)
	if err := store.ReplaceExecutor(t.Context(), q); err != nil {
		t.Fatalf("exact completed replacement did not replay: %v", err)
	}
	q.OperationID = "different"
	if err := store.ReplaceExecutor(t.Context(), q); err == nil {
		t.Fatal("different operation replayed completed replacement")
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

func replacementExecutor(ready bool) *corev1.Pod {
	pod := terminalExecutor()
	pod.UID = types.UID("new-pod")
	pod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
	if ready {
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	}
	return pod
}

func replacementLifecycleEnvironment() *unstructured.Unstructured {
	env := lifecycleAdminEnvironment(2, []any{map[string]any{"bindingID": "binding", "state": "Published", "operationID": "seed", "createdAt": time.Now().UTC().Format(time.RFC3339Nano)}})
	q := adminRequestFixture()
	_ = unstructured.SetNestedMap(env.Object, terminationProof(q, terminalExecutor()), "status", "terminationProof")
	_ = unstructured.SetNestedMap(env.Object, map[string]any{"id": q.OperationID, "type": "ReplaceExecutor", "phase": "CreatingReplacement", "expectedEpoch": int64(4), "expectedPodUID": q.ExpectedPodUID, "expectedPVCUID": q.ExpectedPVCUID, "createdAt": "2026-09-21T00:00:00Z"}, "status", "lifecycleOperation")
	unstructured.RemoveNestedField(env.Object, "status", "pod")
	setConditionObject(env, "Ready", false, "ReplacementStarting", "waiting")
	return env
}

func TestStaleReplacementObservationCannotOverwriteCompletion(t *testing.T) {
	stale := replacementLifecycleEnvironment()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), stale.DeepCopy())
	kube := kubefake.NewSimpleClientset(replacementExecutor(false), retainedPVC())
	observed, release := make(chan struct{}), make(chan struct{})
	kube.PrependReactor("get", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		close(observed)
		<-release
		return false, nil, nil
	})
	r := NewReconciler(dynamicClient, kube, "ns", testProfiles())
	done := make(chan error, 1)
	go func() { done <- r.finishReplacement(t.Context(), stale, adminRequestFixture().OperationID) }()
	<-observed

	current, err := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedField(current.Object, int64(5), "status", "epoch")
	_ = unstructured.SetNestedMap(current.Object, map[string]any{"name": "executor", "uid": "new-pod"}, "status", "pod")
	_ = unstructured.SetNestedMap(current.Object, map[string]any{operationIDField: adminRequestFixture().OperationID, "previousPodUID": "pod-uid", "replacementPodUID": "new-pod", "pvcUID": "pvc-uid", "previousEpoch": int64(4), "replacementEpoch": int64(5)}, "status", "lastReplacement")
	unstructured.RemoveNestedField(current.Object, "status", "lifecycleOperation")
	setConditionObject(current, "Ready", true, "ReplacementReady", "replacement executor is ready on the retained workspace")
	if _, err := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").UpdateStatus(t.Context(), current, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	close(release)
	var controlled *executionenv.Error
	if err := <-done; !errors.As(err, &controlled) || controlled.Code != executionenv.CodeConflict {
		t.Fatalf("stale replacement result=%v", err)
	}
	got, _ := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if !conditionTrue(got, "Ready") || intNested(got.Object, "status", "epoch") != 5 || textNested(got.Object, "status", "pod", "uid") != "new-pod" {
		t.Fatalf("stale unready write damaged completion: %v", got.Object["status"])
	}
	store := NewStore(dynamicClient, "ns", testProfiles(), nil)
	if allocation, err := store.Attach(t.Context(), executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, "client", "owner", "binding"); err != nil || !allocation.Ready || allocation.Epoch != 5 {
		t.Fatalf("attach after completed replacement: allocation=%+v err=%v", allocation, err)
	}
}

func TestLifecycleStatusWritersRejectStaleObservations(t *testing.T) {
	t.Run("phase regression", func(t *testing.T) {
		stale := replacementLifecycleEnvironment()
		_ = unstructured.SetNestedField(stale.Object, "Quiescing", "status", "lifecycleOperation", "phase")
		current := stale.DeepCopy()
		_ = unstructured.SetNestedField(current.Object, "RemovingPodFinalizer", "status", "lifecycleOperation", "phase")
		client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), current)
		r := NewReconciler(client, kubefake.NewSimpleClientset(), "ns", testProfiles())
		if err := r.setLifecyclePhase(t.Context(), stale, adminRequestFixture().OperationID, "Quiescing", "WaitingForTermination"); err == nil {
			t.Fatal("stale phase transition succeeded")
		}
		got, _ := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
		if phase := textNested(got.Object, "status", "lifecycleOperation", "phase"); phase != "RemovingPodFinalizer" {
			t.Fatalf("phase regressed to %q", phase)
		}
	})

	t.Run("waiting and fence writes", func(t *testing.T) {
		stale := replacementLifecycleEnvironment()
		_ = unstructured.SetNestedField(stale.Object, "WaitingForTermination", "status", "lifecycleOperation", "phase")
		current := stale.DeepCopy()
		_ = unstructured.SetNestedField(current.Object, "RemovingPodFinalizer", "status", "lifecycleOperation", "phase")
		client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), current)
		r := NewReconciler(client, kubefake.NewSimpleClientset(), "ns", testProfiles())
		if err := r.setLifecycleCondition(t.Context(), stale, "Ready", false, "AwaitingTerminalExecutor", "waiting"); err == nil {
			t.Fatal("stale waiting condition succeeded")
		}
		if err := r.setLifecycleFenceUnknown(t.Context(), stale, "stale failure"); err == nil {
			t.Fatal("stale fence write succeeded")
		}
		got, _ := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
		if textNested(got.Object, "status", "fenceState") != fenceHealthy || textNested(got.Object, "status", "lifecycleOperation", "phase") != "RemovingPodFinalizer" {
			t.Fatalf("stale writer changed lifecycle status: %v", got.Object["status"])
		}
	})

	t.Run("ordinary reconcile after admission", func(t *testing.T) {
		stale := lifecycleAdminEnvironment(2, []any{})
		current := stale.DeepCopy()
		_ = unstructured.SetNestedMap(current.Object, map[string]any{"id": "replace", "type": "ReplaceExecutor", "phase": "Quiescing", "expectedEpoch": int64(4), "expectedPodUID": "pod-uid", "expectedPVCUID": "pvc-uid", "createdAt": "2026-09-21T00:00:00Z"}, "status", "lifecycleOperation")
		setConditionObject(current, "Ready", false, "Quiescing", "replacement admitted")
		client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), current)
		r := NewReconciler(client, kubefake.NewSimpleClientset(), "ns", testProfiles())
		if err := r.updateRuntimeStatus(t.Context(), stale, retainedPVC(), terminalExecutor(), true); err == nil {
			t.Fatal("ordinary reconcile overwrote lifecycle admission")
		}
		got, _ := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
		if conditionTrue(got, "Ready") || textNested(got.Object, "status", "lifecycleOperation", "id") != "replace" {
			t.Fatalf("ordinary reconcile damaged lifecycle admission: %v", got.Object["status"])
		}
	})
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
