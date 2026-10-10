package executioncontroller

import (
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestRevokedCrashExpiresOperationAndStopsExecutor(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "revoked"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			env := lifecycleAdminEnvironment(2, []any{})
			_ = unstructured.SetNestedMap(env.Object, map[string]any{"id": "crashed-operation", "claimID": "claim", "epoch": int64(4), "expiresAt": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)}, "status", "activeOperation")
			profiles := testProfiles()
			if missing {
				delete(profiles.revisions["go"], profiles.defaultRevision["go"])
			} else {
				profiles.eligibility["go"][profiles.defaultRevision["go"]] = TemplatePolicy{Revoked: true}
			}
			pod := terminalExecutor()
			pod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
			pod.ResourceVersion = "42"
			kube := kubefake.NewSimpleClientset(pod, retainedPVC())
			kube.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
				current, err := kube.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), "ns", action.(k8stesting.DeleteAction).GetName())
				if err != nil {
					return true, nil, err
				}
				terminating := current.(*corev1.Pod).DeepCopy()
				preconditions := action.(k8stesting.DeleteAction).GetDeleteOptions().Preconditions
				if preconditions == nil || preconditions.UID == nil || *preconditions.UID != terminating.UID || preconditions.ResourceVersion == nil || *preconditions.ResourceVersion != terminating.ResourceVersion {
					t.Fatal("revocation delete did not pin the observed Pod UID and resource version")
				}
				now := metav1.Now()
				terminating.DeletionTimestamp = &now
				return true, nil, kube.Tracker().Update(corev1.SchemeGroupVersion.WithResource("pods"), terminating, "ns")
			})
			dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			r := NewReconciler(dynamicClient, kube, "ns", profiles)
			t.Cleanup(r.queue.ShutDown)
			if err := r.Reconcile(t.Context(), "env"); err != nil {
				t.Fatal(err)
			}
			got, err := dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if textNested(got.Object, "status", "fenceState") != "FenceUnknown" || textNested(got.Object, "status", "activeOperation", "id") != "crashed-operation" || conditionTrue(got, "Ready") {
				t.Fatalf("crash fence lost identity: %v", got.Object["status"])
			}
			if retained, err := kube.CoreV1().Pods("ns").Get(t.Context(), "executor", metav1.GetOptions{}); err != nil || !contains(retained.Finalizers, executorFinalizer) || retained.DeletionTimestamp == nil || podTerminal(retained) {
				t.Fatalf("nonterminal executor proof was lost: %v %v", retained, err)
			}
			if _, err := kube.CoreV1().PersistentVolumeClaims("ns").Get(t.Context(), "workspace", metav1.GetOptions{}); err != nil {
				t.Fatalf("workspace lost: %v", err)
			}
			store := NewStore(dynamicClient, "ns", profiles, nil).WithKubeClient(kube)
			q := adminRequestFixture()
			if err := store.RecoverEnvironment(t.Context(), q); err == nil {
				t.Fatal("deleted Pod incorrectly proved termination")
			}
			got, _ = dynamicClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
			if textNested(got.Object, "status", "fenceState") != "FenceUnknown" || textNested(got.Object, "status", "activeOperation", "id") != "crashed-operation" {
				t.Fatalf("failed recovery changed fence: %v", got.Object["status"])
			}
		})
	}
}

