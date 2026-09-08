package mecabroker_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

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
	for _, set := range []string{"image.digest=", "image.digest=sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "image.digest=sha256:deadbeef"} {
		if _, err := exec.Command("helm", "template", "production", ".", "-f", "ci/production-values.yaml", "--set", set).CombinedOutput(); err == nil {
			t.Fatalf("accepted invalid image digest %q", set)
		}
	}
}

func TestSingletonBrokerRemediation_Scenario4_SingletonTopologyAndExposure(t *testing.T) {
	rendered := renderChart(t, "template", "production", ".", "-f", "ci/production-values.yaml")
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
			var d struct {
				Spec struct {
					Replicas *int32 `yaml:"replicas"`
					Strategy struct {
						Type string `yaml:"type"`
					} `yaml:"strategy"`
				} `yaml:"spec"`
			}
			if err := yaml.Unmarshal([]byte(document), &d); err != nil {
				t.Fatal(err)
			}
			if d.Spec.Replicas == nil || *d.Spec.Replicas != 1 || d.Spec.Strategy.Type != "Recreate" {
				t.Fatalf("deployment topology = %#v", d.Spec)
			}
			deployments++
		case "Service":
			var s struct {
				Spec struct {
					Ports []struct {
						Name string `yaml:"name"`
					} `yaml:"ports"`
				} `yaml:"spec"`
			}
			if err := yaml.Unmarshal([]byte(document), &s); err != nil {
				t.Fatal(err)
			}
			for _, p := range s.Spec.Ports {
				if p.Name == "admin" {
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
	koPins := 0
	for jobName, job := range workflow.Jobs {
		for _, step := range job.Steps {
			uses, _ := step["uses"].(string)
			if strings.Contains(uses, "ko-build/setup-ko@") {
				koPins++
				with, ok := step["with"].(map[string]any)
				if !ok || with["version"] != "v0.18.1" {
					t.Fatalf("job %s has an unpinned setup-ko step: %#v", jobName, step)
				}
			}
			if run, ok := step["run"].(string); ok && strings.Contains(run, "--password-stdin") {
				if strings.Contains(run, "secrets.GITHUB_TOKEN") || !strings.Contains(run, "GITHUB_TOKEN") {
					t.Fatalf("job %s interpolates registry credentials in shell: %s", jobName, run)
				}
				env, ok := step["env"].(map[string]any)
				if !ok || env["GITHUB_TOKEN"] == nil {
					t.Fatalf("job %s login lacks step-scoped credential env", jobName)
				}
			}
		}
	}
	if koPins != 4 {
		t.Fatalf("release workflow setup-ko steps = %d, want 4", koPins)
	}
}
