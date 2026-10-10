package executionkind_test

import (
	"os"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestProductionRenewalSelectionAndOuterDeadlines(t *testing.T) {
	script, err := os.ReadFile("run.sh")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	serial := strings.Index(body, "-test.timeout=45m")
	phase := strings.Index(body, "phase=automatic_certificate_renewal")
	selection := strings.Index(body, "-test.run '^TestKindExecution(AutomaticCertificateRenewal|InFlightAutomaticRenewal)$' -test.timeout=105m")
	if serial < 0 || phase <= serial || selection <= phase || !strings.Contains(body[:phase], "phase_done tests") || !strings.Contains(body[phase:selection], "MECATL_EXECUTION_QUAL_RENEWAL=1") || !strings.Contains(body[selection:], "phase_done automatic_certificate_renewal") {
		t.Fatal("serial tests must be followed by both mandatory long renewals with a 105-minute test ceiling")
	}
	ci, err := os.ReadFile("../../.github/workflows/k8s-e2e.yml")
	if err != nil {
		t.Fatal(err)
	}
	job := strings.SplitN(string(ci), "  execution-production-e2e:", 2)
	if len(job) != 2 || !strings.Contains(job[1], "timeout-minutes: 210") {
		t.Fatal("production Kind CI job cannot accommodate serial plus renewal and setup")
	}
	live, err := os.ReadFile("../../.github/workflows/e2e-live.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, requirement := range []string{"native-execution-live:", "timeout-minutes: 270", "name: Qualify production execution\n        timeout-minutes: 210", "timeout --kill-after=15s 208m task e2e:k8s:execution:production", "name: Clean up exact owned resources"} {
		if !strings.Contains(string(live), requirement) {
			t.Fatalf("live CI production qualification/cleanup deadline missing: %s", requirement)
		}
	}
}

func TestCertManagerFixtureIsIssuerOwnedAndHasExactIdentities(t *testing.T) {
	script, err := os.ReadFile("run.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{"helm_kube upgrade --install cert-manager", "--version=v1.17.2", "create secret tls execution-fixture-ca", "wait --for=condition=Ready", "fixture-security-values.yaml", "phase=automatic_certificate_renewal"} {
		if !strings.Contains(string(script), link) {
			t.Fatalf("Kind fixture does not wire issuance or renewal: %s", link)
		}
	}
	values, err := os.ReadFile("fixture-security-values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{"name: execution-security", "name: execution-trust", "key: clients.pem"} {
		if !strings.Contains(string(values), link) {
			t.Fatalf("Kind provider does not consume issued leaf and trust: %s", link)
		}
	}
	data, err := os.ReadFile("fixture-tls.yaml")
	if err != nil {
		t.Fatal(err)
	}
	certs := map[string]bool{
		"execution-provider-tls":    false,
		"execution-client-tls":      false,
		"execution-intruder-tls":    false,
		"execution-operations-tls":  false,
		"execution-wrong-scope-tls": false,
	}
	for _, doc := range strings.Split(string(data), "\n---\n") {
		var resource struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				SecretName  string   `yaml:"secretName"`
				Duration    string   `yaml:"duration"`
				RenewBefore string   `yaml:"renewBefore"`
				DNSNames    []string `yaml:"dnsNames"`
				URIs        []string `yaml:"uris"`
				Usages      []string `yaml:"usages"`
				IssuerRef   struct {
					Name string `yaml:"name"`
				} `yaml:"issuerRef"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &resource); err != nil {
			t.Fatal(err)
		}
		if resource.Kind != "Certificate" || resource.Metadata.Name == "execution-fixture-ca" {
			continue
		}
		if _, ok := certs[resource.Metadata.Name]; !ok {
			t.Fatalf("unexpected leaf %s", resource.Metadata.Name)
		}
		certs[resource.Metadata.Name] = true
		secretName := resource.Metadata.Name
		if resource.Metadata.Name == "execution-provider-tls" {
			secretName = "execution-security"
		}
		if resource.Spec.SecretName != secretName || resource.Spec.Duration != "1h" || resource.Spec.RenewBefore != "50m" || resource.Spec.IssuerRef.Name != "execution-fixture-ca" {
			t.Fatalf("unsupported leaf or renewal contract: %s", resource.Metadata.Name)
		}
		if resource.Metadata.Name == "execution-provider-tls" {
			if !strings.Contains(strings.Join(resource.Spec.DNSNames, ","), "mecatl-execution.execution-qualification.svc.cluster.local") || !strings.Contains(strings.Join(resource.Spec.Usages, ","), "server auth") {
				t.Fatal("server SAN or EKU missing")
			}
		} else {
			client := strings.TrimSuffix(strings.TrimPrefix(resource.Metadata.Name, "execution-"), "-tls")
			if client == "client" {
				client = "mecak8s"
			}
			if len(resource.Spec.URIs) != 1 || resource.Spec.URIs[0] != "spiffe://mecatl.test/client/"+client || !strings.Contains(strings.Join(resource.Spec.Usages, ","), "client auth") {
				t.Fatalf("client URI or EKU missing: %s", resource.Metadata.Name)
			}
		}
	}
	for name, seen := range certs {
		if !seen {
			t.Errorf("missing certificate: %s", name)
		}
	}
}
