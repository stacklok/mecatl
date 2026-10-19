package executioncontroller

import (
	"context"
	"math"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/stacklok/mecatl/internal/executionenv"
)

type adminLifecycleRequest struct {
	Environment      executionenv.EnvironmentRef
	OwnerHash        string
	Client           string
	AdministratorFor []string
	ExpectedEpoch    uint64
	ExpectedPodUID   string
	ExpectedPVCUID   string
	OperationID      string
}

// ReplaceExecutor starts an exact, crash-recoverable executor replacement.
func (s *Store) ReplaceExecutor(ctx context.Context, q adminLifecycleRequest) error {
	return s.startLifecycle(ctx, q, "ReplaceExecutor", false)
}

// RetireExact starts exact executor retirement while retaining its workspace.
func (s *Store) RetireExact(ctx context.Context, q adminLifecycleRequest) error {
	return s.startLifecycle(ctx, q, "RetireEnvironment", true)
}

func (s *Store) startLifecycle(ctx context.Context, q adminLifecycleRequest, kind string, requireNoRefs bool) error { //nolint:gocyclo // Exact admission keeps all identity and receipt checks together.
	if q.ExpectedEpoch == 0 || q.ExpectedEpoch > math.MaxInt64 || q.ExpectedPodUID == "" || q.ExpectedPVCUID == "" || q.OperationID == "" {
		return &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "exact lifecycle identity is required"}
	}
	if err := s.persistObservedTerminalProof(ctx, q); err != nil {
		return err
	}
	return s.retryAdminStatus(ctx, q, func(o *unstructured.Unstructured) error {
		if kind == "ReplaceExecutor" && replacementReceiptMatches(o, q) {
			return nil
		}
		if err := exactAdminSubject(o, q); err != nil {
			return err
		}
		if kind == "RetireEnvironment" && conditionTrue(o, "Retired") {
			if exactTerminationProofOperationMatches(o, q) {
				return nil
			}
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment is already retired by another operation"}
		}
		if existing, found, _ := unstructured.NestedMap(o.Object, "status", "lifecycleOperation"); found {
			if text(existing, "id") == q.OperationID && text(existing, "type") == kind && text(existing, "expectedPodUID") == q.ExpectedPodUID && text(existing, "expectedPVCUID") == q.ExpectedPVCUID && intNested(existing, "expectedEpoch") == int64(q.ExpectedEpoch) { //nolint:gosec // validated above.
				return nil
			}
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "another lifecycle operation is active"}
		}
		if conditionTrue(o, "Retired") {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment is already retired"}
		}
		expiredRun := expiredLifecycleClaimMatches(o, q, s.now())
		if lifecycleAdmissionBlocked(o, q, expiredRun) {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment is not healthy and idle"}
		}
		refs, err := referenceRecords(o)
		if err != nil {
			return err
		}
		if requireNoRefs && len(refs) != 0 {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment still has references"}
		}
		if expiredRun {
			unstructured.RemoveNestedField(o.Object, "status", "activeRun")
		}
		operation := map[string]any{"id": q.OperationID, "type": kind, "phase": "Quiescing", "expectedEpoch": int64(q.ExpectedEpoch), "expectedPodUID": q.ExpectedPodUID, "expectedPVCUID": q.ExpectedPVCUID, "createdAt": s.now().Format(time.RFC3339Nano)} //nolint:gosec // validated above.
		setConditionObject(o, "Ready", false, "Quiescing", "administrator lifecycle operation is quiescing the executor")
		return unstructured.SetNestedMap(o.Object, operation, "status", "lifecycleOperation")
	})
}

func lifecycleAdmissionBlocked(o *unstructured.Unstructured, q adminLifecycleRequest, expiredRun bool) bool {
	return textNested(o.Object, "status", "activeRun", "claimID") != "" && !expiredRun ||
		textNested(o.Object, "status", "activeOperation", "id") != "" ||
		textNested(o.Object, "status", "fenceState") != fenceHealthy ||
		!conditionTrue(o, "Ready") && !exactTerminationProofMatches(o, q)
}

