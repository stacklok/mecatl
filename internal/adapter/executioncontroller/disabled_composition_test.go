package executioncontroller

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/app"
)

func TestDisabledCompositionPreservesAllocationsAndIndependentReconciliation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx := t.Context()
	env := testEnvironment()
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	k := kubefake.NewClientset()
	provider := NewReconciler(d, k, "ns", testProfiles())
	// First persist the finalizer, then provision the runtime.
	if err := provider.Reconcile(ctx, env.GetName()); err != nil {
		t.Fatal(err)
	}
	if err := provider.Reconcile(ctx, env.GetName()); err != nil {
		t.Fatal(err)
	}
	resources := d.Resource(ExecutionEnvironmentGVR).Namespace("ns")
	before, err := resources.Get(ctx, env.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pods, err := k.CoreV1().Pods("ns").List(ctx, metav1.ListOptions{})
	if err != nil || len(pods.Items) != 1 {
		t.Fatalf("pods=%v err=%v", pods, err)
	}
	pvcs, err := k.CoreV1().PersistentVolumeClaims("ns").List(ctx, metav1.ListOptions{})
	if err != nil || len(pvcs.Items) != 1 {
		t.Fatalf("pvcs=%v err=%v", pvcs, err)
	}
	d.ClearActions()
	k.ClearActions()
	// No execution integration is passed to the real composition root. Existing
	// runtime objects belong to the independent provider, not the harness lifetime.
	built, err := app.Build(ctx, app.Config{UseMock: true, NoSoul: true, NoUserModel: true})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		built.Close()
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "offline")
	if err != nil {
		built.Close()
		t.Fatal(err)
	}
	for range run.Events() {
	}
	built.Service.FinishRun(sess.ID, run)
	built.Close()
	if len(d.Actions()) != 0 || len(k.Actions()) != 0 {
		t.Fatal("disabled composition touched provider resources")
	}
	after, err := resources.Get(ctx, env.GetName(), metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("allocation changed: %v", err)
	}
	afterPods, err := k.CoreV1().Pods("ns").List(ctx, metav1.ListOptions{})
	if err != nil || !reflect.DeepEqual(pods.Items, afterPods.Items) {
		t.Fatalf("executor changed: %v", err)
	}
	afterPVCs, err := k.CoreV1().PersistentVolumeClaims("ns").List(ctx, metav1.ListOptions{})
	if err != nil || !reflect.DeepEqual(pvcs.Items, afterPVCs.Items) {
		t.Fatalf("storage changed: %v", err)
	}
	pod := pods.Items[0].DeepCopy()
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if _, err := k.CoreV1().Pods("ns").UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Reconcile(ctx, env.GetName()); err != nil {
		t.Fatal(err)
	}
	ready, err := resources.Get(ctx, env.GetName(), metav1.GetOptions{})
	if err != nil || !conditionTrue(ready, "Ready") {
		t.Fatalf("independent provider failed to converge: %v", err)
	}
}
