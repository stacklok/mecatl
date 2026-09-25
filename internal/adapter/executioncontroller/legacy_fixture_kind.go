//go:build kind_execution_e2e

package executioncontroller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/stacklok/mecatl/internal/executionenv"
)

// LegacyMigrationSeed identifies one pre-upgrade API-server fixture.
type LegacyMigrationSeed struct {
	Environment     executionenv.EnvironmentRef `json:"environment"`
	Owner           executionenv.Owner          `json:"owner"`
	Binding         string                      `json:"binding"`
	PodUID          string                      `json:"pod_uid"`
	PVCUID          string                      `json:"pvc_uid"`
	Malformed       executionenv.EnvironmentRef `json:"malformed_environment"`
	MalformedPodUID string                      `json:"malformed_pod_uid"`
	MalformedPVCUID string                      `json:"malformed_pvc_uid"`
	Insecure        executionenv.EnvironmentRef `json:"insecure_environment"`
	InsecurePodUID  string                      `json:"insecure_pod_uid"`
	InsecurePVCUID  string                      `json:"insecure_pvc_uid"`
}

// SeedLegacyMigrationFixture creates the narrow security-compatible prototype
// accepted by MigrateEnvironment plus malformed and insecure negative controls.
// It is excluded from regular builds.
func SeedLegacyMigrationFixture(ctx context.Context, d dynamic.Interface, kube kubernetes.Interface, namespace, profilesPath string) (LegacyMigrationSeed, error) {
	profiles, err := LoadProfiles(profilesPath)
	if err != nil {
		return LegacyMigrationSeed{}, err
	}
	profile, ok := profiles.get("go")
	if !ok {
		return LegacyMigrationSeed{}, fmt.Errorf("go profile unavailable")
	}
	owner := executionenv.Owner{Issuer: "https://oidc-issuer.execution-qualification.svc.cluster.local:8443", Subject: "production-migration"}
	client := "spiffe://mecatl.test/client/mecak8s"
	recognized, err := seedOneLegacyEnvironment(ctx, d, kube, namespace, profile, owner, client, "legacy-migration", "legacy-migration-binding", []any{"legacy-migration-binding"}, false)
	if err != nil {
		return LegacyMigrationSeed{}, err
	}
	malformed, err := seedOneLegacyEnvironment(ctx, d, kube, namespace, profile, owner, client, "legacy-migration-malformed", "legacy-malformed-binding", []any{""}, false)
	if err != nil {
		return LegacyMigrationSeed{}, err
	}
	insecure, err := seedOneLegacyEnvironment(ctx, d, kube, namespace, profile, owner, client, "legacy-migration-insecure", "legacy-insecure-binding", []any{"legacy-insecure-binding"}, true)
	if err != nil {
		return LegacyMigrationSeed{}, err
	}
	return LegacyMigrationSeed{Environment: recognized.ref, Owner: owner, Binding: "legacy-migration-binding", PodUID: recognized.podUID, PVCUID: recognized.pvcUID, Malformed: malformed.ref, MalformedPodUID: malformed.podUID, MalformedPVCUID: malformed.pvcUID, Insecure: insecure.ref, InsecurePodUID: insecure.podUID, InsecurePVCUID: insecure.pvcUID}, nil
}

// Kubernetes defaults resource-quota usage resync to five minutes. Discovery of
// a new CRD need not enqueue a quota that has not accounted for that resource.
const legacyQuotaWait = 6 * time.Minute

type seededLegacyEnvironment struct {
	ref            executionenv.EnvironmentRef
	podUID, pvcUID string
}

