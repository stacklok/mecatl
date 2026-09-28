package chart_test

import (
	"bytes"
	"context"
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
)

// Exercise Helm's actual lookup/render path, with a loopback API containing only
// synthetic nonsecret objects. No ambient kubeconfig or cluster is consulted.
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
	retained[authority]["data"] = map[string]any{"state.json": `{"generation":7,"digest":"synthetic","fingerprints":{"k1:1":"synthetic"}}`}
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
			delete(m["ConfigMap/test-mecatl-execution-profiles"]["data"].(map[string]any), "lifetime.json")
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
		{name: "changed profile", args: []string{"--set", "profiles.go.maxEnvironments=30"}, want: "configuration is incompatible"},
		{name: "changed profile pulls", args: []string{"--set-json", `profiles.go.imagePullSecrets=["registry"]`}, want: "configuration is incompatible"},
		{name: "changed fullname", args: []string{"--set", "fullnameOverride=other"}, want: "ServiceAccount other-executor with token automount disabled"},
		{name: "missing profiles with orphan policies", mutate: func(m map[string]map[string]any) {
			delete(m, "ExecutionEnvironment/exec-test")
			delete(m, "ConfigMap/test-mecatl-execution-profiles")
			delete(m, capacity)
		}, want: "original release profiles"},
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
				args = []string{"--set-json", "provider.imagePullSecrets=" + tc.provider, "--set-json", "profiles.go.imagePullSecrets=" + tc.profile}
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
			profileCM := &unstructured.Unstructured{Object: objects["ConfigMap/test-mecatl-execution-profiles"]}
			content, _, _ := unstructured.NestedString(profileCM.Object, "data", "profiles.yaml")
			var decoded map[string]any
			if err := yaml.NewYAMLOrJSONDecoder(strings.NewReader(content), 4096).Decode(&decoded); err != nil {
				t.Fatal(err)
			}
			profile := decoded["profiles"].(map[string]any)["go"].(map[string]any)
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
		{"profiles.go.imagePullSecrets", `["ns/reg"]`}, {"profiles.go.imagePullSecrets", `["a","a"]`},
		{"profiles.go.imagePullSecrets", `["a","b","c","d","e","f","g","h","i"]`},
		{"profiles.go.imagePullSecret", `["reg"]`},
	} {
		if _, err := renderLifetime(t, nil, "--set-json", tc.key+"="+tc.value); err == nil {
			t.Fatalf("invalid chart value accepted: %s=%s", tc.key, tc.value)
		}
	}
	if _, err := renderLifetime(t, nil, "--set-json", `provider.imagePullSecrets=["a","b","c","d","e","f","g","h"]`, "--set-json", `profiles.go.imagePullSecrets=["a","b","c","d","e","f","g","h"]`); err != nil {
		t.Fatal(err)
	}
}

func renderLifetime(t *testing.T, objects map[string]map[string]any, extra ...string) (map[string]map[string]any, error) {
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
	// Repo-local scratch: synthetic kubeconfig has no credentials.
	if err := os.MkdirAll("../../../.scratch", 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("../../../.scratch", "chart-lifetime-")
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "kubeconfig")
	body := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: offline\n  cluster:\n    server: %s\ncontexts:\n- name: offline\n  context:\n    cluster: offline\n    namespace: ns\ncurrent-context: offline\n", server.URL)
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	args := []string{"template", "test", ".", "--dry-run=server", "--disable-openapi-validation", "--kubeconfig", config, "--kube-context", "offline", "--namespace", "ns", "-f", "../../mecatl-execution-kind/execution-values.yaml", "--set-string", "provider.image=example.invalid/provider@sha256:" + strings.Repeat("a", 64), "--set", "provider.securitySecretName=synthetic", "--set-literal", "provider.securityManifest={}", "--set-json", `networkPolicy.workloadProfiles={"go":{"egress":[{"cidr":"10.2.0.0/16","ports":[{"port":8080}]}]}}`}
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
