package mecabroker_test

import (
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/yaml"
)

func renderChart(t *testing.T, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Fatalf("helm is required for chart render tests: %v", err)
	}
	cmd := exec.Command("helm", args...)
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	return string(out)
}

func networkPolicyFromRender(t *testing.T, rendered string) networkingv1.NetworkPolicy {
	t.Helper()
	for _, document := range strings.Split(rendered, "\n---") {
		var meta struct {
			Kind string `yaml:"kind"`
		}
		if err := yaml.Unmarshal([]byte(document), &meta); err != nil || meta.Kind != "NetworkPolicy" {
			continue
		}
		var policy networkingv1.NetworkPolicy
		if err := yaml.Unmarshal([]byte(document), &policy); err != nil {
			t.Fatal(err)
		}
		return policy
	}
	t.Fatal("rendered chart has no NetworkPolicy")
	return networkingv1.NetworkPolicy{}
}

func TestSingletonBrokerRemediation_Scenario4_NetworkPolicyValuesRenderExactly(t *testing.T) {
	rendered := renderChart(t, "template", "production", ".", "-f", "ci/production-values.yaml", "--set-json", `networkPolicy.publicFrom=[{"ipBlock":{"cidr":"192.0.2.0/24"}}]`)
	policy := networkPolicyFromRender(t, rendered)
	if len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) != 1 || policy.Spec.Ingress[0].Ports[0].Port.String() != "public" {
		t.Fatalf("multiplexed public ingress = %#v", policy.Spec.Ingress)
	}
	if _, err := exec.Command("helm", "template", "production", ".", "-f", "ci/production-values.yaml", "--set-json", `networkPolicy.mecak8sFrom=[{"ipBlock":{"cidr":"192.0.2.0/24"}}]`).CombinedOutput(); err == nil {
		t.Fatal("ineffective route-specific network policy key was accepted")
	}
}

func TestNetworkPolicyIngressDefaultsToDenyAndUsesPublicContainerPort(t *testing.T) {
	t.Run("empty peers default deny", func(t *testing.T) {
		rendered := renderChart(t, "template", "production", ".", "-f", "ci/production-values.yaml", "--set-json", "networkPolicy.publicFrom=[]")
		policy := networkPolicyFromRender(t, rendered)
		if len(policy.Spec.Ingress) != 0 {
			t.Fatalf("empty peer lists rendered %d ingress rules", len(policy.Spec.Ingress))
		}
	})
	t.Run("configured peers target the multiplexed public port", func(t *testing.T) {
		rendered := renderChart(t, "template", "production", ".", "-f", "ci/production-values.yaml", "--set-json", `networkPolicy.publicFrom=[{"ipBlock":{"cidr":"192.0.2.0/24"}}]`)
		policy := networkPolicyFromRender(t, rendered)
		if len(policy.Spec.Ingress) != 1 || policy.Spec.Ingress[0].Ports[0].Port.String() != "public" {
			t.Fatalf("unexpected ingress: %#v", policy.Spec.Ingress)
		}
	})
}

func TestSingletonBrokerRemediation_Scenario4_DigestRequired(t *testing.T) {
	rendered := renderChart(t, "template", "production", ".", "-f", "ci/production-values.yaml")
	var images []string
	for _, document := range strings.Split(rendered, "\n---") {
		var deployment appsv1.Deployment
		if yaml.Unmarshal([]byte(document), &deployment) != nil || deployment.Kind != "Deployment" {
			continue
		}
		for _, container := range append(deployment.Spec.Template.Spec.Containers, deployment.Spec.Template.Spec.InitContainers...) {
			images = append(images, container.Image)
		}
	}
	if len(images) == 0 {
		t.Fatal("production render contains no workload images")
	}
	canonical := regexp.MustCompile(`^[^@:[:space:]]+(?:/[^@:[:space:]]+)*@sha256:[0-9a-f]{64}$`)
	for _, image := range images {
		if !canonical.MatchString(image) {
			t.Fatalf("production image is not repository@canonical-digest: %q", image)
		}
	}
	for _, set := range []string{"image.digest=", "image.digest=sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "image.digest=sha256:deadbeef", "image.tag=v1"} {
		if _, err := exec.Command("helm", "template", "production", ".", "-f", "ci/production-values.yaml", "--set", set).CombinedOutput(); err == nil {
			t.Fatalf("accepted invalid or ambiguous image override %q", set)
		}
	}
}

