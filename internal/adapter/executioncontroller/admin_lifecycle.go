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
	Environment    executionenv.EnvironmentRef
	OwnerHash      string
	Client         string
	ExpectedEpoch  uint64
	ExpectedPodUID string
	ExpectedPVCUID string
	OperationID    string
	ExpectedSchema int64
}

// ReplaceExecutor starts an exact, crash-recoverable executor replacement.
func (s *Store) ReplaceExecutor(ctx context.Context, q adminLifecycleRequest) error {
	return s.startLifecycle(ctx, q, "ReplaceExecutor", false)
}

// RetireExact starts exact executor retirement while retaining its workspace.
func (s *Store) RetireExact(ctx context.Context, q adminLifecycleRequest) error {
	return s.startLifecycle(ctx, q, "RetireEnvironment", true)
}

func (s *Store) startLifecycle(ctx context.Context, q adminLifecycleRequest, kind string, requireNoRefs bool) error {
	if q.ExpectedEpoch == 0 || q.ExpectedEpoch > math.MaxInt64 || q.ExpectedPodUID == "" || q.ExpectedPVCUID == "" || q.OperationID == "" {
		return &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "exact lifecycle identity is required"}
	}
	return s.retryUpdateStatus(ctx, q.Environment.ID, func(o *unstructured.Unstructured) error {
		if err := exactAdminSubject(o, q); err != nil {
			return err
		}
		if existing, found, _ := unstructured.NestedMap(o.Object, "status", "lifecycleOperation"); found {
			if text(existing, "id") == q.OperationID && text(existing, "type") == kind && text(existing, "expectedPodUID") == q.ExpectedPodUID && text(existing, "expectedPVCUID") == q.ExpectedPVCUID && intNested(existing, "expectedEpoch") == int64(q.ExpectedEpoch) { //nolint:gosec // validated above.
				return nil
			}
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "another lifecycle operation is active"}
		}
		if textNested(o.Object, "status", "activeRun", "claimID") != "" || textNested(o.Object, "status", "activeOperation", "id") != "" || textNested(o.Object, "status", "fenceState") != fenceHealthy || !conditionTrue(o, "Ready") {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment is not healthy and idle"}
		}
		refs, err := referenceRecords(o)
		if err != nil {
			return err
		}
		if requireNoRefs && len(refs) != 0 {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment still has references"}
		}
		operation := map[string]any{"id": q.OperationID, "type": kind, "phase": "Quiescing", "expectedEpoch": int64(q.ExpectedEpoch), "expectedPodUID": q.ExpectedPodUID, "expectedPVCUID": q.ExpectedPVCUID, "createdAt": s.now().Format(time.RFC3339Nano)} //nolint:gosec // validated above.
		setConditionObject(o, "Ready", false, "Quiescing", "administrator lifecycle operation is quiescing the executor")
		return unstructured.SetNestedMap(o.Object, operation, "status", "lifecycleOperation")
	})
}

