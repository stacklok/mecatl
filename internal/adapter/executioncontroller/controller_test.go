package executioncontroller

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
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

func TestExecutorAppliesAndValidatesProfileScheduling(t *testing.T) {
	ctx := t.Context()
	env := testEnvironment()
	profiles := testProfiles()
	profile, _ := profiles.get("go")
	seconds := int64(60)
	profile.Spec.NodeSelector = map[string]string{"node.kubernetes.io/instance-type": "worker"}
	profile.Spec.Tolerations = []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "build", Effect: corev1.TaintEffectNoSchedule}, {Key: "drain", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds}}
	profiles.byName["go"] = profile
	k := kubefake.NewSimpleClientset()
	r := NewReconciler(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env), k, "ns", profiles)
	for range 2 {
		if err := r.Reconcile(ctx, env.GetName()); err != nil {
			t.Fatal(err)
		}
	}
	pod, err := k.CoreV1().Pods("ns").Get(ctx, "executor-test", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defaultSeconds := int64(300)
	defaultSuffix := []corev1.Toleration{
		{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &defaultSeconds},
		{Key: "node.kubernetes.io/unreachable", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &defaultSeconds},
	}
	if !reflect.DeepEqual(pod.Spec.NodeSelector, profile.Spec.NodeSelector) || !reflect.DeepEqual(pod.Spec.Tolerations, append(append([]corev1.Toleration(nil), profile.Spec.Tolerations...), defaultSuffix...)) {
		t.Fatalf("scheduling not applied: %+v", pod.Spec)
	}
	legacy := pod.DeepCopy()
	legacy.Spec.Tolerations = append([]corev1.Toleration(nil), profile.Spec.Tolerations...)
	if err := validatePod(env, profile, "workspace-test", profiles.executorServiceAccount, legacy); err != nil {
		t.Fatalf("pre-default Pod rejected: %v", err)
	}
	custom := pod.DeepCopy()
	custom.Spec.Tolerations[2].TolerationSeconds = new(int64(600))
	custom.Spec.Tolerations[3].TolerationSeconds = new(int64(600))
	if err := validatePod(env, profile, "workspace-test", profiles.executorServiceAccount, custom); err != nil {
		t.Fatalf("older Pod with cluster default duration rejected: %v", err)
	}
	if _, err := k.CoreV1().Pods("ns").Update(ctx, custom, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx, env.GetName()); err != nil {
		t.Fatalf("older Pod with cluster defaults became unavailable: %v", err)
	}
	withoutProfileTolerations := profile
	withoutProfileTolerations.Spec.Tolerations = nil
	withoutProfileDefaults := pod.DeepCopy()
	withoutProfileDefaults.Spec.Tolerations = append([]corev1.Toleration(nil), custom.Spec.Tolerations[2:]...)
	if err := validatePod(env, withoutProfileTolerations, "workspace-test", profiles.executorServiceAccount, withoutProfileDefaults); err != nil {
		t.Fatalf("older Pod with no profile tolerations and cluster defaults rejected: %v", err)
	}
	for name, mutate := range map[string]func(*corev1.Pod){
		"unexpected toleration": func(p *corev1.Pod) {
			p.Spec.Tolerations = append(p.Spec.Tolerations, corev1.Toleration{Key: "injected", Operator: corev1.TolerationOpExists})
		},
		"changed default operator":  func(p *corev1.Pod) { p.Spec.Tolerations[2].Operator = corev1.TolerationOpEqual },
		"changed default value":     func(p *corev1.Pod) { p.Spec.Tolerations[2].Value = "injected" },
		"excessive default seconds": func(p *corev1.Pod) { p.Spec.Tolerations[2].TolerationSeconds = new(int64(86401)) },
		"changed selector":          func(p *corev1.Pod) { p.Spec.NodeSelector["injected"] = "true" },
		"injected affinity":         func(p *corev1.Pod) { p.Spec.Affinity = &corev1.Affinity{} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := pod.DeepCopy()
			mutate(changed)
			if err := validatePod(env, profile, "workspace-test", profiles.executorServiceAccount, changed); err == nil {
				t.Fatal("unexpected scheduling mutation accepted")
			}
		})
	}
	for _, tc := range []struct {
		name       string
		configured corev1.Toleration
		want       int
	}{
		{"matching key and effect with Equal value", corev1.Toleration{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpEqual, Value: "other", Effect: corev1.TaintEffectNoExecute}, 2},
		{"wildcard key", corev1.Toleration{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute}, 3},
		{"wrong effect", corev1.Toleration{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := expectedPodTolerations([]corev1.Toleration{tc.configured})
			if len(got) != tc.want || got[0] != tc.configured {
				t.Fatalf("default admission matching: %+v", got)
			}
		})
	}
}

