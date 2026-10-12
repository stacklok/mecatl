package executionkind_test

import (
	"os"
	"os/exec"
	"path/filepath"
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
	start := strings.Index(body, "\nif [ \"${MECATL_EXECUTION_QUAL_PROFILE:-development}\" = production ]; then\n  if [ \"$natural_expiry\" = 1 ]; then")
	end := strings.Index(body, "\nif [ \"${MECATL_EXECUTION_QUAL_CI:-}\" = 1 ]; then\n  echo")
	serial := strings.Index(body, "-test.timeout=45m\nphase_done tests")
	if serial < 0 || start <= serial || end <= start {
		t.Fatal("renewal phases must follow the complete serial suite")
	}
	for _, tc := range []struct {
		profile, selector string
		want              string
	}{
		{"production", "0", "^TestKindExecutionInFlightAutomaticRenewal$ 30m\n"},
		{"production", "1", "^TestKindExecutionAutomaticCertificateRenewal$ 80m\n^TestKindExecutionInFlightAutomaticRenewal$ 30m\n"},
		{"development", "0", ""},
	} {
		t.Run(tc.profile+"/"+tc.selector, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "calls")
			stub := `
set -eu
dev() {
  selection= ceiling= renewal=
  for arg do
    case "$arg" in
      MECATL_EXECUTION_QUAL_RENEWAL=1) renewal=1 ;;
      ^TestKindExecution*) selection=$arg ;;
      -test.timeout=*) ceiling=${arg#*=} ;;
    esac
  done
  test "$renewal" = 1
  printf '%s %s\n' "$selection" "$ceiling" >> "$MARKER"
}
phase_done() { printf 'done=%s\n' "$1"; }
kubeconfig=fixture context=fixture go_revision=fixture state=fixture
`
			out, err := runStep(t, root, stub+body[start:end], "MECATL_EXECUTION_QUAL_PROFILE="+tc.profile, "natural_expiry="+tc.selector, "MARKER="+marker)
			if err != nil {
				t.Fatalf("renewal selection: %v: %s", err, out)
			}
			calls, err := os.ReadFile(marker)
			if err != nil && !(tc.want == "" && os.IsNotExist(err)) {
				t.Fatal(err)
			}
			if string(calls) != tc.want {
				t.Fatalf("renewal calls=%q, want %q", calls, tc.want)
			}
			if tc.profile == "production" && (!strings.Contains(string(out), "phase=inflight_automatic_renewal starting") || !strings.Contains(string(out), "done=inflight_automatic_renewal")) {
				t.Fatal("mandatory renewal phase evidence missing")
			}
			if tc.profile == "production" && tc.selector == "0" && !strings.Contains(string(out), "phase=automatic_certificate_renewal skipped selector=0") {
				t.Fatal("natural expiry skip evidence missing")
			}
		})
	}
	ci, err := os.ReadFile("../../.github/workflows/k8s-e2e.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On struct {
			Dispatch struct {
				Inputs map[string]struct {
					Type    string `yaml:"type"`
					Default bool   `yaml:"default"`
				} `yaml:"inputs"`
			} `yaml:"workflow_dispatch"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Timeout string         `yaml:"timeout-minutes"`
			Steps   []workflowStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(ci, &workflow); err != nil {
		t.Fatal(err)
	}
	input, ok := workflow.On.Dispatch.Inputs["natural_expiry"]
	job := workflow.Jobs["execution-production-e2e"]
	if !ok || input.Type != "boolean" || input.Default || job.Timeout != "${{ github.event_name == 'workflow_dispatch' && inputs.natural_expiry && 210 || 100 }}" {
		t.Fatal("natural expiry must be explicit manual opt-in with separate job budgets")
	}
	found := false
	for _, step := range job.Steps {
		if step.Run == "task e2e:k8s:execution:production" {
			found = step.Env["MECATL_EXECUTION_QUAL_NATURAL_EXPIRY"] == "${{ github.event_name == 'workflow_dispatch' && inputs.natural_expiry && '1' || '0' }}"
		}
	}
	if !found {
		t.Fatal("Kind production selector is not tied to explicit manual opt-in")
	}
	nativeSteps(t)
}

func TestNaturalExpiryPreflightBeforeProvisioning(t *testing.T) {
	script, err := os.ReadFile("run.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, profile, selector, want string
		set                           bool
	}{
		{"default", "production", "", "CONTAINER_ENGINE must be docker or podman", false},
		{"bounded", "production", "0", "CONTAINER_ENGINE must be docker or podman", true},
		{"long", "production", "1", "CONTAINER_ENGINE must be docker or podman", true},
		{"development", "development", "0", "CONTAINER_ENGINE must be docker or podman", true},
		{"empty", "production", "", "MECATL_EXECUTION_QUAL_NATURAL_EXPIRY must be 0 or 1", true},
		{"invalid", "production", "true", "MECATL_EXECUTION_QUAL_NATURAL_EXPIRY must be 0 or 1", true},
		{"injection", "production", "$(touch injected)", "MECATL_EXECUTION_QUAL_NATURAL_EXPIRY must be 0 or 1", true},
		{"wrong-profile", "development", "1", "natural expiry qualification requires the production profile", true},
		{"missing-profile", "", "1", "natural expiry qualification requires the production profile", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "run.sh")
			writeFixture(t, path, string(script), 0o700)
			cmd := exec.CommandContext(t.Context(), path)
			cmd.Dir = root
			// Stop valid preflights at runtime validation, before any fixture state
			// or provisioning. Leave CLEAN_ENV unset to exercise the real env -i.
			cmd.Env = []string{"HOME=" + root, "PATH=" + os.Getenv("PATH"), "CONTAINER_ENGINE=preflight-stop", "MECATL_EXECUTION_QUAL_PROFILE=" + tc.profile}
			if tc.set {
				cmd.Env = append(cmd.Env, "MECATL_EXECUTION_QUAL_NATURAL_EXPIRY="+tc.selector)
			}
			out, err := cmd.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 || strings.TrimSpace(string(out)) != tc.want {
				t.Fatalf("preflight: %v: %s", err, out)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 || entries[0].Name() != "run.sh" {
				t.Fatal("preflight created state or executed selector input")
			}
		})
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