func TestRevokedCrashWithTerminalPodCanRecoverAndRetire(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{true: "missing", false: "revoked"}[missing], func(t *testing.T) {
			env := lifecycleAdminEnvironment(2, []any{})
			_ = unstructured.SetNestedMap(env.Object, map[string]any{"id": "crashed-operation", "claimID": "claim", "epoch": int64(4), "expiresAt": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)}, "status", "activeOperation")
			profiles := testProfiles()
			if missing {
				delete(profiles.revisions["go"], profiles.defaultRevision["go"])
			} else {
				profiles.eligibility["go"][profiles.defaultRevision["go"]] = TemplatePolicy{Revoked: true}
			}
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			pod := terminalExecutor()
			pod.Status = corev1.PodStatus{Phase: corev1.PodRunning}
			pod.ResourceVersion = "42"
			kube := kubefake.NewSimpleClientset(pod, retainedPVC())
			kube.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
				current, err := kube.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), "ns", action.(k8stesting.DeleteAction).GetName())
				if err != nil {
					return true, nil, err
				}
				if len(current.(*corev1.Pod).Finalizers) == 0 {
					return false, nil, nil
				}
				if current.(*corev1.Pod).DeletionTimestamp == nil {
					terminating := current.(*corev1.Pod).DeepCopy()
					now := metav1.Now()
					terminating.DeletionTimestamp = &now
					if err := kube.Tracker().Update(corev1.SchemeGroupVersion.WithResource("pods"), terminating, "ns"); err != nil {
						return true, nil, err
					}
				}
				return true, nil, nil
			})
			r := NewReconciler(client, kube, "ns", profiles)
			t.Cleanup(r.queue.ShutDown)
			if err := r.Reconcile(t.Context(), "env"); err != nil {
				t.Fatal(err)
			}
			store := NewStore(client, "ns", profiles, nil).WithKubeClient(kube)
			q := adminRequestFixture()
			if err := store.RecoverEnvironment(t.Context(), q); !codeIs(err, executionenv.CodeFenceUnknown) {
				t.Fatalf("running executor incorrectly proved termination: %v", err)
			}
			stopped, err := kube.CoreV1().Pods("ns").Get(t.Context(), "executor", metav1.GetOptions{})
			if err != nil || !contains(stopped.Finalizers, executorFinalizer) || stopped.DeletionTimestamp == nil {
				t.Fatalf("executor finalizer did not retain identity: %v %v", stopped, err)
			}
			stopped.Status = terminalExecutor().Status
			if _, err := kube.CoreV1().Pods("ns").UpdateStatus(t.Context(), stopped, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			// A fresh reconciler and store must finish from persisted Kubernetes and CR state.
			r = NewReconciler(client, kube, "ns", profiles)
			t.Cleanup(r.queue.ShutDown)
			store = NewStore(client, "ns", profiles, nil).WithKubeClient(kube)
			if err := store.RecoverEnvironment(t.Context(), q); err != nil {
				t.Fatalf("terminal executor could not prove recovery: %v", err)
			}
			got, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
			if err != nil || textNested(got.Object, "status", "activeOperation", "id") != "" || textNested(got.Object, "status", "terminationProof", "podUID") != "pod-uid" {
				t.Fatalf("recovery did not retain exact proof: %v %v", got, err)
			}
			q.OperationID = "retire-after-crash"
			if err := store.RetireExact(t.Context(), q); err != nil {
				t.Fatalf("recovered environment cannot retire: %v", err)
			}
			for range 8 {
				if err := r.Reconcile(t.Context(), "env"); err != nil {
					t.Fatal(err)
				}
				current, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
				if err == nil && conditionTrue(current, "Retired") {
					break
				}
			}
			retired, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
			if err != nil || !conditionTrue(retired, "Retired") || !conditionTrue(retired, "ExecutorTerminated") {
				t.Fatalf("retirement did not finish with exact proof: %v %v", retired, err)
			}
			if _, err := kube.CoreV1().PersistentVolumeClaims("ns").Get(t.Context(), "workspace", metav1.GetOptions{}); err != nil {
				t.Fatalf("retirement lost retained workspace: %v", err)
			}
			q.OperationID = "delete-after-crash"
			if err := store.DeleteRetiredEnvironment(t.Context(), q); err != nil {
				t.Fatal(err)
			}
			for range 6 {
				if err := r.Reconcile(t.Context(), "env"); err != nil && !apierrors.IsNotFound(err) {
					t.Fatal(err)
				}
			}
			if _, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("retained environment not deleted: %v", err)
			}
			if _, err := kube.CoreV1().PersistentVolumeClaims("ns").Get(t.Context(), "workspace", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("retained workspace not deleted: %v", err)
			}
		})
	}
}

