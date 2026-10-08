package chart_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"

	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
)

const goFixtureRevision = "v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102"

// Exercise Helm's actual lookup/render path, with a loopback API containing only
// synthetic nonsecret objects. No ambient kubeconfig or cluster is consulted.
// New revisions may be published without losing immutable historical recipes.
func TestChartRejectsLegacyProfileValues(t *testing.T) {
	if _, err := renderLifetime(t, nil, "--set", "profiles.go.image=legacy"); err == nil || !strings.Contains(err.Error(), "additional properties 'profiles' not allowed") {
		t.Fatalf("legacy profile-shaped chart values were accepted: %v", err)
	}
}

func TestChartTemplateRevisionLifetime(t *testing.T) {
	spec := executioncontroller.ProfileSpec{Image: "example.invalid/workload@sha256:" + strings.Repeat("a", 64), StorageClass: "standard", StorageSize: "1Gi", CPURequest: "50m", MemoryRequest: "64Mi", CPULimit: "1", MemoryLimit: "512Mi", EphemeralStorageRequest: "64Mi", EphemeralStorageLimit: "1Gi", TmpSizeLimit: "256Mi", RuntimeClassName: "runc", MaxFileBytes: 1024, MaxCommandBytes: 1024, MaxCommandDuration: 5 * time.Minute, MaxEnvironments: 20}
	revisionOf := func(s executioncontroller.ProfileSpec) string {
		t.Helper()
		body, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(append([]byte("mecatl/execution-template/v1\x00"), body...))
		return "v1-" + hex.EncodeToString(sum[:])
	}
	execution := func(s executioncontroller.ProfileSpec) map[string]any {
		return map[string]any{"image": s.Image, "storageClass": s.StorageClass, "storageSize": s.StorageSize, "cpuRequest": s.CPURequest, "memoryRequest": s.MemoryRequest, "cpuLimit": s.CPULimit, "memoryLimit": s.MemoryLimit, "ephemeralStorageRequest": s.EphemeralStorageRequest, "ephemeralStorageLimit": s.EphemeralStorageLimit, "tmpSizeLimit": s.TmpSizeLimit, "runtimeClassName": s.RuntimeClassName, "maxFileBytes": s.MaxFileBytes, "maxCommandBytes": s.MaxCommandBytes, "maxCommandDuration": "5m", "maxEnvironments": s.MaxEnvironments}
	}
	old := revisionOf(spec)
	oldDefinition := map[string]any{"execution": execution(spec)}
	values := func(revisions map[string]any, def string) string {
		t.Helper()
		content, err := json.Marshal(map[string]any{"provider": map[string]any{"clientIngressSelectors": []any{map[string]any{"namespaceLabels": map[string]string{"kubernetes.io/metadata.name": "ns"}, "podLabels": map[string]string{"app.kubernetes.io/name": "mecak8s"}}}, "apiServerCIDRs": []string{"172.16.0.0/12"}, "dnsCIDRs": []string{"10.96.0.10/32"}}, "templates": map[string]any{"go": map[string]any{"default": def, "revisions": revisions}}})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "templates-values.json")
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	initial := values(map[string]any{old: oldDefinition}, old)
	fresh, err := renderLifetimeWithFile(t, nil, initial)
	if err != nil {
		t.Fatal(err)
	}
	retained := map[string]map[string]any{}
	for _, obj := range fresh {
		u := &unstructured.Unstructured{Object: obj}
		if u.GetAnnotations()["helm.sh/resource-policy"] == "keep" {
			retained[u.GetKind()+"/"+u.GetName()] = obj
		}
	}
	retained["ConfigMap/test-mecatl-execution-security-authority"]["data"] = map[string]any{"state.json": `{"generation":1}`}
	retained["ConfigMap/mecatl-execution-profile-allocations"]["data"] = map[string]any{"profile-go": `["alloc"]`}
	spec.MaxEnvironments = 10
	newRevision := revisionOf(spec)
	additive := values(map[string]any{old: oldDefinition, newRevision: map[string]any{"execution": execution(spec)}}, newRevision)
	if _, err := renderLifetimeWithFile(t, retained, additive); err != nil {
		t.Fatalf("additive revision rejected: %v", err)
	}
	for _, tc := range []struct{ name, file string }{
		{"dropped revision", values(map[string]any{newRevision: map[string]any{"execution": execution(spec)}}, newRevision)},
		{"modified historical execution", values(map[string]any{old: map[string]any{"execution": execution(spec)}, newRevision: map[string]any{"execution": execution(spec)}}, newRevision)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := renderLifetimeWithFile(t, retained, tc.file); err == nil || !strings.Contains(err.Error(), "retained execution template definition") {
				t.Fatalf("invalid revision history was accepted: %v", err)
			}
		})
	}
	oldDefinition["display"] = map[string]any{"name": "Renamed"}
	oldDefinition["deprecated"] = true
	policy := values(map[string]any{old: oldDefinition, newRevision: map[string]any{"execution": execution(spec)}}, newRevision)
	if _, err := renderLifetimeWithFile(t, retained, policy); err != nil {
		t.Fatalf("display/policy update rejected: %v", err)
	}
	// The schema bounds namespaces, and the Helm guard bounds flattened keys.
	metadata := map[string]any{"execution": execution(spec), "display": map[string]any{"extensions": map[string]any{"example.com/catalog": map[string]any{"schema_version": "1", strings.Repeat("x", 240): "opaque"}}}}
	if _, err := renderLifetimeWithFile(t, retained, values(map[string]any{old: oldDefinition, newRevision: metadata}, newRevision)); err == nil || !strings.Contains(err.Error(), "metadata key exceeds 253") {
		t.Fatalf("oversized flattened metadata key accepted: %v", err)
	}
	retainedOnly := map[string]any{"provider": map[string]any{"clientIngressSelectors": []any{map[string]any{"namespaceLabels": map[string]string{"kubernetes.io/metadata.name": "ns"}, "podLabels": map[string]string{"app.kubernetes.io/name": "mecak8s"}}}, "apiServerCIDRs": []string{"172.16.0.0/12"}, "dnsCIDRs": []string{"10.96.0.10/32"}}, "templates": map[string]any{"go": map[string]any{"default": newRevision, "revisions": map[string]any{old: oldDefinition, newRevision: map[string]any{"execution": execution(spec)}}}, "retained": map[string]any{"revisions": map[string]any{old: map[string]any{"execution": execution(spec), "revoked": true}}}}}
	retainedFile := filepath.Join(t.TempDir(), "retained.json")
	body, err := json.Marshal(retainedOnly)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(retainedFile, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := renderLifetimeWithFile(t, retained, retainedFile); err != nil {
		t.Fatalf("retained-only ID rejected: %v", err)
	}
	retainedOnly["templates"].(map[string]any)["retained"].(map[string]any)["revisions"].(map[string]any)[old].(map[string]any)["revoked"] = false
	body, err = json.Marshal(retainedOnly)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(retainedFile, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := renderLifetimeWithFile(t, retained, retainedFile); err == nil || !strings.Contains(err.Error(), "selectable revisions but no default") {
		t.Fatalf("unselected eligible revision accepted: %v", err)
	}
	retained["ConfigMap/test-mecatl-execution-security-authority"]["data"] = map[string]any{"state.json": `{"generation":1}`}
	retained["ConfigMap/mecatl-execution-profile-allocations"]["data"] = map[string]any{"profile-go": `["alloc"]`}
	addPolicy := []string{"--set-json", `networkPolicy.workloadProfiles.python={"egress":[{"cidr":"10.10.0.0/16","ports":[{"port":443}]}]}`}
	base, err := os.ReadFile(initial)
	if err != nil {
		t.Fatal(err)
	}
	var withNewID map[string]any
	if err := json.Unmarshal(base, &withNewID); err != nil {
		t.Fatal(err)
	}
	// The new ID uses the original immutable recipe, not the modified go revision.
	withNewID["templates"].(map[string]any)["python"] = map[string]any{"default": old, "revisions": map[string]any{old: map[string]any{"execution": oldDefinition["execution"]}}}
	additiveValues := filepath.Join(t.TempDir(), "new-id.json")
	encodedNewID, err := json.Marshal(withNewID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(additiveValues, encodedNewID, 0o600); err != nil {
		t.Fatal(err)
	}
	pythonHash := sha256.Sum256([]byte("python"))
	policyName := "NetworkPolicy/test-mecatl-execution-profile-" + hex.EncodeToString(pythonHash[:8])
	if rendered, err := renderLifetimeWithFile(t, retained, additiveValues, addPolicy...); err != nil || rendered[policyName] == nil {
		t.Fatalf("new ID policy not rendered: %v", err)
	}
	retained["Pod/foreign-template-label"] = map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "foreign-template-label", "namespace": "ns", "labels": map[string]any{"execution.mecatl.dev/template-id": hex.EncodeToString(pythonHash[:8])}}}
	if _, err := renderLifetimeWithFile(t, retained, additiveValues, addPolicy...); err == nil || !strings.Contains(err.Error(), "matches a retained workload Pod") {
		t.Fatalf("new policy matched retained pod: %v", err)
	}
}