func TestSingletonBrokerRemediation_Scenario4_SingletonTopologyAndExposure(t *testing.T) {
	rendered := renderChart(t, "template", "production", ".", "-f", "ci/production-values.yaml")
	var deployment appsv1.Deployment
	var deployments, services, highAvailability int
	for _, document := range strings.Split(rendered, "\n---") {
		var meta struct {
			Kind string `yaml:"kind"`
		}
		if yaml.Unmarshal([]byte(document), &meta) != nil {
			continue
		}
		switch meta.Kind {
		case "Deployment":
			if err := yaml.Unmarshal([]byte(document), &deployment); err != nil {
				t.Fatal(err)
			}
			if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 || deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
				t.Fatalf("deployment topology = %#v", deployment.Spec)
			}
			deployments++
		case "Service":
			var service struct {
				Spec struct {
					Ports []struct {
						Name string `yaml:"name"`
					} `yaml:"ports"`
				} `yaml:"spec"`
			}
			if err := yaml.Unmarshal([]byte(document), &service); err != nil {
				t.Fatal(err)
			}
			for _, port := range service.Spec.Ports {
				if port.Name == "admin" {
					t.Fatal("admin listener is publicly exposed")
				}
			}
			services++
		case "PodDisruptionBudget", "HorizontalPodAutoscaler":
			highAvailability++
		}
	}
	if deployments != 1 || services != 1 || highAvailability != 0 {
		t.Fatalf("topology objects deployment=%d service=%d HA=%d", deployments, services, highAvailability)
	}
	pod := deployment.Spec.Template.Spec
	if len(pod.Containers) != 1 {
		t.Fatalf("broker containers = %d, want 1", len(pod.Containers))
	}
	container := pod.Containers[0]
	if !strings.Contains(strings.Join(container.Args, "\n"), "--admin-addr=127.0.0.1:") {
		t.Fatalf("administration listener is not loopback-only: %q", container.Args)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot || pod.SecurityContext.SeccompProfile == nil {
		t.Fatalf("restrictive pod security = token:%v context:%#v", pod.AutomountServiceAccountToken, pod.SecurityContext)
	}
	security := container.SecurityContext
	if security == nil || security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem || security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation || len(security.Capabilities.Drop) == 0 {
		t.Fatalf("restrictive container security = %#v", security)
	}
	policy := networkPolicyFromRender(t, rendered)
	if len(policy.Spec.PolicyTypes) != 2 || len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) == 0 || len(policy.Spec.Egress) == 0 {
		t.Fatalf("default-deny policy with explicit public ingress and operator egress = %#v", policy.Spec)
	}
	for _, policyType := range []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress} {
		if !slices.Contains(policy.Spec.PolicyTypes, policyType) {
			t.Fatalf("network policy omits %s: %v", policyType, policy.Spec.PolicyTypes)
		}
	}
}

func TestSingletonBrokerRemediation_Scenario4_DeploymentGateIsExecutable(t *testing.T) {
	if _, err := exec.LookPath("task"); err != nil {
		t.Fatalf("task is required for deployment gate: %v", err)
	}
	if _, err := exec.LookPath("helm"); err != nil {
		t.Fatalf("helm is required for deployment gate: %v", err)
	}
	if _, err := exec.LookPath("kubeconform"); err != nil {
		t.Fatalf("kubeconform is required for deployment gate: %v", err)
	}
	cmd := exec.Command("task", "deploy:check")
	cmd.Dir = "../../.."
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("task deploy:check: %v\n%s", err, output)
	}

	workflowBody, err := os.ReadFile("../../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Needs any              `yaml:"needs"`
			Steps []map[string]any `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(workflowBody, &workflow); err != nil {
		t.Fatalf("parse CI workflow: %v", err)
	}
	deployment, ok := workflow.Jobs["deployment"]
	if !ok {
		t.Fatal("CI has no required deployment job")
	}
	if deployment.Needs == nil {
		t.Fatal("deployment job is not connected to CI prerequisites")
	}
	runs := map[string]bool{}
	for _, step := range deployment.Steps {
		if run, ok := step["run"].(string); ok {
			runs[strings.TrimSpace(run)] = true
		}
	}
	for _, required := range []string{
		"task deploy:check",
		"go test ./deploy/helm/mecak8s -count=1",
		"go test ./deploy/helm/mecabroker -count=1",
	} {
		if !runs[required] {
			t.Fatalf("deployment job omits required semantic invocation %q", required)
		}
	}
}
func TestInvariant_singleton_broker_release_supply_chain_hardening(t *testing.T) {
	body, err := os.ReadFile("../../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []map[string]any `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatalf("parse release workflow: %v", err)
	}
	approvedSetupKo := "ko-build/setup-ko@61b4d1d396f5b2e7d6bb6fefdce3dc38d1a13445"
	koSteps := 0
	loginSteps := 0
	for jobName, job := range workflow.Jobs {
		for _, step := range job.Steps {
			uses, _ := step["uses"].(string)
			if strings.HasPrefix(uses, "ko-build/setup-ko@") {
				koSteps++
				if uses != approvedSetupKo {
					t.Fatalf("job %s selects an unapproved setup-ko action: %q", jobName, uses)
				}
				with, ok := step["with"].(map[string]any)
				if !ok || with["version"] != "v0.18.1" {
					t.Fatalf("job %s has an unpinned ko tool version: %#v", jobName, step)
				}
			}
			run, _ := step["run"].(string)
			if strings.Contains(run, "secrets.GITHUB_TOKEN") {
				t.Fatalf("job %s interpolates registry credentials in shell: %s", jobName, run)
			}
			if strings.Contains(run, "--password-stdin") {
				loginSteps++
				if !strings.Contains(run, `printf '%s' "$GITHUB_TOKEN"`) {
					t.Fatalf("job %s does not quote the step-scoped credential into password-stdin: %s", jobName, run)
				}
				env, ok := step["env"].(map[string]any)
				if !ok || env["GITHUB_TOKEN"] != "${{ secrets.GITHUB_TOKEN }}" {
					t.Fatalf("job %s login lacks the exact step-scoped credential env: %#v", jobName, step)
				}
			}
		}
	}
	if koSteps != 4 || loginSteps != 6 {
		t.Fatalf("release hardening coverage changed: setup-ko=%d registry-logins=%d", koSteps, loginSteps)
	}
}

