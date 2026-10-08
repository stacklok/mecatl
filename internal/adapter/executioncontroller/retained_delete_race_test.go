package executioncontroller

import (
	"errors"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestRetainedDeletePeerBetweenFinalizerUpdateAndDelete(t *testing.T) {
	for _, mode := range []string{"peer-before-delete", "already-terminating", "stale-resource-version"} {
		t.Run(mode, func(t *testing.T) {
			env := lifecycleAdminEnvironment(2, []any{})
			env.SetResourceVersion("1")
			setConditionObject(env, "Retired", true, "WorkspaceRetained", "retained")
			setConditionObject(env, "ExecutorTerminated", true, "TerminalPodProof", "proved")
			d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			// The tracker does not implement API-server resource versions or CAS.
			versionedUpdate := func(a ktesting.Action) (bool, runtime.Object, error) {
				obj, err := d.Tracker().Get(ExecutionEnvironmentGVR, "ns", "env")
				if err != nil {
					return true, nil, err
				}
				cur := obj.(*unstructured.Unstructured)
				next := a.(ktesting.UpdateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
				version, err := strconv.Atoi(cur.GetResourceVersion())
				if err != nil || version < 1 || next.GetResourceVersion() == "" {
					t.Fatal("fixture lost API-server resource version")
				}
				if next.GetResourceVersion() != cur.GetResourceVersion() {
					return true, nil, apierrors.NewConflict(ExecutionEnvironmentGVR.GroupResource(), "env", errors.New("stale update"))
				}
				if !contains(cur.GetFinalizers(), environmentFinalizer) && contains(next.GetFinalizers(), environmentFinalizer) {
					t.Fatal("peer re-added finalizer during admitted retained deletion")
				}
				next.SetResourceVersion(strconv.Itoa(version + 1))
				return true, next, d.Tracker().Update(ExecutionEnvironmentGVR, next, "ns")
			}
			d.PrependReactor("update", "executionenvironments", versionedUpdate)
			k := kubefake.NewSimpleClientset(retainedPVC(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: profileAllocationConfigMap, Namespace: "ns"}, Data: map[string]string{profileAllocationKey("go"): `["env"]`}})
			store := NewStore(d, "ns", testProfiles(), nil).WithKubeClient(k)
			if err := store.DeleteRetiredEnvironment(t.Context(), adminRequestFixture()); err != nil {
				t.Fatal(err)
			}
			// Separate fake clients avoid the fake's per-client reactor lock, while both
			// reconcilers observe the same API-server tracker.
			peerClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
			peerClient.PrependReactor("*", "*", ktesting.ObjectReaction(d.Tracker()))
			peerClient.PrependReactor("update", "executionenvironments", versionedUpdate)
			peer := NewReconciler(peerClient, k, "ns", testProfiles())
			r := NewReconciler(d, k, "ns", testProfiles())
			t.Cleanup(peer.queue.ShutDown)
			t.Cleanup(r.queue.ShutDown)
			pvcsDeleted, envsDeleted := 0, 0
			k.PrependReactor("delete", "persistentvolumeclaims", func(a ktesting.Action) (bool, runtime.Object, error) {
				opts := a.(ktesting.DeleteAction).GetDeleteOptions()
				if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != "pvc-uid" {
					t.Fatal("PVC deletion lost exact UID")
				}
				pvcsDeleted++
				return false, nil, nil
			})
			staleDeletes := 0
			deletion := func(a ktesting.Action) (bool, runtime.Object, error) {
				opts := a.(ktesting.DeleteAction).GetDeleteOptions()
				if opts.Preconditions == nil || opts.Preconditions.ResourceVersion == nil || *opts.Preconditions.ResourceVersion == "" {
					t.Fatal("CR deletion lost nonempty resource version")
				}
				obj, err := d.Tracker().Get(ExecutionEnvironmentGVR, "ns", "env")
				if err != nil {
					return true, nil, err
				}
				cur := obj.(*unstructured.Unstructured)
				if opts.Preconditions.UID == nil || *opts.Preconditions.UID != cur.GetUID() {
					t.Fatal("CR deletion lost exact UID")
				}
				if *opts.Preconditions.ResourceVersion != cur.GetResourceVersion() {
					staleDeletes++
					return true, nil, apierrors.NewConflict(ExecutionEnvironmentGVR.GroupResource(), "env", errors.New("stale delete"))
				}
				if contains(cur.GetFinalizers(), environmentFinalizer) {
					t.Fatal("admitted retained deletion re-added finalizer")
				}
				envsDeleted++
				return true, nil, d.Tracker().Delete(ExecutionEnvironmentGVR, "ns", "env")
			}
			peerClient.PrependReactor("delete", "executionenvironments", deletion)
			interleaved := false
			d.PrependReactor("delete", "executionenvironments", func(a ktesting.Action) (bool, runtime.Object, error) {
				if !interleaved {
					interleaved = true
					if mode == "stale-resource-version" {
						res := peerClient.Resource(ExecutionEnvironmentGVR).Namespace("ns")
						cur, err := res.Get(t.Context(), "env", metav1.GetOptions{})
						if err != nil {
							t.Fatal(err)
						}
						cur.SetAnnotations(map[string]string{"peer-observation": "updated"})
						if _, err := res.Update(t.Context(), cur, metav1.UpdateOptions{}); err != nil {
							t.Fatal(err)
						}
					} else if err := peer.Reconcile(t.Context(), "env"); err != nil {
						t.Fatal(err)
					}
				}
				return deletion(a)
			})
			if mode == "already-terminating" {
				obj, _ := d.Tracker().Get(ExecutionEnvironmentGVR, "ns", "env")
				cur := obj.(*unstructured.Unstructured)
				now := metav1.Now()
				cur.SetDeletionTimestamp(&now)
				if _, err := peerClient.Resource(ExecutionEnvironmentGVR).Namespace("ns").Update(t.Context(), cur, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			conflicts := 0
			for range 5 {
				if err := r.Reconcile(t.Context(), "env"); err != nil {
					if mode != "stale-resource-version" || !apierrors.IsConflict(err) {
						t.Fatal(err)
					}
					conflicts++
					cur, getErr := d.Tracker().Get(ExecutionEnvironmentGVR, "ns", "env")
					if getErr != nil || contains(cur.(*unstructured.Unstructured).GetFinalizers(), environmentFinalizer) || envsDeleted != 0 {
						t.Fatal("stale deletion must preserve CR without re-adding finalizer")
					}
				}
			}
			wantConflicts := 0
			if mode == "stale-resource-version" {
				wantConflicts = 1
			}
			if !interleaved || staleDeletes != wantConflicts || conflicts != wantConflicts {
				t.Fatalf("interleaved=%v stale deletes=%d returned conflicts=%d", interleaved, staleDeletes, conflicts)
			}
			if _, err := d.Tracker().Get(ExecutionEnvironmentGVR, "ns", "env"); !apierrors.IsNotFound(err) {
				t.Fatalf("authorized deletion stranded: %v", err)
			}
			if pvcsDeleted != 1 || envsDeleted != 1 {
				t.Fatalf("successful deletes PVC=%d CR=%d", pvcsDeleted, envsDeleted)
			}
			if len(profileSlots(t, k)) != 0 {
				t.Fatal("capacity was not released")
			}
		})
	}
}

func TestRetainedDeleteDoesNotRemoveForeignCR(t *testing.T) {
	env := lifecycleAdminEnvironment(2, []any{})
	setConditionObject(env, "Retired", true, "WorkspaceRetained", "retained")
	setConditionObject(env, "ExecutorTerminated", true, "TerminalPodProof", "proved")
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	k := kubefake.NewSimpleClientset()
	store := NewStore(d, "ns", testProfiles(), nil).WithKubeClient(k)
	if err := store.DeleteRetiredEnvironment(t.Context(), adminRequestFixture()); err != nil {
		t.Fatal(err)
	}
	env, _ = d.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	_ = unstructured.SetNestedField(env.Object, "ReleasingSlot", "status", "lifecycleOperation", "phase")
	foreign := env.DeepCopy()
	foreign.SetUID("foreign")
	if err := d.Tracker().Update(ExecutionEnvironmentGVR, foreign, "ns"); err != nil {
		t.Fatal(err)
	}
	r := NewReconciler(d, k, "ns", testProfiles())
	t.Cleanup(r.queue.ShutDown)
	op, _, _ := unstructured.NestedMap(env.Object, "status", "lifecycleOperation")
	if err := r.reconcileRetainedDelete(t.Context(), env, op, "workspace", "pvc-uid"); err == nil {
		t.Fatal("foreign CR did not conflict")
	}
	got, err := d.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil || got.GetUID() != "foreign" || !contains(got.GetFinalizers(), environmentFinalizer) {
		t.Fatal("foreign CR touched")
	}
	for _, a := range k.Actions() {
		if a.GetVerb() != "get" {
			t.Fatal("foreign CR released capacity")
		}
	}
}

func TestDirectCRDeleteStillBlocked(t *testing.T) {
	env := lifecycleAdminEnvironment(2, []any{})
	now := metav1.Now()
	env.SetDeletionTimestamp(&now)
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	k := kubefake.NewSimpleClientset(retainedPVC(), terminalExecutor())
	k.PrependReactor("delete", "*", func(ktesting.Action) (bool, runtime.Object, error) {
		t.Error("direct delete destroyed runtime")
		return true, nil, apierrors.NewForbidden(schema.GroupResource{}, "", errors.New("unexpected"))
	})
	r := NewReconciler(d, k, "ns", testProfiles())
	t.Cleanup(r.queue.ShutDown)
	if err := r.Reconcile(t.Context(), "env"); err != nil {
		t.Fatal(err)
	}
	got, err := d.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil || !contains(got.GetFinalizers(), environmentFinalizer) {
		t.Fatal("direct deletion lost finalizer")
	}
	if !conditionTrue(got, "DeletionBlocked") {
		t.Fatal("direct deletion not blocked")
	}
}