func TestExecutorRequiresDedicatedServiceAccount(t *testing.T) {
	for _, name := range []string{"", "default"} {
		t.Run(name, func(t *testing.T) {
			env := testEnvironment()
			k := kubefake.NewSimpleClientset()
			r := NewReconciler(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env), k, "ns", testProfiles().WithExecutorServiceAccount(name))
			if err := r.Reconcile(t.Context(), env.GetName()); err != nil {
				t.Fatal(err)
			}
			if err := r.Reconcile(t.Context(), env.GetName()); err == nil {
				t.Fatal("missing or default account accepted")
			}
			pods, err := k.CoreV1().Pods("ns").List(t.Context(), metav1.ListOptions{})
			if err != nil || len(pods.Items) != 0 {
				t.Fatalf("pod created without dedicated account: %v: %v", pods, err)
			}
		})
	}
}

func TestExecutorPullIdentity(t *testing.T) {
	ctx := t.Context()
	env := testEnvironment()
	profiles := testProfiles().WithExecutorServiceAccount("release-mecatl-execution-executor")
	profile, _ := profiles.get("go")
	profile.Spec.ImagePullSecrets = []string{"registry-one", "registry.two"}
	profiles.byName["go"] = profile
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	k := kubefake.NewSimpleClientset()
	r := NewReconciler(d, k, "ns", profiles)
	for range 2 {
		if err := r.Reconcile(ctx, env.GetName()); err != nil {
			t.Fatal(err)
		}
	}
	pod, err := k.CoreV1().Pods("ns").Get(ctx, "executor-test", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pod.Spec.ServiceAccountName != profiles.executorServiceAccount || pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken || len(pod.Spec.ImagePullSecrets) != 2 || pod.Spec.ImagePullSecrets[0].Name != "registry-one" || pod.Spec.ImagePullSecrets[1].Name != "registry.two" {
		t.Fatalf("executor pull identity: %+v", pod.Spec)
	}
	for name, mutate := range map[string]func(*corev1.Pod){
		"default account":  func(p *corev1.Pod) { p.Spec.ServiceAccountName = "default" },
		"provider account": func(p *corev1.Pod) { p.Spec.ServiceAccountName = "release-mecatl-execution" },
		"token enabled":    func(p *corev1.Pod) { yes := true; p.Spec.AutomountServiceAccountToken = &yes },
		"injected secret": func(p *corev1.Pod) {
			p.Spec.ImagePullSecrets = append(p.Spec.ImagePullSecrets, corev1.LocalObjectReference{Name: "injected"})
		},
		"missing secret": func(p *corev1.Pod) { p.Spec.ImagePullSecrets = p.Spec.ImagePullSecrets[:1] },
		"reordered": func(p *corev1.Pod) {
			p.Spec.ImagePullSecrets[0], p.Spec.ImagePullSecrets[1] = p.Spec.ImagePullSecrets[1], p.Spec.ImagePullSecrets[0]
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := pod.DeepCopy()
			mutate(changed)
			if err := validatePod(env, profile, "workspace-test", profiles.executorServiceAccount, changed); err == nil {
				t.Fatal("anomalous Pod adopted")
			}
		})
	}
	profile.Spec.ImagePullSecrets = nil
	if err := validatePod(env, profile, "workspace-test", profiles.executorServiceAccount, pod); err == nil {
		t.Fatal("omitted pull list accepted injected secrets")
	}
	pod.Spec.ServiceAccountName = "default"
	if _, err := k.CoreV1().Pods("ns").Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx, env.GetName()); err == nil {
		t.Fatal("reconciliation adopted default account")
	}
}