func TestReleaseWorkflow_BrokerChartUsesPublishedDigestAndTagSerialization(t *testing.T) {
	body, err := os.ReadFile("../../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, required := range []string{
		"group: release-${{ inputs.tag || github.ref_name }}",
		"image_ref: ${{ steps.build.outputs.digest }}",
		"image_digest: ${{ steps.build.outputs.image_digest }}",
		"IMAGE_REF: ${{ needs.publish-mecabroker.outputs.image_ref }}",
		"IMAGE_DIGEST: ${{ needs.publish-mecabroker.outputs.image_digest }}",
		"--app-version \"${VERSION}\"",
		"digest: ${IMAGE_DIGEST}",
		"cosign sign --yes \"${IMAGE}\"",
		"subject-digest: ${{ steps.build.outputs.image_digest }}",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("release workflow omits %q", required)
		}
	}
	if strings.Contains(text, "group: release-${{ inputs.tag || github.ref }}") {
		t.Fatal("tag push and manual release do not serialize on the same tag")
	}
}

func TestMecabrokerChart_DeploymentSecurityAndShutdownBudget(t *testing.T) {
	rendered := renderChart(t, "template", "production", ".", "-f", "ci/production-values.yaml")
	var deployment appsv1.Deployment
	for _, document := range strings.Split(rendered, "\n---") {
		var meta struct {
			Kind string `yaml:"kind"`
		}
		if yaml.Unmarshal([]byte(document), &meta) == nil && meta.Kind == "Deployment" {
			if err := yaml.Unmarshal([]byte(document), &deployment); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if deployment.Name == "" {
		t.Fatal("rendered chart has no Deployment")
	}
	pod := deployment.Spec.Template.Spec
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatal("broker service account token must be isolated")
	}
	if pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot || pod.SecurityContext.SeccompProfile == nil {
		t.Fatalf("pod security context = %#v", pod.SecurityContext)
	}
	container := pod.Containers[0]
	if container.SecurityContext == nil || container.SecurityContext.ReadOnlyRootFilesystem == nil || !*container.SecurityContext.ReadOnlyRootFilesystem || container.SecurityContext.AllowPrivilegeEscalation == nil || *container.SecurityContext.AllowPrivilegeEscalation || len(container.SecurityContext.Capabilities.Drop) == 0 {
		t.Fatalf("container security context = %#v", container.SecurityContext)
	}
	if container.Resources.Requests.Cpu().IsZero() || container.Resources.Limits.Memory().IsZero() {
		t.Fatalf("resources = %#v", container.Resources)
	}
	if container.Lifecycle == nil || container.Lifecycle.PreStop == nil || container.Lifecycle.PreStop.Exec == nil {
		t.Fatal("missing drain preStop")
	}
	if pod.TerminationGracePeriodSeconds == nil || *pod.TerminationGracePeriodSeconds <= 62 {
		t.Fatalf("grace = %v, want > 62", pod.TerminationGracePeriodSeconds)
	}
	for _, want := range []string{"--broker-dial-timeout=5s", "--broker-max-handles=128", "--broker-max-receipts=4096"} {
		if !strings.Contains(strings.Join(container.Args, "\n"), want) {
			t.Fatalf("missing runtime bound %q", want)
		}
	}
	for _, args := range [][]string{
		{"template", "production", ".", "-f", "ci/production-values.yaml", "--set", "terminationGracePeriodSeconds=62"},
		{"template", "production", ".", "-f", "ci/production-values.yaml", "--set-json", `networkPolicy.operatorEgress=[{}]`},
		{"template", "production", ".", "-f", "ci/production-values.yaml", "--set-json", `networkPolicy.operatorEgress=[{"to":[{"ipBlock":{"cidr":"192.0.2.0/24"}}]}]`},
	} {
		if _, err := exec.Command("helm", args...).CombinedOutput(); err == nil {
			t.Fatalf("accepted invalid values %q", args)
		}
	}
}

func TestMecabrokerChart_ExplicitEgressAndDigestRender(t *testing.T) {
	rendered := renderChart(t, "template", "production", ".", "-f", "ci/production-values.yaml")
	policy := networkPolicyFromRender(t, rendered)
	if len(policy.Spec.Egress) != 1 || len(policy.Spec.Egress[0].To) != 1 || len(policy.Spec.Egress[0].Ports) != 1 {
		t.Fatalf("explicit egress = %#v", policy.Spec.Egress)
	}
	if !strings.Contains(rendered, "ghcr.io/stacklok/mecatl/mecabroker@sha256:") {
		t.Fatal("image did not render repository@digest")
	}
}
