package executioncontroller

import (
	"os"
	"testing"

	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsvalidation "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

func loadExecutionEnvironmentCRD(t *testing.T) (*apiextensionsv1.CustomResourceDefinition, *apiextensions.CustomResourceDefinition) {
	t.Helper()
	raw, err := os.ReadFile("../../../deploy/helm/mecatl-execution/crds/executionenvironment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatal(err)
	}
	apiextensionsv1.SetDefaults_CustomResourceDefinition(&crd)
	var internal apiextensions.CustomResourceDefinition
	if err := apiextensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(&crd, &internal, nil); err != nil {
		t.Fatal(err)
	}
	return &crd, &internal
}

func TestExecutionEnvironmentCRDIsAcceptedByKubernetesValidation(t *testing.T) {
	_, internal := loadExecutionEnvironmentCRD(t)
	if errs := apiextensionsvalidation.ValidateCustomResourceDefinition(t.Context(), internal); len(errs) != 0 {
		t.Fatalf("CRD is not installable: %v", errs.ToAggregate())
	}
}

func TestExecutionEnvironmentCRDPreservesControllerStatus(t *testing.T) {
	crd, _ := loadExecutionEnvironmentCRD(t)
	var internal apiextensions.JSONSchemaProps
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(crd.Spec.Versions[0].Schema.OpenAPIV3Schema, &internal, nil); err != nil {
		t.Fatal(err)
	}
	structural, err := schema.NewStructural(&internal)
	if err != nil {
		t.Fatal(err)
	}
	obj := map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": "env"}, "spec": map[string]any{}, "status": map[string]any{
		"schemaVersion": int64(2), "observedGeneration": int64(9), "epoch": int64(3), "grantGeneration": int64(4), "fenceState": "Healthy",
		"references": []any{map[string]any{"bindingID": "binding", "state": "Published", "operationID": "op", "sourceBindingID": "source", "createdAt": "2026-09-17T00:00:00Z"}},
		"pvc":        map[string]any{"name": "pvc", "uid": "pvc-uid"}, "pod": map[string]any{"name": "pod", "uid": "pod-uid"},
		"activeRun":          map[string]any{"bindingID": "binding", "runID": "run", "claimID": "claim", "operationID": "acquire", "ownerHash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "clientHash": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "epoch": int64(3), "grantGeneration": int64(4), "expiresAt": "2026-09-17T00:01:00Z"},
		"renewReceipts":      []any{map[string]any{"operationID": "renew", "fingerprint": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "expiresAt": "2026-09-17T00:01:00Z"}},
		"activeOperation":    map[string]any{"id": "file", "operation": "file.read", "startedAt": "2026-09-17T00:00:00Z", "claimID": "claim", "runID": "run", "epoch": int64(3), "holderID": "holder", "renewedAt": "2026-09-17T00:00:01Z", "expiresAt": "2026-09-17T00:01:00Z"},
		"lifecycleOperation": map[string]any{"id": "life", "type": "ReplaceExecutor", "phase": "Quiescing", "expectedEpoch": int64(3), "expectedPodUID": "pod-uid", "expectedPVCUID": "pvc-uid", "createdAt": "2026-09-17T00:00:00Z"},
		"terminationProof":   map[string]any{"operationID": "life", "podUID": "pod-uid", "pvcUID": "pvc-uid", "epoch": int64(3), "podPhase": "Failed", "observedAt": "2026-09-17T00:00:00Z"},
		"migrationOperation": map[string]any{"id": "migration", "fromSchema": int64(1), "expectedPodUID": "pod-uid", "expectedPVCUID": "pvc-uid"}, "lastMigrationOperationID": "migration",
		"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "Reconciled", "message": "ready", "observedGeneration": int64(9), "lastTransitionTime": "2026-09-17T00:00:00Z"}},
	}}
	pruning.Prune(obj, structural, true)
	for _, path := range [][]string{{"status", "schemaVersion"}, {"status", "observedGeneration"}, {"status", "epoch"}, {"status", "grantGeneration"}, {"status", "references"}, {"status", "pvc"}, {"status", "pod"}, {"status", "activeRun", "grantGeneration"}, {"status", "renewReceipts"}, {"status", "activeOperation"}, {"status", "lifecycleOperation"}, {"status", "terminationProof"}, {"status", "migrationOperation"}, {"status", "lastMigrationOperationID"}, {"status", "conditions"}} {
		if _, found, err := unstructured.NestedFieldNoCopy(obj, path...); err != nil || !found {
			t.Fatalf("CRD pruning removed controller field %v (found=%t err=%v)", path, found, err)
		}
	}
}
