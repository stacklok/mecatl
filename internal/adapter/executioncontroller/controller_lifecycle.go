package executioncontroller

import (
	"context"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func (r *Reconciler) reconcileLifecycle(ctx context.Context, env *unstructured.Unstructured) error { //nolint:gocyclo // Durable phases are deliberately explicit.
	op, found, err := unstructured.NestedMap(env.Object, "status", "lifecycleOperation")
	if err != nil || !found {
		return nil
	}
	kind, phase := text(op, "type"), text(op, "phase")
	operationID := text(op, "id")
	podUID, pvcUID := text(op, "expectedPodUID"), text(op, "expectedPVCUID")
	podName, pvcName := textNested(env.Object, "status", "pod", "name"), textNested(env.Object, "status", "pvc", "name")
	if operationID == "" || pvcUID == "" {
		return r.setFenceUnknown(ctx, env, "lifecycle operation identity is malformed")
	}
	if kind == "DeleteRetiredEnvironment" {
		return r.reconcileRetainedDelete(ctx, env, op, pvcName, pvcUID)
	}
	epoch := intNested(op, "expectedEpoch")
	if podUID == "" || epoch <= 0 || textNested(env.Object, "status", "pod", "uid") != podUID || textNested(env.Object, "status", "pvc", "uid") != pvcUID || intNested(env.Object, "status", "epoch") != epoch {
		return r.setFenceUnknown(ctx, env, "lifecycle operation no longer matches the exact executor and workspace")
	}
	pvc, err := r.kube.CoreV1().PersistentVolumeClaims(r.namespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil || string(pvc.UID) != pvcUID {
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return r.setFenceUnknown(ctx, env, "authoritative workspace identity is unavailable")
	}
	proofMatches := textNested(env.Object, "status", "terminationProof", operationIDField) == operationID && textNested(env.Object, "status", "terminationProof", "podUID") == podUID && textNested(env.Object, "status", "terminationProof", "pvcUID") == pvcUID && intNested(env.Object, "status", "terminationProof", "epoch") == epoch
	switch phase {
	case "Quiescing":
		pod, getErr := r.kube.CoreV1().Pods(r.namespace).Get(ctx, podName, metav1.GetOptions{})
		if getErr != nil || string(pod.UID) != podUID {
			if getErr != nil && !apierrors.IsNotFound(getErr) {
				return getErr
			}
			return r.setFenceUnknown(ctx, env, "executor disappeared before durable terminal proof")
		}
		uid := types.UID(podUID)
		grace := int64(30)
		if deleteErr := r.kube.CoreV1().Pods(r.namespace).Delete(ctx, podName, metav1.DeleteOptions{GracePeriodSeconds: &grace, Preconditions: &metav1.Preconditions{UID: &uid}}); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
			return deleteErr
		}
		return r.setLifecyclePhase(ctx, env, operationID, "WaitingForTermination")
	case "WaitingForTermination":
		pod, getErr := r.kube.CoreV1().Pods(r.namespace).Get(ctx, podName, metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			return r.setFenceUnknown(ctx, env, "executor disappeared before durable terminal proof")
		}
		if getErr != nil {
			return getErr
		}
		if string(pod.UID) != podUID {
			return r.setFenceUnknown(ctx, env, "executor UID changed during quiescing")
		}
		if !podTerminal(pod) {
			return r.setCondition(ctx, env, "Ready", false, "AwaitingTerminalExecutor", "waiting for kubelet terminal phase and terminated container states")
		}
		q := adminLifecycleRequest{ExpectedEpoch: uint64(epoch), ExpectedPodUID: podUID, ExpectedPVCUID: pvcUID, OperationID: operationID} //nolint:gosec // positivity checked above.
		return r.updateStatus(ctx, env, func(o *unstructured.Unstructured) error {
			current, ok, _ := unstructured.NestedMap(o.Object, "status", "lifecycleOperation")
			if !ok || text(current, "id") != operationID || text(current, "phase") != phase {
				return &executionenv.Error{Code: executionenv.CodeConflict, Message: "lifecycle operation changed concurrently"}
			}
			if err := unstructured.SetNestedMap(o.Object, terminationProof(q, pod), "status", "terminationProof"); err != nil {
				return err
			}
			current["phase"] = "RemovingPodFinalizer"
			return unstructured.SetNestedMap(o.Object, current, "status", "lifecycleOperation")
		})
	case "RemovingPodFinalizer":
		if !proofMatches {
			return r.setFenceUnknown(ctx, env, "durable terminal proof is missing or mismatched")
		}
		pod, getErr := r.kube.CoreV1().Pods(r.namespace).Get(ctx, podName, metav1.GetOptions{})
		if getErr == nil {
			if string(pod.UID) != podUID || !podTerminal(pod) {
				return r.setFenceUnknown(ctx, env, "terminal executor identity changed before finalizer removal")
			}
			pod = pod.DeepCopy()
			pod.Finalizers = withoutString(pod.Finalizers, executorFinalizer)
			if _, updateErr := r.kube.CoreV1().Pods(r.namespace).Update(ctx, pod, metav1.UpdateOptions{}); updateErr != nil {
				return updateErr
			}
		} else if !apierrors.IsNotFound(getErr) {
			return getErr
		}
		return r.setLifecyclePhase(ctx, env, operationID, "WaitingForPodDeletion")
	case "WaitingForPodDeletion":
		if !proofMatches {
			return r.setFenceUnknown(ctx, env, "durable terminal proof is missing or mismatched")
		}
		pod, getErr := r.kube.CoreV1().Pods(r.namespace).Get(ctx, podName, metav1.GetOptions{})
		if getErr == nil {
			if string(pod.UID) != podUID {
				return r.setFenceUnknown(ctx, env, "unexpected executor occupies the retained name")
			}
			uid := types.UID(podUID)
			if deleteErr := r.kube.CoreV1().Pods(r.namespace).Delete(ctx, podName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
				return deleteErr
			}
			return nil
		}
		if !apierrors.IsNotFound(getErr) {
			return getErr
		}
		if kind == "RetireEnvironment" {
			return r.finishRetirement(ctx, env, operationID)
		}
		return r.prepareReplacement(ctx, env, operationID)
	case "CreatingReplacement":
		return r.finishReplacement(ctx, env, operationID)
	default:
		return r.setFenceUnknown(ctx, env, "unknown lifecycle phase")
	}
}

func (r *Reconciler) setLifecyclePhase(ctx context.Context, env *unstructured.Unstructured, operationID, phase string) error {
	return r.updateStatus(ctx, env, func(o *unstructured.Unstructured) error {
		op, found, err := unstructured.NestedMap(o.Object, "status", "lifecycleOperation")
		if err != nil || !found || text(op, "id") != operationID {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "lifecycle operation changed concurrently"}
		}
		op["phase"] = phase
		return unstructured.SetNestedMap(o.Object, op, "status", "lifecycleOperation")
	})
}