func TestChartRejectsOversizedAggregateInventory(t *testing.T) {
	templates := map[string]any{}
	for group, id := range []string{"go", "python", "ruby"} {
		revisions := map[string]any{}
		count := 32
		if group == 2 {
			count = 1
		}
		for i := range count {
			revisions[fmt.Sprintf("v1-%064x", group*32+i)] = map[string]any{"execution": map[string]any{
				"image": "example.invalid/workload@sha256:" + strings.Repeat("a", 64), "storageClass": "standard", "storageSize": "1Gi",
				"cpuRequest": "50m", "memoryRequest": "64Mi", "cpuLimit": "1", "memoryLimit": "512Mi",
				"ephemeralStorageRequest": "64Mi", "ephemeralStorageLimit": "1Gi", "tmpSizeLimit": "256Mi", "runtimeClassName": "runc",
				"maxFileBytes": 1024, "maxCommandBytes": 1024, "maxCommandDuration": "5m", "maxEnvironments": 20,
			}}
		}
		templates[id] = map[string]any{"default": fmt.Sprintf("v1-%064x", group*32), "revisions": revisions}
	}
	data, err := json.Marshal(map[string]any{"provider": map[string]any{"clientIngressSelectors": []any{map[string]any{"namespaceLabels": map[string]string{"kubernetes.io/metadata.name": "ns"}, "podLabels": map[string]string{"app.kubernetes.io/name": "mecak8s"}}}, "apiServerCIDRs": []string{"172.16.0.0/12"}, "dnsCIDRs": []string{"10.96.0.10/32"}}, "templates": templates})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "values.json")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := renderLifetimeWithFile(t, nil, file); err == nil || !strings.Contains(err.Error(), "inventory exceeds 64 revisions") {
		t.Fatalf("oversized inventory accepted before provider startup: %v", err)
	}
}