func TestMissingRevokedExecutorCannotProveTermination(t *testing.T) {
	env := lifecycleAdminEnvironment(2, []any{})
	_ = unstructured.SetNestedMap(env.Object, map[string]any{"id": "crashed", "expiresAt": time.Now().Add(-time.Minute).Format(time.RFC3339Nano)}, "status", "activeOperation")
	profiles := testProfiles()
	profiles.eligibility["go"][profiles.defaultRevision["go"]] = TemplatePolicy{Revoked: true}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	kube := kubefake.NewSimpleClientset(retainedPVC())
	r := NewReconciler(client, kube, "ns", profiles)
	t.Cleanup(r.queue.ShutDown)
	if err := r.Reconcile(t.Context(), "env"); err != nil {
		t.Fatal(err)
	}
	store := NewStore(client, "ns", profiles, nil).WithKubeClient(kube)
	if err := store.RecoverEnvironment(t.Context(), adminRequestFixture()); !codeIs(err, executionenv.CodeFenceUnknown) {
		t.Fatalf("missing Pod incorrectly proved terminal: %v", err)
	}
	got, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil || textNested(got.Object, "status", "fenceState") != "FenceUnknown" || textNested(got.Object, "status", "terminationProof", "podUID") != "" {
		t.Fatalf("missing Pod changed durable proof: %v %v", got, err)
	}
	if _, err := kube.CoreV1().PersistentVolumeClaims("ns").Get(t.Context(), "workspace", metav1.GetOptions{}); err != nil {
		t.Fatalf("missing Pod triggered workspace deletion: %v", err)
	}
}

func TestExpiredOperationFenceRevalidatesLeaseOnCASRetry(t *testing.T) {
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, changed := range []string{"renewed", "completed", "replaced"} {
		t.Run(changed, func(t *testing.T) {
			env := lifecycleAdminEnvironment(2, []any{})
			_ = unstructured.SetNestedMap(env.Object, map[string]any{"id": "old", "expiresAt": fixed.Add(-time.Minute).Format(time.RFC3339Nano)}, "status", "activeOperation")
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			resources := client.Resource(ExecutionEnvironmentGVR).Namespace("ns")
			r := NewReconciler(client, kubefake.NewSimpleClientset(), "ns", testProfiles())
			r.now = func() time.Time { return fixed }
			t.Cleanup(r.queue.ShutDown)
			conflicted := false
			client.PrependReactor("update", "executionenvironments", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if action.GetSubresource() != "status" || conflicted {
					return false, nil, nil
				}
				conflicted = true
				obj, err := client.Tracker().Get(ExecutionEnvironmentGVR, "ns", "env")
				if err != nil {
					return true, nil, err
				}
				current := obj.(*unstructured.Unstructured)
				switch changed {
				case "renewed":
					_ = unstructured.SetNestedField(current.Object, fixed.Add(time.Minute).Format(time.RFC3339Nano), "status", "activeOperation", "expiresAt")
				case "completed":
					unstructured.RemoveNestedField(current.Object, "status", "activeOperation")
				case "replaced":
					_ = unstructured.SetNestedField(current.Object, "new", "status", "activeOperation", "id")
				}
				if err := client.Tracker().Update(ExecutionEnvironmentGVR, current, "ns"); err != nil {
					return true, nil, err
				}
				return true, nil, apierrors.NewConflict(ExecutionEnvironmentGVR.GroupResource(), "env", errors.New("concurrent holder update"))
			})
			if err := r.Reconcile(t.Context(), "env"); !codeIs(err, executionenv.CodeConflict) {
				t.Fatalf("stale expiry did not conflict after %s: %v", changed, err)
			}
			if !conflicted {
				t.Fatal("CAS conflict was not exercised")
			}
			got, err := resources.Get(t.Context(), "env", metav1.GetOptions{})
			if err != nil || textNested(got.Object, "status", "fenceState") != fenceHealthy {
				t.Fatalf("stale expiry fenced %s operation: %v %v", changed, got, err)
			}
		})
	}
}