func exactAdminSubject(o *unstructured.Unstructured, q adminLifecycleRequest) error {
	if textNested(o.Object, "spec", "ownerHash") != q.OwnerHash || textNested(o.Object, "spec", "clientHash") != hashText(q.Client) || textNested(o.Object, "spec", "revision") != q.Environment.Revision {
		return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
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
	if err := s.retryUpdateStatus(ctx, q.Environment.ID, func(o *unstructured.Unstructured) error {
		if err := exactAdminSubject(o, q); err != nil {
			return err
		}
		if textNested(o.Object, "status", "fenceState") != "FenceUnknown" {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment is not fenced"}
		}
		podName, pvcName = textNested(o.Object, "status", "pod", "name"), textNested(o.Object, "status", "pvc", "name")
		return nil
	}); err != nil {
		return err
	}
	pod, err := s.kube.CoreV1().Pods(s.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil || string(pod.UID) != q.ExpectedPodUID || !podTerminal(pod) {
		return &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "terminal executor proof unavailable; use the external-fencing operator runbook"}
	}
	pvc, err := s.kube.CoreV1().PersistentVolumeClaims(s.namespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil || string(pvc.UID) != q.ExpectedPVCUID {
		return &executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "workspace identity cannot be verified"}
	}
	return s.retryUpdateStatus(ctx, q.Environment.ID, func(o *unstructured.Unstructured) error {
		if err := exactAdminSubject(o, q); err != nil {
			return err
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
	return map[string]any{"operationID": q.OperationID, "podUID": q.ExpectedPodUID, "pvcUID": q.ExpectedPVCUID, "epoch": int64(q.ExpectedEpoch), "podPhase": string(pod.Status.Phase), "observedAt": time.Now().UTC().Format(time.RFC3339Nano)} //nolint:gosec // epoch is validated by caller.
}

func podTerminal(pod *corev1.Pod) bool {
	if pod == nil || pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed || len(pod.Status.ContainerStatuses) != len(pod.Spec.Containers) || len(pod.Spec.Containers) == 0 {
		return false
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Terminated == nil {
			return false
		}
	}
	return true
}

// DeleteRetiredEnvironment starts exact deletion of an explicitly retained workspace.
func (s *Store) DeleteRetiredEnvironment(ctx context.Context, q adminLifecycleRequest) error {
	if q.ExpectedPVCUID == "" || q.OperationID == "" {
		return &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "exact retained workspace identity is required"}
	}
	return s.retryUpdateStatus(ctx, q.Environment.ID, func(o *unstructured.Unstructured) error {
		if textNested(o.Object, "spec", "ownerHash") != q.OwnerHash || textNested(o.Object, "spec", "clientHash") != hashText(q.Client) || textNested(o.Object, "spec", "revision") != q.Environment.Revision {
			return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
		}
		if textNested(o.Object, "status", "pvc", "uid") != q.ExpectedPVCUID {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "retained workspace identity changed"}
		}
		if existing, found, _ := unstructured.NestedMap(o.Object, "status", "lifecycleOperation"); found {
			if text(existing, "id") == q.OperationID && text(existing, "type") == "DeleteRetiredEnvironment" {
				return nil
			}
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "another lifecycle operation is active"}
		}
		refs, err := referenceRecords(o)
		if err != nil || len(refs) != 0 || textNested(o.Object, "status", "activeRun", "claimID") != "" || textNested(o.Object, "status", "activeOperation", "id") != "" || !conditionTrue(o, "Retired") || !conditionTrue(o, "ExecutorTerminated") {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "environment is not safely retained and unreferenced"}
		}
		return unstructured.SetNestedMap(o.Object, map[string]any{"id": q.OperationID, "type": "DeleteRetiredEnvironment", "phase": "DeletingPVC", "expectedPVCUID": q.ExpectedPVCUID, "createdAt": s.now().Format(time.RFC3339Nano)}, "status", "lifecycleOperation")
	})
}

// MigrateEnvironment explicitly upgrades a recognized, verified prototype schema.
func (s *Store) MigrateEnvironment(ctx context.Context, q adminLifecycleRequest) error { //nolint:gocyclo // Two Kubernetes subresources require a durable phased migration.
	if s.kube == nil || q.ExpectedSchema < 0 || q.ExpectedSchema > 1 || q.OperationID == "" || q.ExpectedPodUID == "" || q.ExpectedPVCUID == "" {
		return &executionenv.Error{Code: executionenv.CodeInvalidArgument, Message: "recognized prototype schema and exact runtime identities are required"}
	}
	if err := s.retryUpdateStatusRaw(ctx, q.Environment.ID, func(o *unstructured.Unstructured) error {
		if textNested(o.Object, "spec", "ownerHash") != q.OwnerHash || textNested(o.Object, "spec", "clientHash") != hashText(q.Client) || textNested(o.Object, "spec", "revision") != q.Environment.Revision {
			return &executionenv.Error{Code: executionenv.CodeNotFound, Message: environmentNotFoundMessage}
		}
		if intNested(o.Object, "spec", "schemaVersion") == currentSchemaVersion && intNested(o.Object, "status", "schemaVersion") == currentSchemaVersion && textNested(o.Object, "status", "lastMigrationOperationID") == q.OperationID {
			return nil
		}
		if migrationID := textNested(o.Object, "status", "migrationOperation", "id"); migrationID != "" {
			if migrationID != q.OperationID || intNested(o.Object, "status", "migrationOperation", "fromSchema") != q.ExpectedSchema {
				return &executionenv.Error{Code: executionenv.CodeConflict, Message: "another schema migration is active"}
			}
			return nil
		}
		if intNested(o.Object, "spec", "schemaVersion") != q.ExpectedSchema || intNested(o.Object, "status", "schemaVersion") != q.ExpectedSchema || textNested(o.Object, "status", "activeRun", "claimID") != "" || textNested(o.Object, "status", "activeOperation", "id") != "" || textNested(o.Object, "status", "lifecycleOperation", "id") != "" || textNested(o.Object, "status", "fenceState") != fenceHealthy {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "prototype environment is not eligible for migration"}
		}
		if textNested(o.Object, "status", "pod", "uid") != q.ExpectedPodUID || textNested(o.Object, "status", "pvc", "uid") != q.ExpectedPVCUID {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "prototype runtime identity mismatch"}
		}
		if err := s.verifyRuntimeUIDs(ctx, o, q); err != nil {
			return err
		}
		refs, err := migrateReferenceRecords(o, q.OperationID, s.now())
		if err != nil {
			return err
		}
		if err := setReferenceRecords(o, refs); err != nil {
			return err
		}
		return unstructured.SetNestedMap(o.Object, map[string]any{"id": q.OperationID, "fromSchema": q.ExpectedSchema, "expectedPodUID": q.ExpectedPodUID, "expectedPVCUID": q.ExpectedPVCUID}, "status", "migrationOperation")
	}); err != nil {
		return err
	}
	for attempt := 0; attempt < 5; attempt++ {
		o, err := s.resources.Get(ctx, q.Environment.ID, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if intNested(o.Object, "spec", "schemaVersion") == currentSchemaVersion {
			break
		}
		if textNested(o.Object, "status", "migrationOperation", "id") != q.OperationID || intNested(o.Object, "spec", "schemaVersion") != q.ExpectedSchema {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "schema migration changed concurrently"}
		}
		_ = unstructured.SetNestedField(o.Object, currentSchemaVersion, "spec", "schemaVersion")
		if _, err = s.resources.Update(ctx, o, metav1.UpdateOptions{}); err == nil {
			break
		} else if !apierrors.IsConflict(err) {
			return err
		} else if attempt == 4 {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "schema migration conflict", Retryable: true}
		}
	}
	return s.retryUpdateStatusRaw(ctx, q.Environment.ID, func(o *unstructured.Unstructured) error {
		if intNested(o.Object, "spec", "schemaVersion") != currentSchemaVersion || textNested(o.Object, "status", "migrationOperation", "id") != q.OperationID || intNested(o.Object, "status", "schemaVersion") != q.ExpectedSchema {
			if intNested(o.Object, "status", "schemaVersion") == currentSchemaVersion && textNested(o.Object, "status", "lastMigrationOperationID") == q.OperationID {
				return nil
			}
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "schema migration phase mismatch"}
		}
		epoch, ok := epochValue(o)
		if !ok || epoch >= math.MaxInt64 {
			return &executionenv.Error{Code: executionenv.CodeConflict, Message: "prototype epoch is invalid"}
		}
		_ = unstructured.SetNestedField(o.Object, int64(epoch+1), "status", "epoch") //nolint:gosec // checked above.
		_ = unstructured.SetNestedField(o.Object, currentSchemaVersion, "status", "schemaVersion")
		_ = unstructured.SetNestedField(o.Object, q.OperationID, "status", "lastMigrationOperationID")
		unstructured.RemoveNestedField(o.Object, "status", "migrationOperation")
		return nil
	})
}

