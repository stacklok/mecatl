package executioncontroller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/stacklok/mecatl/internal/executionenv"
)

const (
	environmentFinalizer = "execution.mecatl.dev/retain-workspace"
	executorFinalizer    = "execution.mecatl.dev/verify-termination"
	fenceHealthy         = "Healthy"
)

// Reconciler maintains executor Pods and retained workspace PVCs.
type Reconciler struct {
	dynamic   dynamic.Interface
	kube      kubernetes.Interface
	namespace string
	profiles  *Profiles
	queue     workqueue.TypedRateLimitingInterface[string]
	initOnce  sync.Once
	initErr   error
	ready     atomic.Bool
	now       func() time.Time
}

// NewReconciler constructs a reconciler for one namespace.
func NewReconciler(d dynamic.Interface, k kubernetes.Interface, namespace string, p *Profiles) *Reconciler {
	return &Reconciler{dynamic: d, kube: k, namespace: namespace, profiles: p, queue: workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()), now: func() time.Time { return time.Now().UTC() }}
}

// Initialize synchronizes informer caches before the provider may expose mutating APIs.
// Startup never mutates operation ownership: another replica may still be its live holder.
func (r *Reconciler) Initialize(ctx context.Context) error {
	r.initOnce.Do(func() {
		factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(r.dynamic, 0, r.namespace, nil)
		environments := factory.ForResource(ExecutionEnvironmentGVR).Informer()
		_, err := environments.AddEventHandler(cache.ResourceEventHandlerFuncs{AddFunc: r.enqueue, UpdateFunc: r.enqueueUpdate})
		if err != nil {
			r.initErr = err
			return
		}
		runtimeFactory := informers.NewSharedInformerFactoryWithOptions(r.kube, 0, informers.WithNamespace(r.namespace), informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.LabelSelector = "execution.mecatl.dev/environment"
		}))
		pods := runtimeFactory.Core().V1().Pods().Informer()
		pvcs := runtimeFactory.Core().V1().PersistentVolumeClaims().Informer()
		for _, informer := range []cache.SharedIndexInformer{pods, pvcs} {
			if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{AddFunc: r.enqueueRuntime, UpdateFunc: func(_, next any) { r.enqueueRuntime(next) }, DeleteFunc: r.enqueueRuntime}); err != nil {
				r.initErr = err
				return
			}
		}
		factory.Start(ctx.Done())
		runtimeFactory.Start(ctx.Done())
		if !cache.WaitForCacheSync(ctx.Done(), environments.HasSynced, pods.HasSynced, pvcs.HasSynced) {
			r.initErr = errors.New("execution environment informer failed to sync")
			return
		}
		go wait.UntilWithContext(ctx, r.worker, time.Second)
		r.ready.Store(true)
	})
	return r.initErr
}

// Ready reports whether startup fencing and informer synchronization completed.
func (r *Reconciler) Ready() bool { return r.ready.Load() }

// Run initializes the reconciler and blocks until shutdown.
func (r *Reconciler) Run(ctx context.Context) error {
	if err := r.Initialize(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	r.queue.ShutDown()
	return nil
}
func (r *Reconciler) enqueue(obj any) {
	u, ok := obj.(*unstructured.Unstructured)
	if ok {
		r.queue.Add(u.GetName())
	}
}

func (r *Reconciler) enqueueUpdate(oldObj, newObj any) {
	oldEnv, oldOK := oldObj.(*unstructured.Unstructured)
	newEnv, newOK := newObj.(*unstructured.Unstructured)
	if !oldOK || !newOK || !reconcileInputsEqual(oldEnv, newEnv) {
		r.enqueue(newObj)
	}
}

func reconcileInputsEqual(oldEnv, newEnv *unstructured.Unstructured) bool {
	if oldEnv.GetGeneration() != newEnv.GetGeneration() ||
		!reflect.DeepEqual(oldEnv.GetDeletionTimestamp(), newEnv.GetDeletionTimestamp()) ||
		!reflect.DeepEqual(oldEnv.GetFinalizers(), newEnv.GetFinalizers()) {
		return false
	}
	for _, path := range [][]string{{"spec"}, {"status", "activeOperation"}, {"status", "references"}, {"status", "fenceState"}} {
		oldValue, _, _ := unstructured.NestedFieldNoCopy(oldEnv.Object, path...)
		newValue, _, _ := unstructured.NestedFieldNoCopy(newEnv.Object, path...)
		if !reflect.DeepEqual(oldValue, newValue) {
			return false
		}
	}
	return true
}

func (r *Reconciler) enqueueRuntime(obj any) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	var labels map[string]string
	switch value := obj.(type) {
	case *corev1.Pod:
		labels = value.Labels
	case *corev1.PersistentVolumeClaim:
		labels = value.Labels
	}
	if name := labels["execution.mecatl.dev/environment"]; name != "" {
		r.queue.Add(name)
	}
}
func (r *Reconciler) worker(ctx context.Context) {
	for r.process(ctx) {
	}
}
func (r *Reconciler) process(ctx context.Context) bool {
	name, shutdown := r.queue.Get()
	if shutdown {
		return false
	}
	defer r.queue.Done(name)
	if err := r.Reconcile(ctx, name); err != nil {
		r.queue.AddRateLimited(name)
	} else {
		r.queue.Forget(name)
	}
	return true
}

