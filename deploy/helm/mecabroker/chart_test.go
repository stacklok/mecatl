package mecabroker_test

import (
	"os/exec"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/yaml"
)

func renderChart(t *testing.T, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required for chart render tests")
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

func TestNetworkPolicyIngressDefaultsToDenyAndUsesPublicContainerPort(t *testing.T) {
	t.Run("empty peers default deny", func(t *testing.T) {
		rendered := renderChart(t, "template", "production", ".", "-f", "ci/production-values.yaml", "--set-json", "networkPolicy.publicFrom=[]")
		policy := networkPolicyFromRender(t, rendered)
		if len(policy.Spec.Ingress) != 0 {
			t.Fatalf("empty peer lists rendered %d ingress rules, want default-deny", len(policy.Spec.Ingress))
		}
	})

	t.Run("configured peers target the multiplexed public port", func(t *testing.T) {
		rendered := renderChart(t, "template", "production", ".", "-f", "ci/production-values.yaml", "--set", "networkPolicy.publicFrom[0].ipBlock.cidr=192.0.2.0/24")
		policy := networkPolicyFromRender(t, rendered)
		if len(policy.Spec.Ingress) != 1 {
			t.Fatalf("configured peers rendered %d ingress rules, want 1", len(policy.Spec.Ingress))
		}
		if got := policy.Spec.Ingress[0].Ports[0].Port.String(); got != "public" {
			t.Fatalf("public ingress port = %q, want public", got)
		}
	})
}