func (r *Reconciler) finishRetirement(ctx context.Context, env *unstructured.Unstructured, operationID string) error {
	return r.updateStatus(ctx, env, func(o *unstructured.Unstructured) error {
		op, found, _ := unstructured.NestedMap(o.Object, "status", "lifecycleOperation")
		if !found || text(op, "id") != operationID {
			return errors.New("lifecycle operation changed concurrently")
		}
		unstructured.RemoveNestedField(o.Object, "status", "lifecycleOperation")
		setConditionObject(o, "ExecutorTerminated", true, "TerminalPodProof", "terminal executor proof is durable")
		setConditionObject(o, "Retired", true, "WorkspaceRetained", "executor retired; workspace PVC retained")
		setConditionObject(o, "Ready", false, "Retained", "environment is retired with retained storage")
		return nil
	})
}

func (r *Reconciler) prepareReplacement(ctx context.Context, env *unstructured.Unstructured, operationID string) error {
	return r.updateStatus(ctx, env, func(o *unstructured.Unstructured) error {
		op, found, _ := unstructured.NestedMap(o.Object, "status", "lifecycleOperation")
		if !found || text(op, "id") != operationID || text(op, "phase") != "WaitingForPodDeletion" {
			return errors.New("lifecycle operation changed concurrently")
		}
		unstructured.RemoveNestedField(o.Object, "status", "pod")
		op["phase"] = "CreatingReplacement"
		return unstructured.SetNestedMap(o.Object, op, "status", "lifecycleOperation")
	})
}