func TestExpiredOperationFenceCurrentAndMalformed(t *testing.T) {
	for _, expiry := range []string{time.Now().Add(-time.Minute).Format(time.RFC3339Nano), "malformed"} {
		env := lifecycleAdminEnvironment(2, []any{})
		_ = unstructured.SetNestedMap(env.Object, map[string]any{"id": "current", "expiresAt": expiry}, "status", "activeOperation")
		client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
		r := NewReconciler(client, kubefake.NewSimpleClientset(), "ns", testProfiles())
		t.Cleanup(r.queue.ShutDown)
		if err := r.Reconcile(t.Context(), "env"); err != nil {
			t.Fatal(err)
		}
		got, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
		if err != nil || textNested(got.Object, "status", "fenceState") != "FenceUnknown" || textNested(got.Object, "status", "activeOperation", "id") != "current" {
			t.Fatalf("current expired/malformed lease was not fenced: %v %v", got, err)
		}
	}
}

func TestPendingEnsureAfterDeprecationAndRevocation(t *testing.T) {
	profiles := testProfiles()
	owner := executionenv.Owner{Issuer: "issuer", Subject: "subject"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{ExecutionEnvironmentGVR: "ExecutionEnvironmentList"})
	store := NewStore(client, "ns", profiles, nil)
	revision := profiles.defaultRevision["go"]
	first, err := store.EnsurePendingOwnedRevision(t.Context(), "client", ownerHash(owner), owner, "binding", "go", revision, "fingerprint", "operation")
	if err != nil {
		t.Fatal(err)
	}
	profiles.eligibility["go"][revision] = TemplatePolicy{Deprecated: true}
	again, err := store.EnsurePendingOwnedRevision(t.Context(), "client", ownerHash(owner), owner, "binding", "go", revision, "fingerprint", "operation")
	if err != nil || again.Environment != first.Environment {
		t.Fatalf("deprecated retry: %+v %v", again, err)
	}
	if err := store.CommitReference(t.Context(), first.Environment, "client", ownerHash(owner), "binding", "operation"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsurePendingOwnedRevision(t.Context(), "client", ownerHash(owner), owner, "fresh", "go", revision, "fingerprint", "operation"); !codeIs(err, executionenv.CodeNotFound) {
		t.Fatalf("new allocation after deprecation: %v", err)
	}
	profiles.eligibility["go"][revision] = TemplatePolicy{Revoked: true}
	if _, err := store.EnsurePendingOwnedRevision(t.Context(), "client", ownerHash(owner), owner, "binding", "go", revision, "fingerprint", "operation"); !codeIs(err, executionenv.CodeNotFound) {
		t.Fatalf("revoked retry: %v", err)
	}
}

func TestForeignPrivateRequestsDoNotDiscloseRunOrTemplateState(t *testing.T) {
	clientID := "spiffe://cluster/ns/mecak8s"
	owner := executionenv.Owner{Issuer: "issuer", Subject: "subject"}
	for _, state := range []string{"active", "idle", "revoked", "missing-definition", "absent"} {
		for _, foreign := range []string{"owner", "client"} {
			t.Run(state+"/"+foreign, func(t *testing.T) {
				env := runFixtureEnvironment(7, 3)
				_ = unstructured.SetNestedField(env.Object, ownerHash(owner), "spec", "ownerHash")
				_ = unstructured.SetNestedField(env.Object, ownerHash(owner), "status", "activeRun", "ownerHash")
				_ = unstructured.SetNestedField(env.Object, hashText(clientID), "spec", "clientHash")
				_ = unstructured.SetNestedField(env.Object, hashText(clientID), "status", "activeRun", "clientHash")
				profiles := testProfiles()
				if foreign == "owner" {
					_ = unstructured.SetNestedField(env.Object, "another-owner", "spec", "ownerHash")
				} else {
					_ = unstructured.SetNestedField(env.Object, hashText("another-client"), "spec", "clientHash")
				}
				if state == "idle" {
					unstructured.RemoveNestedField(env.Object, "status", "activeRun")
				}
				if state == "revoked" {
					profiles.eligibility["go"][profiles.defaultRevision["go"]] = TemplatePolicy{Revoked: true}
				}
				if state == "missing-definition" {
					delete(profiles.revisions["go"], profiles.defaultRevision["go"])
				}
				objects := []runtime.Object{env}
				if state == "absent" {
					objects = nil
				}
				dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
				store := NewStore(dynamicClient, "ns", profiles, nil)
				h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{clientID: {MayAttestOwner: true}}}, store)
				ctx := authenticatedContext(clientID)
				rc := &executionv1.RequestContext{Environment: &executionv1.EnvironmentRef{Id: "env", Revision: "rev"}, Owner: &executionv1.Owner{Issuer: owner.Issuer, Subject: owner.Subject}, BindingId: "binding", RunId: "run", ClaimId: "claim", Epoch: 7, GrantGeneration: 3}
				if _, err := h.Files(ctx, &executionv1.FileRequest{Context: rc, Operation: executionv1.FileOperation_FILE_OPERATION_READ, Path: "file"}); status.Code(err) != codes.NotFound {
					t.Fatalf("file disclosed foreign state: %v", err)
				}
				if _, err := h.AcquireRun(ctx, &executionv1.AcquireRunRequest{Environment: rc.Environment, Owner: rc.Owner, BindingId: rc.BindingId, RunId: rc.RunId, OperationId: "new-run", TtlMillis: 60000}); status.Code(err) != codes.NotFound {
					t.Fatalf("acquire disclosed foreign state: %v", err)
				}
				if _, err := h.AttachEnvironment(ctx, &executionv1.AttachEnvironmentRequest{Context: rc, Purpose: executionenv.PurposeSession}); status.Code(err) != codes.NotFound {
					t.Fatalf("attach disclosed foreign state: %v", err)
				}
			})
		}
	}
}

