package executioncontroller

import (
	"context"
	"errors"
	"fmt"
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
	statusField          = "status"
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
		if err := r.preflightProfiles(ctx); err != nil {
			r.initErr = err
			return
		}
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

func (r *Reconciler) preflightProfiles(ctx context.Context) error {
	runtimeClasses, storageClasses := r.profiles.clusterResources()
	for _, name := range runtimeClasses {
		if _, err := r.kube.NodeV1().RuntimeClasses().Get(ctx, name, metav1.GetOptions{}); err != nil {
			return fmt.Errorf("profile preflight: RuntimeClass %q unavailable: %w", name, err)
		}
	}
	for _, name := range storageClasses {
		if _, err := r.kube.StorageV1().StorageClasses().Get(ctx, name, metav1.GetOptions{}); err != nil {
			return fmt.Errorf("profile preflight: StorageClass %q unavailable: %w", name, err)
		}
	}
	return nil
}

// Ready reports whether startup fencing, profile preflight, and informer synchronization completed.
func (r *Reconciler) Ready() bool { return r.ready.Load() }

// ReadinessReason returns a bounded operator-facing reason class.
func (r *Reconciler) ReadinessReason() string {
	if r.ready.Load() {
		return "ready"
	}
	if r.initErr != nil && strings.HasPrefix(r.initErr.Error(), "profile preflight:") {
		return "profile-resource-unavailable"
	}
	if r.initErr != nil {
		return "controller-cache-unavailable"
	}
	return "controller-starting"
}

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
	for _, path := range [][]string{{"spec"}, {statusField, "activeOperation"}, {statusField, "lifecycleOperation"}, {statusField, "migrationOperation"}, {statusField, "references"}, {statusField, "fenceState"}} {
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
		now := r.now()
		if parseErr != nil || !now.Before(deadline) {
			return r.setFenceUnknown(ctx, env, "operation holder lease expired; operation identity retained for recovery")
		}
		r.queue.AddAfter(name, deadline.Sub(now))
	}
	profileName := textNested(env.Object, "spec", "profile")
	p, ok := r.profiles.get(profileName)
	if !ok || p.Digest != textNested(env.Object, "spec", "profileDigest") {
		return r.setCondition(ctx, env, "Ready", false, "InvalidProfile", "configured profile is unavailable or changed")
	}
	// The admitted durable delete owns finalization, including after DELETE has
	// set deletionTimestamp. A peer must not re-arm the generic finalizer.
	if textNested(env.Object, "status", "lifecycleOperation", "type") == deleteRetiredEnvironment {
		return r.reconcileLifecycle(ctx, env)
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
		if conditionTrue(env, "Retired") {
			return nil
		}
		return r.setCondition(ctx, env, "Ready", false, "UnsupportedRetirementRequest", "retirement requires the exact administrator lifecycle operation")
	}
	pvcName := resourceName("workspace", name)
	podName := resourceName("executor", name)
	pvc, err := r.ensurePVC(ctx, env, p, pvcName)
	if err != nil {
		return errors.Join(err, r.setCondition(ctx, env, "Ready", false, "PVCUnavailable", "workspace PVC unavailable"))
	}
	pod, err := r.ensurePod(ctx, env, p, podName, pvcName)
	if err != nil {
		return errors.Join(err, r.setCondition(ctx, env, "Ready", false, "ExecutorUnavailable", "executor Pod unavailable"))
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
		return cur, validatePVC(env, p, cur)
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	if textNested(env.Object, "status", "pvc", "uid") != "" {
		return nil, errors.New("authoritative PVC disappeared; replacement is forbidden")
	}
	qty := p.StorageSize
	mode := corev1.PersistentVolumeFilesystem
	created, err := pvcs.Create(ctx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"execution.mecatl.dev/environment": env.GetName(), "execution.mecatl.dev/revision": textNested(env.Object, "spec", "revision"), "execution.mecatl.dev/allocation-uid": string(env.GetUID())}}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, StorageClassName: &p.Spec.StorageClass, VolumeMode: &mode, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: qty}}}}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		created, err = pvcs.Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, err
	}
	return created, validatePVC(env, p, created)
}
func (r *Reconciler) ensurePod(ctx context.Context, env *unstructured.Unstructured, p resolvedProfile, name, pvc string) (*corev1.Pod, error) {
	pods := r.kube.CoreV1().Pods(r.namespace)
	cur, err := pods.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return cur, validatePod(env, p, pvc, cur)
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	if textNested(env.Object, "status", "pod", "uid") != "" {
		return nil, errors.Join(
			&executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "previous executor termination unconfirmed"},
			r.setFenceUnknown(ctx, env, "previous executor disappearance is not externally fenced"),
		)
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
		created, err = pods.Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, err
	}
	return created, validatePod(env, p, pvc, created)
}

func validatePVC(env *unstructured.Unstructured, p resolvedProfile, pvc *corev1.PersistentVolumeClaim) error {
	storedUID := textNested(env.Object, "status", "pvc", "uid")
	qty, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if pvc.Labels["execution.mecatl.dev/environment"] != env.GetName() || pvc.Labels["execution.mecatl.dev/revision"] != textNested(env.Object, "spec", "revision") || pvc.Labels["execution.mecatl.dev/allocation-uid"] != string(env.GetUID()) || len(pvc.OwnerReferences) != 0 || (storedUID != "" && storedUID != string(pvc.UID)) || pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != p.Spec.StorageClass || len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce || pvc.Spec.VolumeMode == nil || *pvc.Spec.VolumeMode != corev1.PersistentVolumeFilesystem || !ok || qty.Cmp(p.StorageSize) != 0 {
		return errors.New("PVC ownership or immutable specification mismatch")
	}
	return nil
}

