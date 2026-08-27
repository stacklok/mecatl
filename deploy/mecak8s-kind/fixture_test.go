package mecak8s_kind

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const fixtureTask = "Taskfile.yml"

// TestMecak8sKindFixture_Scenario1_ToolHiveFreeSetup pins the standalone
// fixture boundary: its base lifecycle contains only Kind, the local chart,
// and local Redis, not the optional identity or vMCP stacks.
func TestMecak8sKindFixture_Scenario1_ToolHiveFreeSetup(t *testing.T) {
	text := fixtureTaskClosure(t, "kind-setup")
	for _, want := range []string{
		"reset-state:", "cluster-create:", "namespace-apply:", "image-build-load:", "chart-apply:",
		"--values=deploy/helm/mecak8s/values-kind.yaml", "statefulset/redis",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Kind base setup missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"cert-manager", "certificate-apply", "keycloak", "toolhive", "yardstick", "vmcp",
		"values-kind-vmcp.yaml", "oci://", "curl ", "api.github.com",
	} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("Kind base setup transitively depends on forbidden %q", forbidden)
		}
	}
}

// TestMecak8sKindFixture_Scenario1_DedicatedKubeconfig pins the fixture's
// explicit context boundary, including loopback-only host access and cleanup.
func TestMecak8sKindFixture_Scenario1_DedicatedKubeconfig(t *testing.T) {
	body, err := os.ReadFile(fixtureTask)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body) + "\n" + fixtureTaskClosure(t, "kind-setup", "kind-status", "kind-port-forward", "kind-destroy")
	for _, want := range []string{
		"KUBECONFIG: deploy/mecak8s-kind/kconfig.yaml", "CONTEXT: kind-mecatl-dev",
		"--kubeconfig={{.KUBECONFIG}}", "--context={{.CONTEXT}}", "--kube-context={{.CONTEXT}}",
		"--address=127.0.0.1", "svc/{{.RELEASE}}-mecak8s", "18080:8080", "18081:8081",
		"kind delete cluster --name={{.CLUSTER}}", "rm -rf {{.STATE}}", "rm -f {{.KUBECONFIG}} {{.SETUP_LOCK}}",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("dedicated fixture lifecycle missing %q", want)
		}
	}
}

// TestMecak8sKindFixture_Scenario1_DocumentationBoundaries pins that the
// local operator fixture is neither the production chart nor e2e/k8s, and
// makes no production isolation claim.
func TestMecak8sKindFixture_Scenario1_DocumentationBoundaries(t *testing.T) {
	body, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"operator-run", "deploy/helm/mecak8s/", "e2e/k8s/", "no general NetworkPolicy",
		"127.0.0.1", "port-forward",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("fixture documentation missing boundary %q", want)
		}
	}
	for _, forbidden := range []string{"production network isolation", "ToolHive", "Keycloak", "vMCP"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("ToolHive-free fixture documentation contains %q", forbidden)
		}
	}
}

var fixtureTaskBlockRe = regexp.MustCompile(`(?m)^  ([a-zA-Z][a-zA-Z0-9_-]*):\s*$`)

func fixtureTaskClosure(t *testing.T, roots ...string) string {
	t.Helper()
	body, err := os.ReadFile(fixtureTask)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	tasksIdx := strings.Index(text, "\ntasks:\n")
	if tasksIdx < 0 {
		t.Fatal("Taskfile has no tasks section")
	}
	taskBody := text[tasksIdx:]
	headers := fixtureTaskBlockRe.FindAllStringSubmatchIndex(taskBody, -1)
	blocks := make(map[string]string, len(headers))
	for i, header := range headers {
		end := len(taskBody)
		if i+1 < len(headers) {
			end = headers[i+1][0]
		}
		blocks[taskBody[header[2]:header[3]]] = taskBody[header[0]:end]
	}

	refs := regexp.MustCompile(`task:\s*([a-zA-Z][a-zA-Z0-9_-]*)`)
	visited := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		block, ok := blocks[name]
		if !ok {
			t.Fatalf("Taskfile references unknown task %q", name)
		}
		visited[name] = true
		for _, match := range refs.FindAllStringSubmatch(block, -1) {
			visit(match[1])
		}
	}
	for _, root := range roots {
		visit(root)
	}

	var out strings.Builder
	for _, root := range roots {
		out.WriteString(blocks[root])
	}
	for name, block := range blocks {
		if visited[name] && !slices.Contains(roots, name) {
			out.WriteString(block)
		}
	}
	return out.String()
}