func TestMatchingOwnerSeesActualRunAndTemplateState(t *testing.T) {
	clientID := "spiffe://cluster/ns/mecak8s"
	owner := executionenv.Owner{Issuer: "issuer", Subject: "subject"}
	for _, tc := range []struct {
		state string
		want  codes.Code
	}{{"active", codes.Aborted}, {"idle", codes.OK}, {"revoked", codes.Unavailable}, {"missing-definition", codes.Unavailable}} {
		t.Run(tc.state, func(t *testing.T) {
			env := runFixtureEnvironment(7, 3)
			_ = unstructured.SetNestedField(env.Object, ownerHash(owner), "spec", "ownerHash")
			_ = unstructured.SetNestedField(env.Object, ownerHash(owner), "status", "activeRun", "ownerHash")
			_ = unstructured.SetNestedField(env.Object, hashText(clientID), "spec", "clientHash")
			_ = unstructured.SetNestedField(env.Object, hashText(clientID), "status", "activeRun", "clientHash")
			profiles := testProfiles()
			switch tc.state {
			case "idle":
				unstructured.RemoveNestedField(env.Object, "status", "activeRun")
			case "revoked":
				profiles.eligibility["go"][profiles.defaultRevision["go"]] = TemplatePolicy{Revoked: true}
			case "missing-definition":
				delete(profiles.revisions["go"], profiles.defaultRevision["go"])
			}
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{clientID: {MayAttestOwner: true}}}, NewStore(client, "ns", profiles, nil))
			_, err := h.AcquireRun(authenticatedContext(clientID), &executionv1.AcquireRunRequest{Environment: &executionv1.EnvironmentRef{Id: "env", Revision: "rev"}, Owner: &executionv1.Owner{Issuer: owner.Issuer, Subject: owner.Subject}, BindingId: "binding", RunId: "new-run", OperationId: "new-run", TtlMillis: 60000})
			if got := status.Code(err); got != tc.want {
				t.Fatalf("matching owner %s: got %s, want %s: %v", tc.state, got, tc.want, err)
			}
		})
	}
}

func codeIs(err error, code executionenv.ErrorCode) bool {
	var controlled *executionenv.Error
	return errors.As(err, &controlled) && controlled.Code == code
}