func (r *Reconciler) finishReplacement(ctx context.Context, env *unstructured.Unstructured, operationID string) error {
	profile, ok := r.profiles.get(textNested(env.Object, "spec", "profile"))
	if !ok || profile.Digest != textNested(env.Object, "spec", "profileDigest") {
		return r.setFenceUnknown(ctx, env, "replacement profile is unavailable")
	}
	pvcName := textNested(env.Object, "status", "pvc", "name")
	pod, err := r.ensurePod(ctx, env, profile, resourceName("executor", env.GetName()), pvcName)
	if err != nil {
		return err
	}
	if !podReady(pod) {
		return r.setCondition(ctx, env, "Ready", false, "ReplacementStarting", "waiting for replacement executor readiness")
	}
	return r.updateStatus(ctx, env, func(o *unstructured.Unstructured) error {
		op, found, _ := unstructured.NestedMap(o.Object, "status", "lifecycleOperation")
		if !found || text(op, "id") != operationID || text(op, "phase") != "CreatingReplacement" {
			return errors.New("lifecycle operation changed concurrently")
		}
		epoch := intNested(o.Object, "status", "epoch")
		if epoch <= 0 {
			return errors.New("execution epoch cannot advance")
		}
		_ = unstructured.SetNestedField(o.Object, epoch+1, "status", "epoch")
		_ = unstructured.SetNestedMap(o.Object, map[string]any{"name": pod.Name, "uid": string(pod.UID)}, "status", "pod")
		unstructured.RemoveNestedField(o.Object, "status", "lifecycleOperation")
		setConditionObject(o, "Ready", true, "ReplacementReady", "replacement executor is ready on the retained workspace")
		return nil
	})
}

func (r *Reconciler) reconcileRetainedDelete(ctx context.Context, env *unstructured.Unstructured, op map[string]any, pvcName, pvcUID string) error {
	if text(op, "phase") != "DeletingPVC" || !conditionTrue(env, "Retired") || !conditionTrue(env, "ExecutorTerminated") {
		return r.setFenceUnknown(ctx, env, "retained deletion state is invalid")
	}
	pvc, err := r.kube.CoreV1().PersistentVolumeClaims(r.namespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err == nil {
		if string(pvc.UID) != pvcUID {
			return r.setFenceUnknown(ctx, env, "retained PVC UID changed")
		}
		uid := types.UID(pvcUID)
		if deleteErr := r.kube.CoreV1().PersistentVolumeClaims(r.namespace).Delete(ctx, pvcName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
			return deleteErr
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	res := r.dynamic.Resource(ExecutionEnvironmentGVR).Namespace(r.namespace)
	current, err := res.Get(ctx, env.GetName(), metav1.GetOptions{})
	if err != nil {
		return err
	}
	current.SetFinalizers(withoutString(current.GetFinalizers(), environmentFinalizer))
	updated, err := res.Update(ctx, current, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	uid := updated.GetUID()
	if err := res.Delete(ctx, updated.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return releaseProfileSlot(ctx, r.kube, r.namespace, textNested(updated.Object, "spec", "profile"), updated.GetName())
}

func withoutString(values []string, remove string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != remove {
			out = append(out, value)
		}
	}
	return out
}
