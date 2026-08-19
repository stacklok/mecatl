package deploycheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func between(t *testing.T, body, start, end string) string {
	t.Helper()
	from := strings.Index(body, start)
	if from < 0 {
		t.Fatalf("missing %q", start)
	}
	body = body[from:]
	to := strings.Index(body, end)
	if to < 0 {
		t.Fatalf("missing %q after %q", end, start)
	}
	return body[:to]
}

func TestMecak8sTaskfileWiring(t *testing.T) {
	root := readRepoFile(t, "Taskfile.yml")
	body := readRepoFile(t, "deploy/mecak8s-vmcp/Taskfile.yml")
	var doc struct {
		Tasks map[string]struct {
			Internal bool `yaml:"internal"`
		} `yaml:"tasks"`
	}
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("parse Taskfile: %v", err)
	}
	if !strings.Contains(root, "mecak8s:\n    taskfile: deploy/mecak8s-vmcp/Taskfile.yml\n    dir: .") {
		t.Fatal("root Taskfile does not include the mecak8s Taskfile unflattened")
	}

	wantPublic := map[string]bool{"kind-setup": true, "kind-status": true, "kind-destroy": true}
	for name, task := range doc.Tasks {
		if !task.Internal && !wantPublic[name] {
			t.Errorf("unexpected public mecak8s task %q", name)
		}
		delete(wantPublic, name)
	}
	for name := range wantPublic {
		t.Errorf("missing public mecak8s task %q", name)
	}

	setup := between(t, body, "  kind-setup:\n", "  kind-status:\n")
	for _, step := range []string{"task: reset-state", "task: cluster-create", "task: image-build-load", "task: chart-apply", "task: dex-apply"} {
		if !strings.Contains(setup, step) {
			t.Errorf("setup missing %q", step)
		}
	}
	if strings.Index(setup, "task: cluster-create") > strings.Index(setup, "task: image-build-load") ||
		strings.Index(setup, "task: image-build-load") > strings.Index(setup, "task: chart-apply") ||
		strings.Index(setup, "task: chart-apply") > strings.Index(setup, "task: dex-apply") {
		t.Error("setup must recreate the cluster, load the local image, apply the chart, then deploy Dex")
	}
	for _, forbidden := range []string{"ownership-guard", "mecatl-vmcp-owner", "OWNER_FILE", "SETUP_CREATED", "release-resolve", "live-llm", "toolhive-install", "receipts-record", "github.com", "VirtualMCPServer"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("Kind profile contains forbidden %q", forbidden)
		}
	}

	reset := between(t, body, "  reset-state:\n", "  cluster-ready:\n")
	if !strings.Contains(reset, "kind delete cluster --name={{.CLUSTER}}") {
		t.Error("setup must reset the named Kind cluster")
	}
	destroy := between(t, body, "  kind-destroy:\n", "  reset-state:\n")
	if !strings.Contains(destroy, "kind delete cluster --name={{.CLUSTER}}") {
		t.Error("kind-destroy must delete the named Kind cluster")
	}

	for _, forbidden := range []string{"kind-toolhive", "kubectl config use-context", "kind load docker-image", "mktemp"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("Taskfile contains forbidden operation %q", forbidden)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, "kubectl ") || strings.Contains(line, "command -v kubectl") || strings.Contains(line, "kubectl is required") {
			continue
		}
		if strings.Contains(line, "kubectl --kubeconfig={{.KUBECONFIG}} config current-context") {
			continue
		}
		if !strings.Contains(line, "--kubeconfig={{.KUBECONFIG}} --context={{.CONTEXT}}") {
			t.Errorf("kubectl invocation is not bound to the dedicated kubeconfig/context: %q", line)
		}
	}
	chartApply := body[strings.Index(body, "  chart-apply:\n"):]
	for _, required := range []string{
		"deploy/helm/mecak8s/values-kind.yaml",
		"pod-security.kubernetes.io/enforce=restricted",
		"rollout status statefulset/redis",
		"rollout status deployment/{{.RELEASE}}-mecak8s",
		"--kubeconfig={{.KUBECONFIG}} --kube-context={{.CONTEXT}}",
	} {
		if !strings.Contains(chartApply, required) {
			t.Errorf("chart application missing %q", required)
		}
	}

	dexApply := body[strings.Index(body, "  dex-apply:\n"):]
	for _, required := range []string{"deploy/mecak8s-vmcp/dex.yaml", "rollout status deployment/dex", "--namespace={{.NAMESPACE}}"} {
		if !strings.Contains(dexApply, required) {
			t.Errorf("Dex application missing %q", required)
		}
	}
}

func TestMecak8sDexFixtureHasDistinctDisposableUsers(t *testing.T) {
	body := readRepoFile(t, "deploy/mecak8s-vmcp/dex.yaml")
	for _, required := range []string{
		"storage:\n      type: memory",
		"issuer: http://dex.mecatl-vmcp.svc.cluster.local:5556",
		"email: alice@example.com",
		"email: bob@example.com",
		"app.kubernetes.io/name: dex",
		"readOnlyRootFilesystem: true",
		"capabilities:\n              drop: [\"ALL\"]",
		"name: dex-allow-mecak8s",
		"name: mecak8s-allow-dex",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("Dex fixture missing %q", required)
		}
	}
	for _, forbidden := range []string{"VirtualMCPServer", "toolhive.stacklok.dev"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("Dex fixture must not introduce vMCP integration through %q", forbidden)
		}
	}
}

func TestVMCPFixtureDoesNotImplyVMCPIntegration(t *testing.T) {
	readme := readRepoFile(t, "deploy/mecak8s-vmcp/README.md")
	body := readRepoFile(t, "deploy/mecak8s-vmcp/Taskfile.yml")

	for _, required := range []string{"ToolHive-free", "does **not** install ToolHive"} {
		if !strings.Contains(readme, required) {
			t.Errorf("README must state Phase-A boundary %q", required)
		}
	}
	for _, forbidden := range []string{"port-forward.sh", "connect-user.sh", "VirtualMCPServer", "toolhive"} {
		if strings.Contains(body, forbidden) || strings.Contains(readme, forbidden) {
			t.Errorf("Phase-A fixture must not imply vMCP integration through %q", forbidden)
		}
	}
	for _, removed := range []string{"port-forward.sh", "connect-user.sh", "kustomization.yaml", "agent-oidc-args.yaml"} {
		if _, err := os.Stat(filepath.Join("..", "..", "deploy", "mecak8s-vmcp", removed)); !os.IsNotExist(err) {
			t.Errorf("obsolete vMCP fixture artifact %q still exists or could not be checked: %v", removed, err)
		}
	}
}