func expiredLifecycleClaimMatches(o *unstructured.Unstructured, q adminLifecycleRequest, now time.Time) bool {
	if textNested(o.Object, "status", "activeRun", "claimID") == "" {
		return false
	}
	claim, _, expiry, ok := activeRunFrom(o)
	return ok && !now.Before(expiry) && intNested(o.Object, "status", "activeRun", "epoch") == int64(q.ExpectedEpoch) && //nolint:gosec // q.ExpectedEpoch is bounded by startLifecycle.
		claim.Epoch == q.ExpectedEpoch && epochMatches(o, q.ExpectedEpoch) && generationMatches(o, claim.GrantGeneration)
}

func replacementReceiptMatches(o *unstructured.Unstructured, q adminLifecycleRequest) bool {
	return textNested(o.Object, "status", "lastReplacement", operationIDField) == q.OperationID &&
		textNested(o.Object, "status", "lastReplacement", "previousPodUID") == q.ExpectedPodUID &&
		textNested(o.Object, "status", "lastReplacement", "pvcUID") == q.ExpectedPVCUID &&
		intNested(o.Object, "status", "lastReplacement", "previousEpoch") == int64(q.ExpectedEpoch) //nolint:gosec // q.ExpectedEpoch is bounded by startLifecycle.
}

func exactTerminationProofMatches(o *unstructured.Unstructured, q adminLifecycleRequest) bool {
	return textNested(o.Object, "status", "terminationProof", "podUID") == q.ExpectedPodUID &&
		textNested(o.Object, "status", "terminationProof", "pvcUID") == q.ExpectedPVCUID &&
		intNested(o.Object, "status", "terminationProof", "epoch") == int64(q.ExpectedEpoch) //nolint:gosec // q.ExpectedEpoch is bounded above.
}

func exactTerminationProofOperationMatches(o *unstructured.Unstructured, q adminLifecycleRequest) bool {
	return exactTerminationProofMatches(o, q) && textNested(o.Object, "status", "terminationProof", operationIDField) == q.OperationID
}