// Reconcile converges one ExecutionEnvironment and its runtime resources.
func (r *Reconciler) Reconcile(ctx context.Context, name string) error {
	res := r.dynamic.Resource(ExecutionEnvironmentGVR).Namespace(r.namespace)
	env, err := res.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if requireCurrentSchema(env) != nil {
		return r.setCondition(ctx, env, "Ready", false, "IncompatibleSchema", "explicit administrator migration to schema version 2 is required")
	}
	if expires := textNested(env.Object, "status", "activeOperation", "expiresAt"); expires != "" {
		deadline, parseErr := time.Parse(time.RFC3339Nano, expires)
		if parseErr != nil || !r.now().Before(deadline) {
			return r.setFenceUnknown(ctx, env, "operation holder lease expired; operation identity retained for recovery")
		}
	}
	profileName := textNested(env.Object, "spec", "profile")
	p, ok := r.profiles.get(profileName)
	if !ok || p.Digest != textNested(env.Object, "spec", "profileDigest") {
		return r.setCondition(ctx, env, "Ready", false, "InvalidProfile", "configured profile is unavailable or changed")
	}
	if env.GetDeletionTimestamp() != nil {
		return r.reconcileDeletion(ctx, res, env)
	}
	if !contains(env.GetFinalizers(), environmentFinalizer) {
		updated := env.DeepCopy()
		updated.SetFinalizers(append(updated.GetFinalizers(), environmentFinalizer))
		if _, err := res.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return err
		}
		return nil
	}
	if textNested(env.Object, "status", "lifecycleOperation", "id") != "" {
		return r.reconcileLifecycle(ctx, env)
	}
	if textNested(env.Object, "spec", "desired") == "Retiring" {
		return r.reconcileRetiring(ctx, env)
	}
	pvcName := resourceName("workspace", name)
	podName := resourceName("executor", name)
	pvc, err := r.ensurePVC(ctx, env, p, pvcName)
	if err != nil {
		return r.setCondition(ctx, env, "Ready", false, "PVCUnavailable", "workspace PVC unavailable")
	}
	pod, err := r.ensurePod(ctx, env, p, podName, pvcName)
	if err != nil {
		return r.setCondition(ctx, env, "Ready", false, "ExecutorUnavailable", "executor Pod unavailable")
	}
	ready := podReady(pod)
	if err := r.updateRuntimeStatus(ctx, env, pvc, pod, ready); err != nil {
		return err
	}
	if !ready {
		r.queue.AddAfter(name, time.Second)
	}
	return nil
}
func (r *Reconciler) ensurePVC(ctx context.Context, env *unstructured.Unstructured, p resolvedProfile, name string) (*corev1.PersistentVolumeClaim, error) {
	pvcs := r.kube.CoreV1().PersistentVolumeClaims(r.namespace)
	cur, err := pvcs.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if cur.Labels["execution.mecatl.dev/environment"] != env.GetName() || (textNested(env.Object, "status", "pvc", "uid") != "" && textNested(env.Object, "status", "pvc", "uid") != string(cur.UID)) {
			return nil, errors.New("PVC ownership identity mismatch")
		}
		return cur, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	if textNested(env.Object, "status", "pvc", "uid") != "" {
		return nil, errors.New("authoritative PVC disappeared; replacement is forbidden")
	}
	qty := p.StorageSize
	mode := corev1.PersistentVolumeFilesystem
	created, err := pvcs.Create(ctx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"execution.mecatl.dev/environment": env.GetName(), "execution.mecatl.dev/revision": textNested(env.Object, "spec", "revision")}}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, StorageClassName: &p.Spec.StorageClass, VolumeMode: &mode, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: qty}}}}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return pvcs.Get(ctx, name, metav1.GetOptions{})
	}
	return created, err
}
func (r *Reconciler) ensurePod(ctx context.Context, env *unstructured.Unstructured, p resolvedProfile, name, pvc string) (*corev1.Pod, error) {
	pods := r.kube.CoreV1().Pods(r.namespace)
	cur, err := pods.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if cur.Labels["execution.mecatl.dev/environment"] != env.GetName() || (textNested(env.Object, "status", "pod", "uid") != "" && textNested(env.Object, "status", "pod", "uid") != string(cur.UID)) {
			return nil, errors.New("pod ownership identity mismatch")
		}
		return cur, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	if textNested(env.Object, "status", "pod", "uid") != "" {
		_ = r.setFenceUnknown(ctx, env, "previous executor disappearance is not externally fenced")
		return nil, &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "previous executor termination unconfirmed"}
	}
	cpuReq := p.CPURequest
	memReq := p.MemoryRequest
	cpuLim := p.CPULimit
	memLim := p.MemoryLimit
	ephemeralReq := p.EphemeralStorageRequest
	ephemeralLim := p.EphemeralStorageLimit
	tmpLim := p.TmpSizeLimit
	nonroot := true
	uid := int64(65532)
	noPriv := false
	ro := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"execution.mecatl.dev/environment": env.GetName(), "execution.mecatl.dev/profile": hashText(textNested(env.Object, "spec", "profile"))[:16]}, Finalizers: []string{executorFinalizer}, OwnerReferences: []metav1.OwnerReference{{APIVersion: env.GetAPIVersion(), Kind: env.GetKind(), Name: env.GetName(), UID: env.GetUID(), Controller: &nonroot}}}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &noPriv, RuntimeClassName: &p.Spec.RuntimeClassName, RestartPolicy: corev1.RestartPolicyNever, SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &nonroot, RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Containers: []corev1.Container{{Name: "executor", Image: p.Spec.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/bin/sh", "-c", "trap : TERM INT; sleep infinity & wait"}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &noPriv, ReadOnlyRootFilesystem: &ro, RunAsNonRoot: &nonroot, RunAsUser: &uid, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: cpuReq, corev1.ResourceMemory: memReq, corev1.ResourceEphemeralStorage: ephemeralReq}, Limits: corev1.ResourceList{corev1.ResourceCPU: cpuLim, corev1.ResourceMemory: memLim, corev1.ResourceEphemeralStorage: ephemeralLim}}, VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "tmp", MountPath: "/tmp"}}}}, Volumes: []corev1.Volume{{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc}}}, {Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &tmpLim}}}}}}
	created, err := pods.Create(ctx, pod, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return pods.Get(ctx, name, metav1.GetOptions{})
	}
	return created, err
}
func (r *Reconciler) reconcileRetiring(ctx context.Context, env *unstructured.Unstructured) error {
	if textNested(env.Object, "status", "activeOperation", "id") != "" || textNested(env.Object, "status", "fenceState") != fenceHealthy {
		return r.setCondition(ctx, env, "Retired", false, "NotQuiescent", "retirement waits for proven quiescence")
	}
	refs, refsErr := referenceRecords(env)
	if refsErr != nil || len(refs) > 0 || textNested(env.Object, "status", "activeRun", "claimID") != "" {
		return r.setCondition(ctx, env, "Retired", false, "Referenced", "retirement refused while references remain")
	}
	podName := textNested(env.Object, "status", "pod", "name")
	if podName == "" {
		return r.setCondition(ctx, env, "Retired", false, "FenceUnknown", "executor identity is unavailable; external fencing proof is required")
	}
	pod, err := r.kube.CoreV1().Pods(r.namespace).Get(ctx, podName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if conditionTrue(env, "ExecutorTerminated") {
			return r.setCondition(ctx, env, "Retired", true, "WorkspaceRetained", "verified terminal executor removed; PVC retained")
		}
		return r.setCondition(ctx, env, "Retired", false, "FenceUnknown", "executor disappeared without a verified terminal state; external fencing proof is required")
	}
	if err != nil {
		return err
	}
	if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		return r.setCondition(ctx, env, "Retired", false, "PendingExternalFence", "executor is not terminal; no automatic force retirement is permitted")
	}
	if err := r.setCondition(ctx, env, "ExecutorTerminated", true, "TerminalPod", "executor Pod reported a terminal phase"); err != nil {
		return err
	}
	grace := int64(30)
	if err := r.kube.CoreV1().Pods(r.namespace).Delete(ctx, podName, metav1.DeleteOptions{GracePeriodSeconds: &grace}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
func (r *Reconciler) reconcileDeletion(ctx context.Context, res dynamic.ResourceInterface, env *unstructured.Unstructured) error {
	refs, refsErr := referenceRecords(env)
	if refsErr != nil || textNested(env.Object, "spec", "desired") != "Retiring" || !conditionTrue(env, "Retired") || len(refs) > 0 || textNested(env.Object, "status", "activeRun", "claimID") != "" || textNested(env.Object, "status", "activeOperation", "id") != "" || textNested(env.Object, "status", "fenceState") != fenceHealthy {
		return r.setCondition(ctx, env, "DeletionBlocked", true, "RetentionSafety", "finalizer requires explicit completed retirement and proven quiescence")
	}
	pod := textNested(env.Object, "status", "pod", "name")
	if pod != "" {
		grace := int64(30)
		if err := r.kube.CoreV1().Pods(r.namespace).Delete(ctx, pod, metav1.DeleteOptions{GracePeriodSeconds: &grace}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	updated := env.DeepCopy()
	next := []string{}
	for _, f := range updated.GetFinalizers() {
		if f != environmentFinalizer {
			next = append(next, f)
		}
	}
	updated.SetFinalizers(next)
	_, err := res.Update(ctx, updated, metav1.UpdateOptions{})
	return err
}
func (r *Reconciler) updateRuntimeStatus(ctx context.Context, env *unstructured.Unstructured, pvc *corev1.PersistentVolumeClaim, pod *corev1.Pod, ready bool) error {
	return r.updateStatus(ctx, env, func(o *unstructured.Unstructured) error {
		_ = unstructured.SetNestedMap(o.Object, map[string]any{"name": pvc.Name, "uid": string(pvc.UID)}, "status", "pvc")
		_ = unstructured.SetNestedMap(o.Object, map[string]any{"name": pod.Name, "uid": string(pod.UID)}, "status", "pod")
		_ = unstructured.SetNestedField(o.Object, o.GetGeneration(), "status", "observedGeneration")
		setConditionObject(o, "Ready", ready, "Reconciled", map[bool]string{true: "PVC and executor Pod are ready", false: "waiting for executor Pod readiness"}[ready])
		return nil
	})
}
func (r *Reconciler) setFenceUnknown(ctx context.Context, env *unstructured.Unstructured, msg string) error {
	return r.updateStatus(ctx, env, func(o *unstructured.Unstructured) error {
		_ = unstructured.SetNestedField(o.Object, "FenceUnknown", "status", "fenceState")
		setConditionObject(o, "Ready", false, "FenceUnknown", msg)
		return nil
	})
}
func (r *Reconciler) setCondition(ctx context.Context, env *unstructured.Unstructured, name string, status bool, reason, msg string) error {
	return r.updateStatus(ctx, env, func(o *unstructured.Unstructured) error { setConditionObject(o, name, status, reason, msg); return nil })
}
func (r *Reconciler) updateStatus(ctx context.Context, env *unstructured.Unstructured, fn func(*unstructured.Unstructured) error) error {
	res := r.dynamic.Resource(ExecutionEnvironmentGVR).Namespace(r.namespace)
	for i := 0; i < 5; i++ {
		cur, err := res.Get(ctx, env.GetName(), metav1.GetOptions{})
		if err != nil {
			return err
		}
		before, _, _ := unstructured.NestedMap(cur.Object, "status")
		if err := fn(cur); err != nil {
			return err
		}
		after, _, _ := unstructured.NestedMap(cur.Object, "status")
		if reflect.DeepEqual(before, after) {
			return nil
		}
		if _, err = res.UpdateStatus(ctx, cur, metav1.UpdateOptions{}); err == nil {
			return nil
		} else if !apierrors.IsConflict(err) {
			return err
		}
	}
	return errors.New("status update conflict limit exceeded")
}
func setConditionObject(o *unstructured.Unstructured, name string, status bool, reason, msg string) {
	conds, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
	st := "False"
	if status {
		st = "True"
	}
	replacement := map[string]any{"type": name, "status": st, "reason": reason, "message": msg, "observedGeneration": o.GetGeneration()}
	next := make([]any, 0, len(conds)+1)
	replaced := false
	for _, value := range conds {
		condition, ok := value.(map[string]any)
		if !ok || text(condition, "type") != name {
			next = append(next, value)
			continue
		}
		if replaced {
			continue
		}
		transitionTime := text(condition, "lastTransitionTime")
		if text(condition, "status") != st || transitionTime == "" {
			transitionTime = time.Now().UTC().Format(time.RFC3339)
		}
		replacement["lastTransitionTime"] = transitionTime
		next = append(next, replacement)
		replaced = true
	}
	if !replaced {
		replacement["lastTransitionTime"] = time.Now().UTC().Format(time.RFC3339)
		next = append(next, replacement)
	}
	_ = unstructured.SetNestedSlice(o.Object, next, "status", "conditions")
}
func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
func resourceName(prefix, env string) string {
	name := prefix + "-" + strings.TrimPrefix(env, "exec-")
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.TrimRight(name, "-")
}