func seedOneLegacyEnvironment(ctx context.Context, d dynamic.Interface, kube kubernetes.Interface, namespace string, profile resolvedProfile, owner executionenv.Owner, client, name, binding string, references []any, insecure bool) (seededLegacyEnvironment, error) {
	// Deployment readiness does not imply that quota admission has initialized
	// accounting, especially for the freshly installed custom resource.
	started := time.Now()
	var missing []string
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, legacyQuotaWait, true, func(ctx context.Context) (bool, error) {
		quota, err := kube.CoreV1().ResourceQuotas(namespace).Get(ctx, "mecatl-execution", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		missing = nil
		ready := true
		for name, configured := range quota.Spec.Hard {
			hard, hasHard := quota.Status.Hard[name]
			_, hasUsed := quota.Status.Used[name]
			if !hasHard || hard.Cmp(configured) != 0 || !hasUsed {
				ready = false
				switch name {
				case "pods", "persistentvolumeclaims", "count/executionenvironments.execution.mecatl.dev", "requests.cpu", "requests.memory", "requests.storage", "requests.ephemeral-storage", "limits.cpu", "limits.memory", "limits.ephemeral-storage":
					missing = append(missing, string(name))
				}
			}
		}
		return ready, nil
	}); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			err = context.DeadlineExceeded
		} else if errors.Is(err, context.Canceled) {
			err = context.Canceled
		} else if apierrors.IsForbidden(err) {
			err = &apierrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonForbidden, Code: 403, Message: "forbidden"}}
		} else {
			err = errors.New("api_error")
		}
		// The fixture CLI prints this diagnostic; never wrap raw API response data.
		slices.Sort(missing)
		return seededLegacyEnvironment{}, fmt.Errorf("wait for legacy fixture quota accounting: missing_or_mismatched_keys=%v elapsed=%s: %w", missing, time.Since(started).Round(time.Millisecond), err)
	}
	revision, err := randomID()
	if err != nil {
		return seededLegacyEnvironment{}, err
	}
	spec := map[string]any{
		"schemaVersion": int64(1), "allocationID": name, "revision": revision,
		"ownerHash": ownerHash(owner), "ownerIssuer": owner.Issuer, "ownerSubject": owner.Subject,
		"clientHash": hashText(client), "bindingID": binding, "requestFingerprint": hashText("legacy-fixture-" + name),
		"profile": "go", "profileDigest": profile.Digest, "image": profile.Spec.Image,
		"storageClass": profile.Spec.StorageClass, "storageSize": profile.Spec.StorageSize,
		"resources": map[string]any{"cpuRequest": profile.Spec.CPURequest, "memoryRequest": profile.Spec.MemoryRequest, "cpuLimit": profile.Spec.CPULimit, "memoryLimit": profile.Spec.MemoryLimit},
		"desired":   "Active",
	}
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment",
		"metadata": map[string]any{"name": name, "namespace": namespace, "finalizers": []any{environmentFinalizer}},
		"spec":     spec,
	}}
	resources := d.Resource(ExecutionEnvironmentGVR).Namespace(namespace)
	created, err := resources.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		created, err = resources.Create(ctx, object, metav1.CreateOptions{})
	} else if err == nil {
		revision = textNested(created.Object, "spec", "revision")
		podUID := textNested(created.Object, "status", "pod", "uid")
		pvcUID := textNested(created.Object, "status", "pvc", "uid")
		if revision != "" && podUID != "" && pvcUID != "" {
			return seededLegacyEnvironment{ref: executionenv.EnvironmentRef{ID: name, Revision: revision}, podUID: podUID, pvcUID: pvcUID}, nil
		}
	}
	if err != nil {
		return seededLegacyEnvironment{}, fmt.Errorf("create legacy environment %s: %w", name, err)
	}
	r := NewReconciler(d, kube, namespace, &Profiles{byName: map[string]resolvedProfile{"go": profile}})
	pvcName, podName := resourceName("workspace", name), resourceName("executor", name)
	pvc, err := r.ensurePVC(ctx, created, profile, pvcName)
	if err != nil {
		return seededLegacyEnvironment{}, fmt.Errorf("create legacy PVC %s: %w", name, err)
	}
	pod, err := r.ensurePod(ctx, created, profile, podName, pvcName)
	if err != nil {
		return seededLegacyEnvironment{}, fmt.Errorf("create legacy Pod %s: %w", name, err)
	}
	if insecure {
		pod, err = replaceWithInsecureLegacyPod(ctx, kube, namespace, pod)
		if err != nil {
			return seededLegacyEnvironment{}, fmt.Errorf("create insecure legacy Pod %s: %w", name, err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	created.Object["status"] = map[string]any{
		"schemaVersion": int64(1), "observedGeneration": created.GetGeneration(), "epoch": int64(1), "grantGeneration": int64(1), "fenceState": fenceHealthy,
		"references": references, "pvc": map[string]any{"name": pvc.Name, "uid": string(pvc.UID)}, "pod": map[string]any{"name": pod.Name, "uid": string(pod.UID)},
		"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "LegacyFixtureReady", "message": "security-compatible prototype runtime created before CRD upgrade", "observedGeneration": created.GetGeneration(), "lastTransitionTime": now}},
	}
	if _, err := resources.UpdateStatus(ctx, created, metav1.UpdateOptions{}); err != nil {
		return seededLegacyEnvironment{}, fmt.Errorf("persist legacy status %s: %w", name, err)
	}
	return seededLegacyEnvironment{ref: executionenv.EnvironmentRef{ID: name, Revision: revision}, podUID: string(pod.UID), pvcUID: string(pvc.UID)}, nil
}

func replaceWithInsecureLegacyPod(ctx context.Context, kube kubernetes.Interface, namespace string, pod *corev1.Pod) (*corev1.Pod, error) {
	pods := kube.CoreV1().Pods(namespace)
	if _, err := pods.Patch(ctx, pod.Name, types.MergePatchType, []byte(`{"metadata":{"finalizers":[]}}`), metav1.PatchOptions{}); err != nil {
		return nil, err
	}
	zero := int64(0)
	if err := pods.Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero}); err != nil {
		return nil, err
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		if _, err := pods.Get(ctx, pod.Name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			break
		} else if err != nil {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := pods.Get(ctx, pod.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("secure fixture Pod deletion did not complete: %w", err)
	}
	candidate := pod.DeepCopy()
	candidate.ObjectMeta = metav1.ObjectMeta{Name: pod.Name, Namespace: namespace, Labels: pod.Labels, Finalizers: pod.Finalizers, OwnerReferences: pod.OwnerReferences}
	candidate.Status = corev1.PodStatus{}
	automount := true
	candidate.Spec.AutomountServiceAccountToken = &automount
	return pods.Create(ctx, candidate, metav1.CreateOptions{})
}