func (s *Store) persistObservedTerminalProof(ctx context.Context, q adminLifecycleRequest) error { //nolint:gocyclo // Exact terminal proof deliberately fails closed at every observable mismatch.
	o, err := s.resources.Get(ctx, q.Environment.ID, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
	}
	if err != nil {
		return err
	}
	if err := adminSubject(o, q); err != nil {
		return err
	}
	if err := requireCurrentSchema(o); err != nil {
		return err
	}
	if replacementReceiptMatches(o, q) {
		return nil
	}
	if err := exactAdminSubject(o, q); err != nil {
		return err
	}
	if conditionTrue(o, "Ready") || conditionTrue(o, "Retired") || exactTerminationProofOperationMatches(o, q) {
		return nil
	}
	expiredRun := expiredLifecycleClaimMatches(o, q, s.now())
	if textNested(o.Object, "status", "activeRun", "claimID") != "" && !expiredRun || textNested(o.Object, "status", "activeOperation", "id") != "" || textNested(o.Object, "status", "fenceState") != fenceHealthy {
		return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment is not healthy and idle"}
	}
	if exactTerminationProofMatches(o, q) {
		return nil
	}
	if s.kube == nil {
		return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment is not healthy and idle"}
	}
	podName, pvcName := textNested(o.Object, "status", "pod", "name"), textNested(o.Object, "status", "pvc", "name")
	pod, err := s.kube.CoreV1().Pods(s.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil || string(pod.UID) != q.ExpectedPodUID || !podTerminal(pod) {
		return &executionenv.Error{Code: executionenv.CodeConflict, Message: "exact terminal executor proof is unavailable"}
	}
	pvc, err := s.kube.CoreV1().PersistentVolumeClaims(s.namespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil || string(pvc.UID) != q.ExpectedPVCUID {
		return &executionenv.Error{Code: executionenv.CodeConflict, Message: "exact workspace identity is unavailable"}
	}
	return s.retryAdminStatus(ctx, q, func(current *unstructured.Unstructured) error {
		if err := exactAdminSubject(current, q); err != nil {
			return err
		}
		if conditionTrue(current, "Ready") || exactTerminationProofOperationMatches(current, q) {
			return nil
		}
		expiredRun := expiredLifecycleClaimMatches(current, q, s.now())
		if textNested(current.Object, "status", "activeRun", "claimID") != "" && !expiredRun || textNested(current.Object, "status", "activeOperation", "id") != "" || textNested(current.Object, "status", "fenceState") != fenceHealthy {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment is not healthy and idle"}
		}
		if exactTerminationProofMatches(current, q) {
			return nil
		}
		return unstructured.SetNestedMap(current.Object, terminationProof(q, pod), "status", "terminationProof")
	})
}

// adminSubject uses only the policy captured by authentication for this request.
// The actor remains distinct from the immutable creator, including on receipt replay.
func adminSubject(o *unstructured.Unstructured, q adminLifecycleRequest) error {
	creator := textNested(o.Object, "spec", "clientHash")
	allowed := creator == hashText(q.Client)
	for _, uri := range q.AdministratorFor {
		allowed = allowed || creator == hashText(uri)
	}
	if !allowed || textNested(o.Object, "spec", "ownerHash") != q.OwnerHash || textNested(o.Object, "spec", "revision") != q.Environment.Revision {
		return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
	}
	return nil
}

func (s *Store) retryAdminStatus(ctx context.Context, q adminLifecycleRequest, mutate func(*unstructured.Unstructured) error) error {
	return s.retryUpdateStatusRaw(ctx, q.Environment.ID, func(o *unstructured.Unstructured) error {
		if err := adminSubject(o, q); err != nil {
			return err
		}
		if err := requireCurrentSchema(o); err != nil {
			return err
		}
		return mutate(o)
	})
}

func exactAdminSubject(o *unstructured.Unstructured, q adminLifecycleRequest) error {
	if err := adminSubject(o, q); err != nil {
		return err
	}
	if !epochMatches(o, q.ExpectedEpoch) || textNested(o.Object, "status", "pod", "uid") != q.ExpectedPodUID || textNested(o.Object, "status", "pvc", "uid") != q.ExpectedPVCUID {
		return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment execution identity changed"}
	}
	return nil
}

// RecoverEnvironment clears uncertainty only after built-in exact terminal verification.
func (s *Store) RecoverEnvironment(ctx context.Context, q adminLifecycleRequest) error {
	if s.kube == nil || q.ExpectedPodUID == "" || q.ExpectedPVCUID == "" || q.OperationID == "" {
		return &executionenv.Error{Code: executionenv.CodeNotReady, Message: "built-in recovery verifier is unavailable"}
	}
	podName, pvcName := "", ""
	if err := s.retryAdminStatus(ctx, q, func(o *unstructured.Unstructured) error {
		if err := exactAdminSubject(o, q); err != nil {
			return err
		}
		if exactTerminationProofOperationMatches(o, q) {
			return nil
		}
		if textNested(o.Object, "status", "fenceState") != "FenceUnknown" {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment is not fenced"}
		}
		podName, pvcName = textNested(o.Object, "status", "pod", "name"), textNested(o.Object, "status", "pvc", "name")
		return nil
	}); err != nil {
		return err
	}
	if podName == "" {
		return nil
	}
	pod, err := s.kube.CoreV1().Pods(s.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil || string(pod.UID) != q.ExpectedPodUID || !podTerminal(pod) {
		return &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "terminal executor proof unavailable; use the external-fencing operator runbook"}
	}
	pvc, err := s.kube.CoreV1().PersistentVolumeClaims(s.namespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil || string(pvc.UID) != q.ExpectedPVCUID {
		return &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "workspace identity cannot be verified"}
	}
	return s.retryAdminStatus(ctx, q, func(o *unstructured.Unstructured) error {
		if err := exactAdminSubject(o, q); err != nil {
			return err
		}
		if exactTerminationProofOperationMatches(o, q) {
			return nil
		}
		proof := terminationProof(q, pod)
		if err := unstructured.SetNestedMap(o.Object, proof, "status", "terminationProof"); err != nil {
			return err
		}
		unstructured.RemoveNestedField(o.Object, "status", "activeOperation")
		unstructured.RemoveNestedField(o.Object, "status", "activeRun")
		return unstructured.SetNestedField(o.Object, fenceHealthy, "status", "fenceState")
	})
}

func terminationProof(q adminLifecycleRequest, pod *corev1.Pod) map[string]any {
	return map[string]any{operationIDField: q.OperationID, "podUID": q.ExpectedPodUID, "pvcUID": q.ExpectedPVCUID, "epoch": int64(q.ExpectedEpoch), "podPhase": string(pod.Status.Phase), "observedAt": time.Now().UTC().Format(time.RFC3339Nano)} //nolint:gosec // epoch is validated by caller.
}

func podTerminal(pod *corev1.Pod) bool {
	if pod == nil || pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed || len(pod.Spec.Containers) == 0 {
		return false
	}
	if !allDeclaredContainersTerminated(pod.Spec.InitContainers, pod.Status.InitContainerStatuses) || !allDeclaredContainersTerminated(pod.Spec.Containers, pod.Status.ContainerStatuses) {
		return false
	}
	return allDeclaredContainersTerminated(pod.Spec.EphemeralContainers, pod.Status.EphemeralContainerStatuses)
}

func allDeclaredContainersTerminated[T interface {
	corev1.Container | corev1.EphemeralContainer
}](declared []T, statuses []corev1.ContainerStatus) bool {
	if len(statuses) != len(declared) {
		return false
	}
	names := make(map[string]struct{}, len(declared))
	for _, container := range declared {
		var name string
		switch c := any(container).(type) {
		case corev1.Container:
			name = c.Name
		case corev1.EphemeralContainer:
			name = c.Name
		}
		if name == "" {
			return false
		}
		names[name] = struct{}{}
	}
	if len(names) != len(declared) {
		return false
	}
	seen := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		if _, declared := names[status.Name]; !declared || status.State.Terminated == nil {
			return false
		}
		if _, duplicate := seen[status.Name]; duplicate {
			return false
		}
		seen[status.Name] = struct{}{}
	}
	return len(seen) == len(names)
}

// DeleteRetiredEnvironment starts exact deletion of an explicitly retained workspace.
func (s *Store) DeleteRetiredEnvironment(ctx context.Context, q adminLifecycleRequest) error {
	if q.ExpectedPVCUID == "" || q.OperationID == "" {
		return &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "exact retained workspace identity is required"}
	}
	return s.retryAdminStatus(ctx, q, func(o *unstructured.Unstructured) error {
		if textNested(o.Object, "status", "pvc", "uid") != q.ExpectedPVCUID {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "retained workspace identity changed"}
		}
		if existing, found, _ := unstructured.NestedMap(o.Object, "status", "lifecycleOperation"); found {
			if text(existing, "id") == q.OperationID && text(existing, "type") == deleteRetiredEnvironment {
				return nil
			}
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "another lifecycle operation is active"}
		}
		refs, err := referenceRecords(o)
		if err != nil || len(refs) != 0 || textNested(o.Object, "status", "activeRun", "claimID") != "" || textNested(o.Object, "status", "activeOperation", "id") != "" || !conditionTrue(o, "Retired") || !conditionTrue(o, "ExecutorTerminated") {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment is not safely retained and unreferenced"}
		}
		return unstructured.SetNestedMap(o.Object, map[string]any{"id": q.OperationID, "type": deleteRetiredEnvironment, "phase": "DeletingPVC", "expectedPVCUID": q.ExpectedPVCUID, "createdAt": s.now().Format(time.RFC3339Nano)}, "status", "lifecycleOperation")
	})
}
