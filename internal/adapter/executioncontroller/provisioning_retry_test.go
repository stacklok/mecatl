package executioncontroller

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/util/workqueue"
)

func TestProvisioningWorkerConvergesWithoutAnotherEvent(t *testing.T) {
	for _, resource := range []string{"persistentvolumeclaims", "pods"} {
		t.Run(resource, func(t *testing.T) {
			ctx := t.Context()
			env := testEnvironment()
			env.SetFinalizers([]string{environmentFinalizer})
			d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			k := kubefake.NewSimpleClientset()
			var attempts, pvcCreates, podCreates atomic.Int32
			var restored atomic.Bool
			k.PrependReactor("create", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if action.GetResource().Resource == resource {
					attempts.Add(1)
					if !restored.Load() {
						return true, nil, errors.New("transient quota saturation")
					}
				}
				switch obj := action.(k8stesting.CreateAction).GetObject().(type) {
				case *corev1.PersistentVolumeClaim:
					pvcCreates.Add(1)
					obj.UID = "pvc-stable"
				case *corev1.Pod:
					podCreates.Add(1)
					obj.UID = "pod-stable"
					obj.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
				}
				return false, nil, nil
			})
			r := NewReconciler(d, k, "ns", testProfiles())
			r.queue.ShutDown()
			r.queue = workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedItemExponentialFailureRateLimiter[string](10*time.Millisecond, 10*time.Millisecond))
			done := make(chan struct{})
			go func() { r.worker(ctx); close(done) }()
			t.Cleanup(func() { r.queue.ShutDown(); <-done })
			start := time.Now()
			r.queue.Add(env.GetName())
			// Exceed a customary finite retry budget before restoring admission.
			waitProvisioning(t, func() bool { return attempts.Load() >= 24 })
			if time.Since(start) < 200*time.Millisecond {
				t.Fatal("provisioning retries hot-looped instead of rate limiting")
			}
			got, err := d.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, env.GetName(), metav1.GetOptions{})
			if err != nil || conditionTrue(got, "Ready") || conditionTransition(got, "Ready") == "" {
				t.Fatalf("failed provisioning did not retain Ready=false: env=%v err=%v", got, err)
			}
			if writes := statusUpdateCount(d.Actions()); writes != 1 {
				t.Fatalf("unchanged failure rewrote status: writes=%d", writes)
			}
			// No CR, runtime, or queue event: only the failed external prerequisite changes.
			restored.Store(true)
			waitProvisioning(t, func() bool {
				got, err = d.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, env.GetName(), metav1.GetOptions{})
				return err == nil && conditionTrue(got, "Ready") && r.queue.NumRequeues(env.GetName()) == 0
			})
			if textNested(got.Object, "status", "pvc", "uid") != "pvc-stable" || textNested(got.Object, "status", "pod", "uid") != "pod-stable" || pvcCreates.Load() != 1 || podCreates.Load() != 1 {
				t.Fatalf("duplicate or changed runtime identity: pvc=%d pod=%d status=%v", pvcCreates.Load(), podCreates.Load(), got.Object["status"])
			}
		})
	}
}

func waitProvisioning(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("worker did not converge without another event")
}

func TestProvisioningPreservesCreateAndConditionErrors(t *testing.T) {
	for _, resource := range []string{"persistentvolumeclaims", "pods"} {
		t.Run(resource, func(t *testing.T) {
			env := testEnvironment()
			env.SetFinalizers([]string{environmentFinalizer})
			d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			k := kubefake.NewSimpleClientset()
			createErr, statusErr := errors.New("create unavailable"), errors.New("status unavailable")
			k.PrependReactor("create", resource, func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, createErr })
			d.PrependReactor("update", "executionenvironments", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, statusErr })
			r := NewReconciler(d, k, "ns", testProfiles())
			t.Cleanup(r.queue.ShutDown)
			if err := r.Reconcile(t.Context(), env.GetName()); !errors.Is(err, createErr) || !errors.Is(err, statusErr) {
				t.Fatalf("lost provisioning/status error: %v", err)
			}
		})
	}
}

func TestProvisioningRetriesNeverReplaceAuthoritativeResources(t *testing.T) {
	for _, resource := range []string{"pvc", "pod"} {
		t.Run(resource, func(t *testing.T) {
			env := testEnvironment()
			env.SetFinalizers([]string{environmentFinalizer})
			if err := unstructured.SetNestedField(env.Object, "missing-authority", "status", resource, "uid"); err != nil {
				t.Fatal(err)
			}
			d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			k := kubefake.NewSimpleClientset()
			r := NewReconciler(d, k, "ns", testProfiles())
			t.Cleanup(r.queue.ShutDown)
			for range 3 {
				if err := r.Reconcile(t.Context(), env.GetName()); err == nil {
					t.Fatal("missing authoritative resource was not rejected")
				}
			}
			for _, action := range k.Actions() {
				if action.GetVerb() == "create" && (resource == "pvc" || action.GetResource().Resource == "pods") {
					t.Fatalf("replaced missing authority: %v", action)
				}
			}
		})
	}
}