func TestChartRetainedLifetime(t *testing.T) {
	fresh, err := renderLifetime(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	retained := map[string]map[string]any{}
	for _, obj := range fresh {
		u := &unstructured.Unstructured{Object: obj}
		if u.GetAnnotations()["helm.sh/resource-policy"] == "keep" {
			retained[u.GetKind()+"/"+u.GetName()] = obj
		}
	}
	if len(retained) != 7 {
		t.Fatalf("retained resources=%d, want four ConfigMaps, two workload policies, and executor ServiceAccount", len(retained))
	}
	authority := "ConfigMap/test-mecatl-execution-security-authority"
	capacity := "ConfigMap/mecatl-execution-profile-allocations"
	retained[authority]["data"] = map[string]any{"state.json": `{"generation":7,"digest":"synthetic"}`}
	retained[capacity]["data"] = map[string]any{"profile-go": `["allocation-uid"]`}
	retained["ExecutionEnvironment/exec-test"] = map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": "exec-test", "namespace": "ns"}}
	for _, upgrade := range []bool{false, true} {
		t.Run(fmt.Sprintf("reuse-upgrade-%t", upgrade), func(t *testing.T) {
			var args []string
			if upgrade {
				args = append(args, "--is-upgrade")
			}
			rendered, err := renderLifetime(t, retained, args...)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{authority, capacity} {
				if !reflect.DeepEqual(rendered[key]["data"], retained[key]["data"]) {
					t.Fatalf("%s data was reset", key)
				}
			}
			for key, obj := range retained {
				if strings.HasPrefix(key, "NetworkPolicy/") && !reflect.DeepEqual(rendered[key]["spec"], obj["spec"]) {
					t.Fatalf("%s changed confinement", key)
				}
			}
		})
	}
	tests := []struct {
		name   string
		mutate func(map[string]map[string]any)
		args   []string
		want   string
	}{
		{name: "active provider deployment", mutate: func(m map[string]map[string]any) {
			m["Deployment/test-mecatl-execution"] = map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "test-mecatl-execution", "namespace": "ns"}, "spec": map[string]any{"replicas": 2}}
		}, want: "quiesce the execution provider"},
		{name: "provider pod still terminating", mutate: func(m map[string]map[string]any) {
			m["Pod/provider"] = map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "provider", "namespace": "ns", "deletionTimestamp": "2026-09-21T00:00:00Z", "labels": map[string]any{"app.kubernetes.io/name": "test-mecatl-execution"}}}
		}, want: "wait for all execution provider Pods"},
		{name: "reserved capacity without CR", mutate: func(m map[string]map[string]any) {
			delete(m, "ExecutionEnvironment/exec-test")
			delete(m, authority)
		}, want: "bootstrap is forbidden"},
		{name: "surviving pod without CR", mutate: func(m map[string]map[string]any) {
			delete(m, "ExecutionEnvironment/exec-test")
			delete(m, authority)
			delete(m, capacity)
			m["Pod/executor"] = map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "executor", "namespace": "ns", "labels": map[string]any{"execution.mecatl.dev/environment": "exec-test"}}}
		}, want: "bootstrap is forbidden"},
		{name: "surviving PVC without CR", mutate: func(m map[string]map[string]any) {
			delete(m, "ExecutionEnvironment/exec-test")
			delete(m, authority)
			delete(m, capacity)
			m["PersistentVolumeClaim/workspace"] = map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": "workspace", "namespace": "ns", "labels": map[string]any{"execution.mecatl.dev/environment": "exec-test"}}}
		}, want: "bootstrap is forbidden"},
		{name: "missing lifetime configuration", mutate: func(m map[string]map[string]any) {
			delete(m["ConfigMap/test-mecatl-execution-templates"]["data"].(map[string]any), "lifetime.json")
		}, want: "missing lifetime.json history"},
		{name: "missing authority", mutate: func(m map[string]map[string]any) { delete(m, authority) }, want: "bootstrap is forbidden"},
		{name: "empty authority", mutate: func(m map[string]map[string]any) { m[authority]["data"] = map[string]any{} }, want: "bootstrap is forbidden"},
		{name: "missing state", mutate: func(m map[string]map[string]any) { m[authority]["data"] = map[string]any{"other": "value"} }, want: "state.json"},
		{name: "missing capacity", mutate: func(m map[string]map[string]any) { delete(m, capacity) }, want: "bootstrap is forbidden"},
		{name: "missing executor ServiceAccount", mutate: func(m map[string]map[string]any) {
			delete(m, "ServiceAccount/test-mecatl-execution-executor")
		}, want: "ServiceAccount test-mecatl-execution-executor with token automount disabled"},
		{name: "executor ServiceAccount token enabled", mutate: func(m map[string]map[string]any) {
			m["ServiceAccount/test-mecatl-execution-executor"]["automountServiceAccountToken"] = true
		}, want: "ServiceAccount test-mecatl-execution-executor with token automount disabled"},
		{name: "executor ServiceAccount inherited pulls", mutate: func(m map[string]map[string]any) {
			m["ServiceAccount/test-mecatl-execution-executor"]["imagePullSecrets"] = []any{map[string]any{"name": "injected"}}
		}, want: "ServiceAccount test-mecatl-execution-executor with token automount disabled"},
		{name: "missing deny", mutate: func(m map[string]map[string]any) {
			delete(m, "NetworkPolicy/test-mecatl-execution-workload-default-deny")
		}, want: "existing workload NetworkPolicies"},
		{name: "foreign release", mutate: func(m map[string]map[string]any) {
			u := &unstructured.Unstructured{Object: m[capacity]}
			a := u.GetAnnotations()
			a["meta.helm.sh/release-name"] = "foreign"
			u.SetAnnotations(a)
		}, want: "foreign or ambiguous"},
		{name: "foreign namespace", mutate: func(m map[string]map[string]any) {
			u := &unstructured.Unstructured{Object: m[authority]}
			a := u.GetAnnotations()
			a["meta.helm.sh/release-namespace"] = "foreign"
			u.SetAnnotations(a)
		}, want: "foreign or ambiguous"},
		{name: "unmanaged", mutate: func(m map[string]map[string]any) {
			u := &unstructured.Unstructured{Object: m[authority]}
			u.SetLabels(nil)
		}, want: "foreign or ambiguous"},
		{name: "removed profile policy", args: []string{"--set-json", "networkPolicy.workloadProfiles={}"}, want: "configuration is incompatible"},
		{name: "changed egress", args: []string{"--set", "networkPolicy.workloadProfiles.go.egress[0].cidr=10.3.0.0/16"}, want: "configuration is incompatible"},
		{name: "changed profile", args: []string{"--set", "templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.maxEnvironments=30"}, want: "retained execution template definition"},
		{name: "changed profile scheduling", args: []string{"--set", "templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.nodeSelector.pool=workers"}, want: "retained execution template definition"},
		{name: "changed profile tolerations", args: []string{"--set", "templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.tolerations[0].key=dedicated", "--set", "templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.tolerations[0].operator=Exists"}, want: "retained execution template definition"},
		{name: "changed profile pulls", args: []string{"--set-json", `templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.imagePullSecrets=["registry"]`}, want: "retained execution template definition"},
		{name: "changed fullname", args: []string{"--set", "fullnameOverride=other"}, want: "ServiceAccount other-executor with token automount disabled"},
		{name: "missing profiles with orphan policies", mutate: func(m map[string]map[string]any) {
			delete(m, "ExecutionEnvironment/exec-test")
			delete(m, "ConfigMap/test-mecatl-execution-templates")
			delete(m, capacity)
		}, want: "original release templates"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := make(map[string]map[string]any, len(retained))
			for key, obj := range retained {
				objects[key] = (&unstructured.Unstructured{Object: obj}).DeepCopy().Object
			}
			if tt.mutate != nil {
				tt.mutate(objects)
			}
			_, err := renderLifetime(t, objects, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("render error=%v, want %q", err, tt.want)
			}
		})
	}
	if _, err := renderLifetime(t, retained, "--set-json", `provider.imagePullSecrets=["registry"]`); err != nil {
		t.Fatalf("provider-only pull Secret rotation must not change retained workload identity: %v", err)
	}
}