func (s *Store) verifyRuntimeUIDs(ctx context.Context, o *unstructured.Unstructured, q adminLifecycleRequest) error {
	pod, err := s.kube.CoreV1().Pods(s.namespace).Get(ctx, textNested(o.Object, "status", "pod", "name"), metav1.GetOptions{})
	if err != nil || string(pod.UID) != q.ExpectedPodUID {
		return &executionenv.Error{Code: executionenv.CodeConflict, Message: "prototype executor identity is not observable"}
	}
	pvc, err := s.kube.CoreV1().PersistentVolumeClaims(s.namespace).Get(ctx, textNested(o.Object, "status", "pvc", "name"), metav1.GetOptions{})
	if err != nil || string(pvc.UID) != q.ExpectedPVCUID {
		return &executionenv.Error{Code: executionenv.CodeConflict, Message: "prototype workspace identity is not observable"}
	}
	return nil
}

func migrateReferenceRecords(o *unstructured.Unstructured, operationID string, now time.Time) ([]referenceRecord, error) {
	raw, found, err := unstructured.NestedSlice(o.Object, "status", "references")
	if err != nil || !found {
		return nil, &executionenv.Error{Code: executionenv.CodeConflict, Message: "prototype references are malformed"}
	}
	out := make([]referenceRecord, 0, len(raw))
	for _, value := range raw {
		switch item := value.(type) {
		case string:
			if item == "" {
				return nil, &executionenv.Error{Code: executionenv.CodeConflict, Message: "prototype references are malformed"}
			}
			out = append(out, referenceRecord{BindingID: item, State: executionenv.ReferencePublished, OperationID: "migration-" + operationID, CreatedAt: now})
		case map[string]any:
			created, parseErr := time.Parse(time.RFC3339Nano, text(item, "createdAt"))
			r := referenceRecord{BindingID: text(item, "bindingID"), State: executionenv.ReferenceState(text(item, "state")), OperationID: text(item, "operationID"), SourceBindingID: text(item, "sourceBindingID"), CreatedAt: created}
			if parseErr != nil || r.BindingID == "" || r.OperationID == "" || r.State != executionenv.ReferencePendingCreate && r.State != executionenv.ReferencePublished && r.State != executionenv.ReferencePendingDelete {
				return nil, &executionenv.Error{Code: executionenv.CodeConflict, Message: "prototype references are malformed"}
			}
			out = append(out, r)
		default:
			return nil, &executionenv.Error{Code: executionenv.CodeConflict, Message: "prototype references are malformed"}
		}
	}
	return out, nil
}