func TestExecutorPullProfileDigestMismatchDoesNotAllocate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		drift string
	}{
		{name: "pull secrets", drift: "    imagePullSecrets: [registry]\n"},
		{name: "scheduling", drift: "    nodeSelector: {node.kubernetes.io/instance-type: worker}\n    tolerations: [{key: dedicated, operator: Equal, value: build, effect: NoSchedule}]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profiles.yaml")
			load := func(content string) *Profiles {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				profiles, err := LoadProfiles(path)
				if err != nil {
					t.Fatal(err)
				}
				return profiles.WithExecutorServiceAccount("release-executor")
			}
			original := load(validProfileYAML())
			old, _ := original.get("go")
			changed := load(validProfileYAML() + tc.drift)
			current, _ := changed.get("go")
			if current.Digest == old.Digest {
				t.Fatal("changed profile digest did not differ")
			}
			env := testEnvironment()
			if err := unstructured.SetNestedField(env.Object, old.Digest, "spec", "profileDigest"); err != nil {
				t.Fatal(err)
			}
			d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			k := kubefake.NewSimpleClientset()
			if err := NewReconciler(d, k, "ns", changed).Reconcile(t.Context(), env.GetName()); err != nil {
				t.Fatal(err)
			}
			got, err := d.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), env.GetName(), metav1.GetOptions{})
			if err != nil || conditionTrue(got, "Ready") || textNested(got.Object, "status", "pod", "uid") != "" || textNested(got.Object, "spec", "profileDigest") != old.Digest {
				t.Fatalf("old profile was adopted or changed: %v: %v", got, err)
			}
			pods, err := k.CoreV1().Pods("ns").List(t.Context(), metav1.ListOptions{})
			if err != nil || len(pods.Items) != 0 {
				t.Fatalf("Pod created for stale profile: %v: %v", pods, err)
			}
			pvcs, err := k.CoreV1().PersistentVolumeClaims("ns").List(t.Context(), metav1.ListOptions{})
			if err != nil || len(pvcs.Items) != 0 {
				t.Fatalf("PVC created for stale profile: %v: %v", pvcs, err)
			}
		})
	}
}

func TestAdmissionInjectedPullSecretFailsClosed(t *testing.T) {
	ctx := t.Context()
	env := testEnvironment()
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	k := kubefake.NewSimpleClientset()
	k.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pod := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
		pod.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "injected"}}
		return true, pod, nil
	})
	r := NewReconciler(d, k, "ns", testProfiles())
	if err := r.Reconcile(ctx, env.GetName()); err != nil {
		t.Fatal(err)
	}
	if err := r.Reconcile(ctx, env.GetName()); err == nil {
		t.Fatal("admission-injected Secret adopted")
	}
	got, err := d.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(ctx, env.GetName(), metav1.GetOptions{})
	if err != nil || conditionTrue(got, "Ready") || textNested(got.Object, "status", "pod", "uid") != "" {
		t.Fatalf("injected Pod became authoritative: %v: %v", got, err)
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

func TestInitializeRefusesRuntimeClassScheduling(t *testing.T) {
	for name, scheduling := range map[string]*nodev1.Scheduling{
		"selector":   {NodeSelector: map[string]string{"pool": "workers"}},
		"toleration": {Tolerations: []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists}}},
	} {
		t.Run(name, func(t *testing.T) {
			kube := kubefake.NewSimpleClientset(
				&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "sandboxed"}, Scheduling: scheduling},
				&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "standard"}},
			)
			r := NewReconciler(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), testEnvironment()), kube, "ns", testProfiles())
			if err := r.Initialize(t.Context()); err == nil || !strings.Contains(err.Error(), `RuntimeClass "sandboxed" must not define scheduling`) || r.Ready() {
				t.Fatalf("preflight accepted RuntimeClass scheduling: ready=%v error=%v", r.Ready(), err)
			}
			for _, action := range kube.Actions() {
				if action.GetVerb() == "create" && action.GetResource().Resource == "pods" {
					t.Fatal("preflight created a Pod")
				}
			}
		})
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
	return &Profiles{byName: map[string]resolvedProfile{"go": profile}, executorServiceAccount: "test-mecatl-execution-executor"}
}
func testEnvironment() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": "exec-test", "namespace": "ns", "uid": string(types.UID("uid"))}, "spec": map[string]any{"schemaVersion": int64(2), "profile": "go", "profileDigest": "sha256:profile", "revision": "rev", "desired": "Active"}, "status": map[string]any{"schemaVersion": int64(2), "epoch": int64(1), "references": []any{}, "fenceState": "Healthy"}}}
}