func TestInitializeStatusRechecksImmutableSelectorUnderCAS(t *testing.T) {
	owner := executionenv.Owner{Issuer: "issuer", Subject: "subject"}
	profiles := testProfiles()
	revision := profiles.defaultRevision["go"]
	other := "v1-other-revision"
	profiles.revisions["go"][other] = profiles.revisions["go"][revision]
	profiles.eligibility["go"][other] = TemplatePolicy{}
	env := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": allocationName("client", ownerHash(owner), "binding"), "namespace": "ns"}, "spec": map[string]any{"schemaVersion": int64(2), "revision": "allocation-rev", "ownerHash": ownerHash(owner), "clientHash": hashText("client"), "bindingID": "binding", "requestFingerprint": "old-fp", "templateID": "go", "templateRevision": revision, "templateDigest": profiles.revisions["go"][revision].Digest, "desired": "Active"}}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	resources := client.Resource(ExecutionEnvironmentGVR).Namespace("ns")
	stale := env.DeepCopy()
	current, err := resources.Get(t.Context(), env.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedField(current.Object, other, "spec", "templateRevision")
	_ = unstructured.SetNestedField(current.Object, "winner-fp", "spec", "requestFingerprint")
	if _, err := resources.Update(t.Context(), current, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	store := NewStore(client, "ns", profiles, nil)
	if err := store.initializeStatus(t.Context(), stale, "client", ownerHash(owner), "binding", "go", revision, "old-fp", "loser-op"); !codeIs(err, executionenv.CodeConflict) {
		t.Fatalf("stale selector initialized winner: %v", err)
	}
	got, err := resources.Get(t.Context(), env.GetName(), metav1.GetOptions{})
	if err != nil || intNested(got.Object, "status", "epoch") != 0 {
		t.Fatalf("loser wrote winner status: %v %v", got, err)
	}
	if _, err := store.EnsurePendingOwnedRevision(t.Context(), "client", ownerHash(owner), owner, "binding", "go", other, "winner-fp", "winner-op"); err != nil {
		t.Fatalf("winner cannot initialize: %v", err)
	}
	if err := store.CommitReference(t.Context(), executionenv.EnvironmentRef{ID: env.GetName(), Revision: "allocation-rev"}, "client", ownerHash(owner), "binding", "winner-op"); err != nil {
		t.Fatalf("winner cannot commit: %v", err)
	}
}

func TestCompetingPendingEnsureCannotInitializeWinnersReference(t *testing.T) {
	owner := executionenv.Owner{Issuer: "issuer", Subject: "subject"}
	profiles := testProfiles()
	revision := profiles.defaultRevision["go"]
	other := "v1-other-revision"
	profiles.revisions["go"][other] = profiles.revisions["go"][revision]
	profiles.eligibility["go"][other] = TemplatePolicy{}
	env := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": allocationName("client", ownerHash(owner), "binding"), "namespace": "ns"}, "spec": map[string]any{"schemaVersion": int64(2), "revision": "allocation-rev", "ownerHash": ownerHash(owner), "clientHash": hashText("client"), "bindingID": "binding", "requestFingerprint": "winner-fp", "templateID": "go", "templateRevision": revision, "templateDigest": profiles.revisions["go"][revision].Digest, "desired": "Active"}}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	barrier := make(chan struct{})
	store := NewStore(client, "ns", profiles, nil)
	results := make(chan error, 2)
	for _, tc := range []struct{ rev, fp, op string }{{revision, "winner-fp", "winner-op"}, {other, "loser-fp", "loser-op"}} {
		go func() {
			<-barrier
			_, err := store.EnsurePendingOwnedRevision(t.Context(), "client", ownerHash(owner), owner, "binding", "go", tc.rev, tc.fp, tc.op)
			results <- err
		}()
	}
	close(barrier)
	var wins, losses int
	for range 2 {
		err := <-results
		if err == nil {
			wins++
		} else if codeIs(err, executionenv.CodeConflict) {
			losses++
		} else {
			t.Fatalf("unexpected ensure result: %v", err)
		}
	}
	if wins != 1 || losses != 1 {
		t.Fatalf("winner=%d loser=%d", wins, losses)
	}
	got, err := client.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), env.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !referenceOperationMatches(got, "binding", "winner-op") || referenceOperationMatches(got, "binding", "loser-op") {
		t.Fatalf("loser poisoned pending reference: %v", got.Object["status"])
	}
	if err := store.CommitReference(t.Context(), executionenv.EnvironmentRef{ID: env.GetName(), Revision: "allocation-rev"}, "client", ownerHash(owner), "binding", "winner-op"); err != nil {
		t.Fatalf("winner cannot commit: %v", err)
	}
}