func TestChartPullIdentity(t *testing.T) {
	for _, tc := range []struct{ name, provider, profile string }{
		{name: "omitted"},
		{name: "configured", provider: `["registry-one","registry.two"]`, profile: `["workload-one","workload.two"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var args []string
			if tc.provider != "" {
				args = []string{"--set-json", "provider.imagePullSecrets=" + tc.provider, "--set-json", "templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.imagePullSecrets=" + tc.profile}
			}
			objects, err := renderLifetime(t, nil, args...)
			if err != nil {
				t.Fatal(err)
			}
			account := objects["ServiceAccount/test-mecatl-execution-executor"]
			if account == nil || account["automountServiceAccountToken"] != false || account["imagePullSecrets"] != nil {
				t.Fatalf("executor account must be tokenless without pull Secrets: %v", account)
			}
			for _, object := range objects {
				if object["kind"] != "RoleBinding" && object["kind"] != "ClusterRoleBinding" {
					continue
				}
				binding := &unstructured.Unstructured{Object: object}
				subjects, _, _ := unstructured.NestedSlice(binding.Object, "subjects")
				for _, subject := range subjects {
					if subject.(map[string]any)["name"] == "test-mecatl-execution-executor" {
						t.Fatalf("executor got RBAC: %v", binding.Object)
					}
				}
			}
			deployment := &unstructured.Unstructured{Object: objects["Deployment/test-mecatl-execution"]}
			providerAccount, _, _ := unstructured.NestedString(deployment.Object, "spec", "template", "spec", "serviceAccountName")
			if providerAccount != "test-mecatl-execution" {
				t.Fatalf("provider account: %s", providerAccount)
			}
			providerPull, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "imagePullSecrets")
			want := 0
			if tc.provider != "" {
				want = 2
			}
			if len(providerPull) != want {
				t.Fatalf("provider pull: %v", providerPull)
			}
			if want == 2 && (providerPull[0].(map[string]any)["name"] != "registry-one" || providerPull[1].(map[string]any)["name"] != "registry.two") {
				t.Fatalf("provider pull order: %v", providerPull)
			}
			profileCM := &unstructured.Unstructured{Object: objects["ConfigMap/test-mecatl-execution-templates"]}
			content, _, _ := unstructured.NestedString(profileCM.Object, "data", "templates.yaml")
			var decoded map[string]any
			if err := yaml.NewYAMLOrJSONDecoder(strings.NewReader(content), 4096).Decode(&decoded); err != nil {
				t.Fatal(err)
			}
			profile := decoded["templates"].(map[string]any)["go"].(map[string]any)["revisions"].(map[string]any)[goFixtureRevision].(map[string]any)["execution"].(map[string]any)
			if want == 0 && profile["imagePullSecrets"] != nil || want == 2 && !reflect.DeepEqual(profile["imagePullSecrets"], []any{"workload-one", "workload.two"}) {
				t.Fatalf("profile pull: %v", profile)
			}
		})
	}
	for _, release := range []string{"other", "test"} {
		objects, err := renderLifetime(t, nil, "--set", "fullnameOverride="+release+"-provider")
		if err != nil {
			t.Fatal(err)
		}
		deployment := &unstructured.Unstructured{Object: objects["Deployment/"+release+"-provider"]}
		containers, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
		args := containers[0].(map[string]any)["args"].([]any)
		if objects["ServiceAccount/"+release+"-provider-executor"] == nil || !strings.Contains(fmt.Sprint(args), "--executor-service-account="+release+"-provider-executor") {
			t.Fatalf("release account not wired: %v", args)
		}
	}
	longPrefix := strings.Repeat("a", 53)
	first, err := renderLifetime(t, nil, "--set", "fullnameOverride="+longPrefix+"-first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderLifetime(t, nil, "--set", "fullnameOverride="+longPrefix+"-second")
	if err != nil {
		t.Fatal(err)
	}
	for name := range first {
		if strings.HasPrefix(name, "ServiceAccount/") && strings.HasSuffix(name, "-executor") && second[name] != nil {
			t.Fatalf("long release names share executor account %s", name)
		}
	}
}

func TestChartRejectsInvalidPullSecrets(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"provider.imagePullSecrets", `[""]`}, {"provider.imagePullSecrets", `["reg","reg"]`},
		{"provider.imagePullSecrets", `["REG"]`}, {"provider.imagePullSecrets", `["` + strings.Repeat("a", 64) + `"]`},
		{"provider.imagePullSecrets", `["a","b","c","d","e","f","g","h","i"]`},
		{"templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.imagePullSecrets", `["ns/reg"]`}, {"templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.imagePullSecrets", `["a","a"]`},
		{"templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.imagePullSecrets", `["a","b","c","d","e","f","g","h","i"]`},
		{"templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.imagePullSecret", `["reg"]`},
	} {
		if _, err := renderLifetime(t, nil, "--set-json", tc.key+"="+tc.value); err == nil {
			t.Fatalf("invalid chart value accepted: %s=%s", tc.key, tc.value)
		}
	}
	if _, err := renderLifetime(t, nil, "--set-json", `provider.imagePullSecrets=["a","b","c","d","e","f","g","h"]`, "--set-json", `templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.imagePullSecrets=["a","b","c","d","e","f","g","h"]`); err != nil {
		t.Fatal(err)
	}
}

func TestChartSchedulingSchema(t *testing.T) {
	objects, err := renderLifetime(t, nil,
		"--set-json", `templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.nodeSelector={"node.kubernetes.io/instance-type":"worker"}`,
		"--set-json", `templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.tolerations=[{"key":"dedicated","operator":"Equal","value":"build","effect":"NoSchedule"}]`)
	if err != nil {
		t.Fatal(err)
	}
	profileCM := &unstructured.Unstructured{Object: objects["ConfigMap/test-mecatl-execution-templates"]}
	content, _, _ := unstructured.NestedString(profileCM.Object, "data", "templates.yaml")
	var decoded map[string]any
	if err := yaml.NewYAMLOrJSONDecoder(strings.NewReader(content), 4096).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	profile := decoded["templates"].(map[string]any)["go"].(map[string]any)["revisions"].(map[string]any)[goFixtureRevision].(map[string]any)["execution"].(map[string]any)
	if !reflect.DeepEqual(profile["nodeSelector"], map[string]any{"node.kubernetes.io/instance-type": "worker"}) || !reflect.DeepEqual(profile["tolerations"], []any{map[string]any{"key": "dedicated", "operator": "Equal", "value": "build", "effect": "NoSchedule"}}) {
		t.Fatalf("rendered profile scheduling: %v", profile)
	}
	for _, tc := range []struct{ key, value string }{
		{"templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.nodeSelector", `{}`},
		{"templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.tolerations", `[]`},
		{"templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.tolerations", `[{"key":"dedicated"}]`},
		{"templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.tolerations", `[{"key":"dedicated","operator":"Exists"},{"key":"dedicated","operator":"Exists"}]`},
		{"templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.nodeSelector", `{"bad/key/again":"worker"}`},
		{"templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.tolerations", `[{"operator":"Exists","value":"build"}]`},
		{"templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.tolerations", `[{"key":"dedicated","operator":"Exists","effect":"NoSchedule","tolerationSeconds":1}]`},
		{"templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.tolerations", `[{"key":"dedicated","operator":"Exists","effect":"NoExecute","tolerationSeconds":86401}]`},
		{"templates.go.revisions.v1-5ba40f3d97f3bab366ee704db599b033d26d725e88f796ef956b74cb929a6102.execution.tolerations", `[{"key":"dedicated","operator":"Exists","unknown":true}]`},
	} {
		if _, err := renderLifetime(t, nil, "--set-json", tc.key+"="+tc.value); err == nil {
			t.Fatalf("invalid scheduling accepted: %s=%s", tc.key, tc.value)
		}
	}
}

func TestChartSecuritySources(t *testing.T) {
	const sources = `[{"name":"server-tls","items":[{"key":"tls.crt","path":"tls.crt"},{"key":"tls.key","path":"tls.key"}]},{"name":"client-roots","items":[{"key":"ca.crt","path":"clients.pem"}]}]`
	const manifest = `{"version":1,"generation":1,"tls":{"certificateFile":"tls.crt","privateKeyFile":"tls.key","clientCAFile":"clients.pem"},"clients":[{"uri":"spiffe://mecatl.test/client/mecak8s","mayAttestOwner":true}]}`
	render := func(src, m string, extra ...string) (map[string]map[string]any, error) {
		args := []string{"--set", "provider.securitySecretName=", "--set-json", "provider.securitySources=" + src, "--set-literal", "provider.securityManifest=" + m}
		return renderLifetime(t, nil, append(args, extra...)...)
	}
	objects, err := render(sources, manifest)
	if err != nil {
		t.Fatal(err)
	}
	deployment := &unstructured.Unstructured{Object: objects["Deployment/test-mecatl-execution"]}
	volumes, _, err := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "volumes")
	if err != nil {
		t.Fatal(err)
	}
	var projected []any
	for _, v := range volumes {
		if volume := v.(map[string]any); volume["name"] == "security" {
			projected = volume["projected"].(map[string]any)["sources"].([]any)
		}
	}
	if len(projected) != 3 {
		t.Fatalf("manifest and two TLS Secrets: %v", projected)
	}
	for i, name := range []string{"server-tls", "client-roots"} {
		secret := projected[i+1].(map[string]any)["secret"].(map[string]any)
		if secret["name"] != name {
			t.Fatalf("TLS projection: %v", secret)
		}
	}
	if strings.Contains(objects["ConfigMap/test-mecatl-execution-templates"]["data"].(map[string]any)["lifetime.json"].(string), "securitySecretName") {
		t.Fatal("TLS Secret name frozen into retained allocations")
	}
	invalid := []struct{ name, src, manifest string }{
		{"legacy signing references", sources, strings.Replace(manifest, `"clients":`, `"keys":[{"file":"signing.pem"}],"clients":`, 1)},
		{"duplicate destination", strings.Replace(sources, `"path":"tls.key"`, `"path":"tls.crt"`, 1), manifest},
		{"reserved manifest", strings.Replace(sources, `"path":"tls.key"`, `"path":"manifest.json"`, 1), manifest},
		{"unmapped certificate", sources, strings.Replace(manifest, `"tls.crt"`, `"missing.crt"`, 1)},
		{"unmapped CA", sources, strings.Replace(manifest, `"clients.pem"`, `"missing.pem"`, 1)},
		{"empty clients", sources, strings.Replace(manifest, `[{"uri":"spiffe://mecatl.test/client/mecak8s","mayAttestOwner":true}]`, `null`, 1)},
		{"absolute path", strings.Replace(sources, `"path":"tls.key"`, `"path":"/tls.key"`, 1), manifest},
		{"duplicate source", strings.Replace(sources, `"client-roots"`, `"server-tls"`, 1), manifest},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := render(tc.src, tc.manifest); err == nil {
				t.Fatal("invalid TLS projection accepted")
			}
		})
	}
	retained := map[string]map[string]any{}
	for _, obj := range objects {
		u := &unstructured.Unstructured{Object: obj}
		if u.GetAnnotations()["helm.sh/resource-policy"] == "keep" {
			retained[u.GetKind()+"/"+u.GetName()] = obj
		}
	}
	retained["ConfigMap/test-mecatl-execution-security-authority"]["data"] = map[string]any{"state.json": `{"generation":1,"digest":"synthetic"}`}
	retained["ConfigMap/mecatl-execution-profile-allocations"]["data"] = map[string]any{"profile-go": `["allocation-uid"]`}
	retained["ExecutionEnvironment/exec-test"] = map[string]any{"apiVersion": "execution.mecatl.dev/v1alpha1", "kind": "ExecutionEnvironment", "metadata": map[string]any{"name": "exec-test", "namespace": "ns"}}
	rotated := strings.Replace(manifest, `"generation":1`, `"generation":2`, 1)
	rotated = strings.Replace(rotated, `"tls.crt"`, `"next.crt"`, 1)
	staged := strings.Replace(sources, `{"key":"tls.crt","path":"tls.crt"}`, `{"key":"tls.crt","path":"tls.crt"},{"key":"next.crt","path":"next.crt"}`, 1)
	if _, err := renderLifetime(t, retained, "--set", "provider.securitySecretName=", "--set-json", "provider.securitySources="+staged, "--set-literal", "provider.securityManifest="+rotated); err != nil {
		t.Fatalf("TLS renewal should not alter retained allocations: %v", err)
	}
}

func renderLifetime(t *testing.T, objects map[string]map[string]any, extra ...string) (map[string]map[string]any, error) {
	t.Helper()
	values := filepath.Join(t.TempDir(), "templates.yaml")
	cmd := exec.Command("go", "run", "-tags", "kind_execution_e2e", "../../mecatl-execution-kind/fixture/templates", "../../mecatl-execution-kind/template-recipes.yaml", "example.invalid/workload@sha256:"+strings.Repeat("a", 64), "example.invalid/derivative@sha256:"+strings.Repeat("b", 64), values)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("render canonical fixture templates: %v: %s", err, out)
	}
	return renderLifetimeWithFile(t, objects, "../../mecatl-execution-kind/execution-values.yaml", append([]string{"-f", values}, extra...)...)
}

func renderLifetimeWithFile(t *testing.T, objects map[string]map[string]any, values string, extra ...string) (map[string]map[string]any, error) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required for real chart rendering")
	}
	resources := map[string][]map[string]any{}
	for gv, kinds := range map[string][]string{
		"v1":                            {"ConfigMap", "Pod", "PersistentVolumeClaim", "ResourceQuota", "LimitRange", "ServiceAccount", "Service"},
		"execution.mecatl.dev/v1alpha1": {"ExecutionEnvironment"},
		"networking.k8s.io/v1":          {"NetworkPolicy"},
		"apps/v1":                       {"Deployment"},
		"policy/v1":                     {"PodDisruptionBudget"},
		"rbac.authorization.k8s.io/v1":  {"Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding"},
	} {
		for _, kind := range kinds {
			plural := strings.ToLower(kind) + "s"
			if kind == "NetworkPolicy" {
				plural = "networkpolicies"
			}
			resources[gv] = append(resources[gv], map[string]any{"name": plural, "kind": kind, "namespaced": !strings.HasPrefix(kind, "Cluster")})
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var response any
		switch r.URL.Path {
		case "/version":
			response = map[string]any{"major": "1", "minor": "35", "gitVersion": "v1.35.0"}
		case "/api":
			response = map[string]any{"kind": "APIVersions", "apiVersion": "v1", "versions": []string{"v1"}}
		case "/apis":
			groups := []any{}
			for _, group := range []string{"execution.mecatl.dev", "networking.k8s.io", "apps", "policy", "rbac.authorization.k8s.io"} {
				version := "v1"
				if group == "execution.mecatl.dev" {
					version = "v1alpha1"
				}
				gv := map[string]any{"groupVersion": group + "/" + version, "version": version}
				groups = append(groups, map[string]any{"name": group, "versions": []any{gv}, "preferredVersion": gv})
			}
			response = map[string]any{"kind": "APIGroupList", "apiVersion": "v1", "groups": groups}
		default:
			for gv, rs := range resources {
				prefix := "/apis/" + gv
				if gv == "v1" {
					prefix = "/api/v1"
				}
				if r.URL.Path == prefix {
					response = map[string]any{"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": gv, "resources": rs}
					break
				}
				for _, resource := range rs {
					base := prefix + "/namespaces/ns/" + resource["name"].(string)
					if r.URL.Path == base {
						items := []any{}
						for _, obj := range objects {
							if obj["kind"] == resource["kind"] {
								items = append(items, obj)
							}
						}
						response = map[string]any{"kind": resource["kind"].(string) + "List", "apiVersion": gv, "items": items}
					} else if strings.HasPrefix(r.URL.Path, base+"/") {
						if obj := objects[resource["kind"].(string)+"/"+strings.TrimPrefix(r.URL.Path, base+"/")]; obj != nil {
							response = obj
						}
					}
				}
			}
		}
		if response == nil {
			w.WriteHeader(http.StatusNotFound)
			response = map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": "NotFound", "code": 404}
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	config := filepath.Join(dir, "kubeconfig")
	body := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: offline\n  cluster:\n    server: %s\ncontexts:\n- name: offline\n  context:\n    cluster: offline\n    namespace: ns\ncurrent-context: offline\n", server.URL)
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	args := []string{"template", "test", ".", "--dry-run=server", "--disable-openapi-validation", "--kubeconfig", config, "--kube-context", "offline", "--namespace", "ns", "-f", values, "--set-string", "provider.image=example.invalid/provider@sha256:" + strings.Repeat("a", 64), "--set", "provider.securitySecretName=synthetic", "--set-literal", `provider.securityManifest={"version":1,"generation":1,"tls":{"certificateFile":"tls.crt","privateKeyFile":"tls.key","clientCAFile":"clients.pem"},"clients":[{"uri":"spiffe://mecatl.test/client/test","mayAttestOwner":true}]}`, "--set-json", `networkPolicy.workloadProfiles={"go":{"egress":[{"cidr":"10.2.0.0/16","ports":[{"port":8080}]}]}}`}
	cmd := exec.CommandContext(ctx, "helm", append(args, extra...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "KUBECONFIG=" + config}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("helm rendering: %w: %s", err, stderr.String())
	}
	result := map[string]map[string]any{}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
	for {
		var obj map[string]any
		err := decoder.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(obj) == 0 {
			continue
		}
		u := &unstructured.Unstructured{Object: obj}
		result[u.GetKind()+"/"+u.GetName()] = obj
	}
	return result, nil
}