func validatePod(env *unstructured.Unstructured, p resolvedProfile, pvcName string, pod *corev1.Pod) error { //nolint:gocyclo // Every security-sensitive immutable field is checked explicitly.
	if pod.DeletionTimestamp != nil {
		return errors.New("pod is terminating")
	}
	storedUID := textNested(env.Object, "status", "pod", "uid")
	if pod.Labels["execution.mecatl.dev/environment"] != env.GetName() || pod.Labels["execution.mecatl.dev/profile"] != hashText(textNested(env.Object, "spec", "profile"))[:16] || (storedUID != "" && storedUID != string(pod.UID)) || len(pod.OwnerReferences) != 1 {
		return errors.New("pod ownership identity mismatch")
	}
	owner := pod.OwnerReferences[0]
	if owner.APIVersion != env.GetAPIVersion() || owner.Kind != env.GetKind() || owner.Name != env.GetName() || owner.UID != env.GetUID() || owner.Controller == nil || !*owner.Controller || !reflect.DeepEqual(pod.Finalizers, []string{executorFinalizer}) {
		return errors.New("pod controller ownership mismatch")
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever || pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken || pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != p.Spec.RuntimeClassName || len(pod.Spec.Containers) != 1 || len(pod.Spec.InitContainers) != 0 || len(pod.Spec.EphemeralContainers) != 0 || len(pod.Spec.Volumes) != 2 {
		return errors.New("pod immutable specification mismatch")
	}
	psc := pod.Spec.SecurityContext
	if psc == nil || psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot || psc.RunAsUser == nil || *psc.RunAsUser != 65532 || psc.RunAsGroup == nil || *psc.RunAsGroup != 65532 || psc.FSGroup == nil || *psc.FSGroup != 65532 || psc.SeccompProfile == nil || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		return errors.New("pod security context mismatch")
	}
	c := pod.Spec.Containers[0]
	if c.Name != "executor" || c.Image != p.Spec.Image || !reflect.DeepEqual(c.Command, []string{"/bin/sh", "-c", "trap : TERM INT; sleep infinity & wait"}) || !reflect.DeepEqual(c.VolumeMounts, []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "tmp", MountPath: "/tmp"}}) {
		return errors.New("executor container specification mismatch")
	}
	cs := c.SecurityContext
	if cs == nil || cs.AllowPrivilegeEscalation == nil || *cs.AllowPrivilegeEscalation || cs.ReadOnlyRootFilesystem == nil || !*cs.ReadOnlyRootFilesystem || cs.RunAsNonRoot == nil || !*cs.RunAsNonRoot || cs.RunAsUser == nil || *cs.RunAsUser != 65532 || cs.Capabilities == nil || !reflect.DeepEqual(cs.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		return errors.New("executor container security mismatch")
	}
	wantRequests := corev1.ResourceList{corev1.ResourceCPU: p.CPURequest, corev1.ResourceMemory: p.MemoryRequest, corev1.ResourceEphemeralStorage: p.EphemeralStorageRequest}
	wantLimits := corev1.ResourceList{corev1.ResourceCPU: p.CPULimit, corev1.ResourceMemory: p.MemoryLimit, corev1.ResourceEphemeralStorage: p.EphemeralStorageLimit}
	if !reflect.DeepEqual(c.Resources.Requests, wantRequests) || !reflect.DeepEqual(c.Resources.Limits, wantLimits) || pod.Spec.Volumes[0].Name != "workspace" || pod.Spec.Volumes[0].PersistentVolumeClaim == nil || pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != pvcName || pod.Spec.Volumes[1].Name != "tmp" || pod.Spec.Volumes[1].EmptyDir == nil || pod.Spec.Volumes[1].EmptyDir.SizeLimit == nil || pod.Spec.Volumes[1].EmptyDir.SizeLimit.Cmp(p.TmpSizeLimit) != 0 {
		return errors.New("executor resources or volumes mismatch")
	}
	return nil
}

func (r *Reconciler) reconcileDeletion(ctx context.Context, _ dynamic.ResourceInterface, env *unstructured.Unstructured) error {
	return r.setCondition(ctx, env, "DeletionBlocked", true, "ExactLifecycleRequired", "deletion requires the exact retained-environment lifecycle operation")
}
func (r *Reconciler) updateRuntimeStatus(ctx context.Context, env *unstructured.Unstructured, pvc *corev1.PersistentVolumeClaim, pod *corev1.Pod, ready bool) error {
	return r.updateStatus(ctx, env, func(o *unstructured.Unstructured) error {
		if !runtimeObservationMatches(o, env) {
			return lifecycleConflict()
		}
		_ = unstructured.SetNestedMap(o.Object, map[string]any{"name": pvc.Name, "uid": string(pvc.UID)}, "status", "pvc")
		_ = unstructured.SetNestedMap(o.Object, map[string]any{"name": pod.Name, "uid": string(pod.UID)}, "status", "pod")
		_ = unstructured.SetNestedField(o.Object, o.GetGeneration(), "status", "observedGeneration")
		setConditionObject(o, "Ready", ready, "Reconciled", map[bool]string{true: "PVC and executor Pod are ready", false: "waiting for executor Pod readiness"}[ready])
		return nil
	})
}
func (r *Reconciler) setFenceUnknown(ctx context.Context, env *unstructured.Unstructured, msg string) error {
	return r.updateStatus(ctx, env, func(o *unstructured.Unstructured) error {
		if !runtimeObservationMatches(o, env) {
			return lifecycleConflict()
		}
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
	if p == nil || p.DeletionTimestamp != nil {
		return false
	}
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
