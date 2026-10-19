package executionkind_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
	"github.com/stacklok/mecatl/internal/executionenv"
)

func scriptRange(t *testing.T, file, start, end string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	i, j := strings.Index(body, start), strings.Index(body, end)
	if i < 0 || j <= i {
		t.Fatalf("missing script range in %s", file)
	}
	return body[i:j]
}

func TestProductionKindUsesShorterSyncPeriodsOnlyInFixture(t *testing.T) {
	create := scriptRange(t, "run.sh", "kind_config=\n", "\nphase_done cluster")
	for _, profile := range []string{"production", "development"} {
		t.Run(profile, func(t *testing.T) {
			state := t.TempDir()
			out, err := runStep(t, state, "kind() { :; }\n"+create,
				"state="+state, "cluster=owned", "kubeconfig="+filepath.Join(state, "kubeconfig"), "MECATL_EXECUTION_QUAL_PROFILE="+profile)
			if err != nil {
				t.Fatalf("render kind cluster config: %v: %s", err, out)
			}
			data, err := os.ReadFile(filepath.Join(state, "kind.yaml"))
			if profile == "development" {
				if !os.IsNotExist(err) {
					t.Fatalf("development fixture unexpectedly wrote Kind config: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var cluster struct {
				Nodes []struct {
					Role                 string   `yaml:"role"`
					KubeadmConfigPatches []string `yaml:"kubeadmConfigPatches"`
				} `yaml:"nodes"`
			}
			if err := yaml.Unmarshal(data, &cluster); err != nil {
				t.Fatal(err)
			}
			if len(cluster.Nodes) != 1 || cluster.Nodes[0].Role != "control-plane" || len(cluster.Nodes[0].KubeadmConfigPatches) != 2 {
				t.Fatalf("production Kind sync patches missing: %+v", cluster.Nodes)
			}
			var patch struct {
				APIVersion    string `yaml:"apiVersion"`
				Kind          string `yaml:"kind"`
				SyncFrequency string `yaml:"syncFrequency"`
			}
			if err := yaml.Unmarshal([]byte(cluster.Nodes[0].KubeadmConfigPatches[0]), &patch); err != nil {
				t.Fatal(err)
			}
			if patch.APIVersion != "kubelet.config.k8s.io/v1beta1" || patch.Kind != "KubeletConfiguration" || patch.SyncFrequency != "5s" {
				t.Fatalf("production Kind kubelet patch = %+v", patch)
			}
			var controller struct {
				APIVersion        string `yaml:"apiVersion"`
				Kind              string `yaml:"kind"`
				ControllerManager struct {
					ExtraArgs map[string]string `yaml:"extraArgs"`
				} `yaml:"controllerManager"`
			}
			if err := yaml.Unmarshal([]byte(cluster.Nodes[0].KubeadmConfigPatches[1]), &controller); err != nil {
				t.Fatal(err)
			}
			if controller.APIVersion != "kubeadm.k8s.io/v1beta3" || controller.Kind != "ClusterConfiguration" || len(controller.ControllerManager.ExtraArgs) != 1 || controller.ControllerManager.ExtraArgs["resource-quota-sync-period"] != "10s" {
				t.Fatalf("production Kind controller-manager patch = %+v", controller)
			}
		})
	}
}

func TestKindNetworkPolicyRuntimeValues(t *testing.T) {
	render := scriptRange(t, "run.sh", "runtime_values=", "\n# Render one operator-approved revision")
	for _, profile := range []string{"development", "production"} {
		t.Run(profile, func(t *testing.T) {
			state := t.TempDir()
			out, err := runStep(t, state, `
kube() {
  case "$*" in
    *"get service kubernetes"*"clusterIP"*) printf '10.100.0.1' ;;
    *"get endpoints kubernetes"*"addresses"*) printf '10.89.4.5' ;;
    *"get endpoints kubernetes"*"ports"*) printf '6443' ;;
    *"get pod network-fixture"*)
      test "$MECATL_EXECUTION_QUAL_PROFILE" = production || exit 1
      printf '10.244.0.9' ;;
    *) exit 1 ;;
  esac
}
`+render+"\nprintf '%s' \"$runtime_values\"", "state="+state, "MECATL_EXECUTION_QUAL_PROFILE="+profile)
			if err != nil {
				t.Fatalf("render runtime network values: %v: %s", err, out)
			}
			data, err := os.ReadFile(string(out))
			if err != nil {
				t.Fatal(err)
			}
			var values struct {
				Provider struct {
					APIServerCIDRs []string `yaml:"apiServerCIDRs"`
				} `yaml:"provider"`
				NetworkPolicy struct {
					APIServerPorts   []int          `yaml:"apiServerPorts"`
					WorkloadProfiles map[string]any `yaml:"workloadProfiles"`
				} `yaml:"networkPolicy"`
			}
			if err := yaml.Unmarshal(data, &values); err != nil {
				t.Fatal(err)
			}
			if strings.Join(values.Provider.APIServerCIDRs, ",") != "10.100.0.1/32,10.89.4.5/32" {
				t.Fatalf("API policy must admit only service and discovered endpoint: %+v", values.Provider)
			}
			ports := values.NetworkPolicy.APIServerPorts
			if len(ports) != 2 || ports[0] != 443 || ports[1] != 6443 {
				t.Fatalf("API policy lacks service or translated port: %v", ports)
			}
			if (len(values.NetworkPolicy.WorkloadProfiles) != 0) != (profile == "production") {
				t.Fatal("workload egress must remain production-only")
			}
		})
	}
}

func TestKindNetworkPolicyRejectsInvalidEndpoints(t *testing.T) {
	render := scriptRange(t, "run.sh", "runtime_values=", "\n# Render one operator-approved revision")
	for _, invalid := range []string{"", "fd00::1", "10.0.0.256", "10.0.1", "10.0.0.1/24", "10.0.0.1\n10.0.0.2", "10.0.0.1\n0.0.0.0/0"} {
		for _, field := range []string{"service", "endpoint", "fixture"} {
			t.Run(field+"/"+invalid, func(t *testing.T) {
				state := t.TempDir()
				out, err := runStep(t, state, `
kube() {
  case "$*" in
    *"get service kubernetes"*) field=service ;;
    *"get endpoints kubernetes"*"addresses"*) field=endpoint ;;
    *"get endpoints kubernetes"*"ports"*) printf '6443'; return ;;
    *"get pod network-fixture"*) field=fixture ;;
    *) exit 1 ;;
  esac
  if [ "$field" = "$invalid_field" ]; then printf '%s' "$invalid_ip"; else printf '10.0.0.1'; fi
}
`+render, "state="+state, "MECATL_EXECUTION_QUAL_PROFILE=production", "invalid_field="+field, "invalid_ip="+invalid)
				if err == nil || !strings.Contains(string(out), "invalid discovered IPv4 network-policy") {
					t.Fatalf("invalid IPv4 endpoint accepted: err=%v output=%s", err, out)
				}
			})
		}
	}
}

func TestCanonicalTemplateFixtureTracksWorkloadDigest(t *testing.T) {
	var previous string
	for _, marker := range []string{"a", "b"} {
		image := "example.invalid/workload@sha256:" + strings.Repeat(marker, 64)
		path := filepath.Join(t.TempDir(), "templates.yaml")
		derivative := "example.invalid/derivative@sha256:" + strings.Repeat("c", 64)
		incompatible := "example.invalid/incompatible@sha256:" + strings.Repeat("d", 64)
		cmd := exec.Command("go", "run", "../execution-templates", "template-recipes.yaml", image, derivative, incompatible, path)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("render template fixture: %v: %s", err, out)
		}
		registry, err := executioncontroller.LoadTemplates(path)
		if err != nil {
			t.Fatal("rendered chart values disagree with provider loader:", err)
		}
		revision := strings.TrimSpace(string(out))
		if revision != registry.DefaultRevision("go") || revision == previous || registry.DefaultRevision("operator-utility") == "" || registry.DefaultRevision("operator-utility") == revision {
			t.Fatalf("fixture images did not produce distinct canonical revisions: %s", revision)
		}
		if registry.DefaultRevision("incompatible-derivative") == "" || registry.DefaultRevision("incompatible-derivative") == registry.DefaultRevision("operator-utility") {
			t.Fatal("incompatible fixture must have its own exact image revision")
		}
		previous = revision
	}
}

func TestIncompatibleDerivativeProtocolFixture(t *testing.T) {
	fixture, err := filepath.Abs("fixture/derivative/incompatible-protocol")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	sentinel := filepath.Join(workspace, "sentinel")
	if err := os.WriteFile(sentinel, []byte("retained\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", fixture)
	cmd.Dir = workspace
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + workspace}
	cmd.Stdin = strings.NewReader("{\"operation\":\"command.start\",\"command\":\"printf corrupted > sentinel\"}\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var envelope executionenv.ExecutorEnvelope
	if err := executionenv.DecodeStrict(out, &envelope); err == nil {
		t.Fatalf("negative fixture produced compatible protocol: %s", out)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "retained\n" {
		t.Fatal("negative fixture executed the request")
	}
}

func TestTemplateFixtureNeverSeedsLegacyAllocations(t *testing.T) {
	body, err := os.ReadFile("run.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"legacyfixture", "legacy-profiles.yaml", "--skip-crds", "profiles.go.image"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("hard-cut fixture still provisions %q", forbidden)
		}
	}
}

func TestQualificationTestBinaryWaitAndWorkingDirectory(t *testing.T) {
	build := scriptRange(t, "run.sh", "test_build_start=$(date +%s)", "\nkube create namespace execution-qualification")
	run := scriptRange(t, "run.sh", "test_build_wait_start=$(date +%s)", "\nphase_done tests")
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "compiled", true: "build-failed"}[failed], func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, "state")
			for _, dir := range []string{"bin", "state", "e2e/k8s_execution"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			binary := filepath.Join(state, "qualification.test")
			writeFixture(t, binary, "#!/bin/sh\nprintf 'stale executed\\n'\n", 0o700)
			writeFixture(t, filepath.Join(root, "fresh.test"), "#!/bin/sh\nprintf 'cwd=%s args=%s profile=%s\\n' \"$PWD\" \"$*\" \"$MECATL_EXECUTION_QUAL_PROFILE\"\n", 0o700)
			builder := `#!/bin/sh
set -eu
test "$*" = "test -c -tags kind_execution_e2e -o $HOME/state/qualification.test ./e2e/k8s_execution"
`
			if failed {
				builder += "exit 17\n"
			} else {
				builder += "cp \"$HOME/fresh.test\" \"$HOME/state/qualification.test\"\n"
			}
			writeFixture(t, filepath.Join(root, "bin/go"), builder, 0o700)
			out, err := runStep(t, root, `
set -eu
dev() { (cd "$root" && "$@"); }
`+build+"\n"+run, "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"), "root="+root, "state="+state, "kubeconfig="+filepath.Join(state, "kubeconfig"), "context=owned", "go_revision=v1-"+strings.Repeat("a", 64), "MECATL_EXECUTION_DEV_TOOLBOX=", "MECATL_EXECUTION_QUAL_PROFILE=production")
			if failed {
				if err == nil || strings.Contains(string(out), "stale executed") || strings.Contains(string(out), "cwd=") {
					t.Fatalf("failed test build executed binary: %v: %s", err, out)
				}
				return
			}
			want := "cwd=" + filepath.Join(root, "e2e/k8s_execution") + " args=-test.v -test.count=1 -test.timeout=45m profile=production"
			if err != nil || !strings.Contains(string(out), want) || strings.Contains(string(out), "stale executed") {
				t.Fatalf("test binary ran from wrong directory or lost test flags: %v: %s", err, out)
			}
		})
	}
}

func TestQualificationEarlyFailureStopsBuildBeforeDiagnostics(t *testing.T) {
	cleanup := scriptRange(t, "run.sh", "test_build_pid=\ncleanup_test_build()", "\nexport KUBECONFIG")
	build := scriptRange(t, "run.sh", "test_build_start=$(date +%s)", "\nkube create namespace execution-qualification")
	root := t.TempDir()
	for _, dir := range []string{"bin", "state", "deploy/mecatl-execution-kind"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture(t, filepath.Join(root, "bin/go"), "#!/bin/sh\nprintf 'build-start\\n' >> \"$HOME/events\"\nexec sleep 5\n", 0o700)
	writeFixture(t, filepath.Join(root, "deploy/mecatl-execution-kind/collect-failure.sh"), "#!/bin/sh\nprintf 'collect\\n' >> \"$HOME/events\"\n", 0o700)
	started := time.Now()
	out, err := runStep(t, root, "set -eu\nkind() { printf 'delete\\n' >> \"$HOME/events\"; }\n"+cleanup+"\n"+build+`
while [ ! -f "$HOME/events" ]; do sleep 0.01; done
exit 17
`, "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"), "root="+root, "state="+filepath.Join(root, "state"), "cluster=owned", "kubeconfig="+filepath.Join(root, "state/kubeconfig"), "context=kind-owned", "MECATL_EXECUTION_DEV_TOOLBOX=", "MECATL_EXECUTION_QUAL_CI=1")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 17 || time.Since(started) >= 4*time.Second {
		t.Fatalf("early failure delayed or masked: %v: %s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(root, "events"))
	if err != nil || string(data) != "build-start\ncollect\ndelete\n" {
		t.Fatalf("build must stop before evidence and cleanup: %q: %v: %s", data, err, out)
	}
}

func TestProductionHelmInstallsCurrentCRDAndCanonicalTemplates(t *testing.T) {
	body, err := os.ReadFile("run.sh")
	if err != nil {
		t.Fatal(err)
	}
	deploy := scriptRange(t, "run.sh", "set -- upgrade --install mecatl-execution", "\nkube -n execution-qualification create configmap execution-mock")
	if strings.Contains(deploy, "--skip-crds") || strings.Contains(deploy, "--server-side=false") {
		t.Fatal("initial provider install must use Helm's default CRD and apply mode")
	}
	for _, required := range []string{"template-recipes.yaml", "templates-values-$run_id.yaml", "--set-string execution.templateRevision=\"$go_revision\""} {
		if !strings.Contains(string(body), required) {
			t.Fatalf("qualification deploy does not wire %q", required)
		}
	}
}

func TestDerivativeBuildUsesVerifiedLocalBaseForDockerAndPodman(t *testing.T) {
	build := scriptRange(t, "run.sh", "# Give BuildKit a resolvable, run-unique local name", "\nimage_step_done incompatible_derivative_build")
	for _, runtime := range []string{"docker", "podman"} {
		t.Run(runtime, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, runtime)
			writeFixture(t, bin, `#!/bin/sh
set -eu
case "$1" in
image) test "$2" = inspect; printf 'sha256:%064d\n' 0 ;;
tag) test "$2" = "$WORKLOAD_TAG"; test "$3" = "$BASE_REF" ;;
build)
  test "$2" = "$PULL_POLICY"
  shift 2
  if [ "$1" = --target ]; then test "$2" = incompatible; shift 2; fi
  test "$1" = --build-arg; test "$2" = "EXECUTOR_BASE=$BASE_REF" ;;
*) exit 1 ;;
esac
`, 0o700)
			base := "localhost/mecatl-execution-base:qual-owned"
			out, err := runStep(t, root, "set -eu\nimage_step_done() { :; }\n"+build, "PATH="+root+":"+os.Getenv("PATH"), "runtime="+runtime, "run_id=owned", "workload_tag=localhost/workload:e2e", "WORKLOAD_TAG=localhost/workload:e2e", "BASE_REF="+base, "PULL_POLICY="+map[string]string{"docker": "--pull=false", "podman": "--pull=never"}[runtime], "root="+root)
			if err != nil {
				t.Fatalf("derivative local FROM: %v: %s", err, out)
			}
		})
	}
}

// Run the actual clean-environment live entrypoint and shared digest against
// an owned offline fixture. Fake executables reject private-data projections.
func TestQualifiedLiveHandoffRejectsDriftBeforeCredentialLoader(t *testing.T) {
	for _, fault := range []string{"", "leaf-renewal", "default", "sha", "run", "attempt", "cluster", "state", "session", "missing", "malformed", "selector", "config", "spec", "deployment-uid", "image", "revision", "trust", "catalog", "authority", "alias", "node", "secret-uid", "read", "ready", "checkout", "oidc-spec", "oidc-uid", "oidc-image", "oidc-ready", "networkpolicy", "service", "serviceaccount", "role", "rolebinding"} {
		t.Run("fault="+fault, func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, ".scratch/k8s-execution/owned")
			scripts := filepath.Join(root, "deploy/mecatl-execution-kind")
			for _, dir := range []string{state + "/images", state + "/pki", scripts, filepath.Join(root, "bin"), filepath.Join(root, "inventory")} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"live.sh", "handoff.sh", "images.sh"} {
				data, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				writeFixture(t, filepath.Join(scripts, name), string(data), 0o700)
			}
			const cluster = "mecatl-execution-qual-owned"
			sha := strings.Repeat("a", 40)
			digest := "sha256:" + strings.Repeat("b", 64)
			kubeconfig := filepath.Join(state, "kubeconfig")
			writeFixture(t, kubeconfig, "private synthetic kubeconfig", 0o600)
			writeFixture(t, filepath.Join(state, "ownership"), "cluster="+cluster+"\ncontext=kind-"+cluster+"\nkubeconfig="+kubeconfig+"\nnamespace=execution-qualification\nowner=fixture\nruntime=docker\nprofile=production\n", 0o600)
			writeFixture(t, filepath.Join(state, "go-template-revision"), "v1-"+strings.Repeat("c", 64)+"\n", 0o600)
			writeFixture(t, filepath.Join(state, "pki/ca.crt"), "public CA", 0o600)
			writeFixture(t, filepath.Join(state, "pki/ca.key"), "PRIVATE-SENTINEL", 0o600)
			var proof, aliases strings.Builder
			for _, name := range []string{"provider", "agent", "oidc", "netprobe", "workload", "derivative", "incompatible"} {
				proof.WriteString(name + "=ko.local/" + name + "@" + digest + "\n")
				aliases.WriteString("ko.local/" + name + "@" + digest + " type " + digest + "\nko.local/" + name + ":head type " + digest + "\n")
			}
			aliases.WriteString("localhost/mecatl-execution-workload:e2e type " + digest + "\nlocalhost/mecatl-execution-workload@" + digest + " type " + digest + "\n")
			writeFixture(t, filepath.Join(state, "images/proof"), proof.String(), 0o600)
			writeFixture(t, filepath.Join(root, "aliases"), aliases.String(), 0o600)
			writeFixture(t, filepath.Join(root, "fault"), "", 0o600)
			// Independent installed chart/fixture inventory: never synthesize an
			// arbitrary object from the name requested by the handoff implementation.
			goHash := sha256.Sum256([]byte("go"))
			inventory := map[string][]string{
				"networkpolicy": {
					`{"kind":"NetworkPolicy","metadata":{"name":"mecatl-execution-provider","uid":"np-provider"},"spec":{"podSelector":{"matchLabels":{"app.kubernetes.io/name":"mecatl-execution"}},"policyTypes":["Ingress","Egress"],"ingress":[{"from":[{"podSelector":{"matchLabels":{"app.kubernetes.io/name":"mecak8s"}}}],"ports":[{"port":8443}]}]}}`,
					`{"kind":"NetworkPolicy","metadata":{"name":"mecatl-execution-workload-default-deny","uid":"np-deny"},"spec":{"podSelector":{"matchExpressions":[{"key":"execution.mecatl.dev/environment","operator":"Exists"}]},"policyTypes":["Ingress","Egress"],"ingress":[],"egress":[]}}`,
					fmt.Sprintf(`{"kind":"NetworkPolicy","metadata":{"name":"mecatl-execution-profile-%x","uid":"np-go"},"spec":{"podSelector":{"matchLabels":{"execution.mecatl.dev/template-id":"%x"}},"policyTypes":["Egress"],"egress":[{"to":[{"ipBlock":{"cidr":"10.244.0.5/32"}}],"ports":[{"protocol":"TCP","port":8080}]}]}}`, goHash[:8], goHash[:8]),
					`{"kind":"NetworkPolicy","metadata":{"name":"mecak8s-raw-driver","uid":"np-driver"},"spec":{"podSelector":{"matchLabels":{"app.kubernetes.io/component":"raw-driver"}},"policyTypes":["Ingress"],"ingress":[{"from":[{"podSelector":{"matchLabels":{"app.kubernetes.io/component":"agent"}}}],"ports":[{"protocol":"TCP","port":9090}]}]}}`,
				},
				"service": {
					`{"kind":"Service","metadata":{"name":"mecatl-execution","uid":"svc-provider"},"spec":{"selector":{"app.kubernetes.io/name":"mecatl-execution"},"ports":[{"name":"grpc","port":8443,"targetPort":"grpc"}]}}`,
					`{"kind":"Service","metadata":{"name":"mecak8s","uid":"svc-agent"},"spec":{"selector":{"app.kubernetes.io/name":"mecak8s","app.kubernetes.io/component":"agent"},"ports":[{"name":"grpc","port":8080},{"name":"http","port":8081}]}}`,
					`{"kind":"Service","metadata":{"name":"oidc-issuer","uid":"svc-oidc"},"spec":{"selector":{"app.kubernetes.io/name":"oidc-issuer"},"ports":[{"name":"https","port":8443,"targetPort":"https"}]}}`,
					`{"kind":"Service","metadata":{"name":"redis","uid":"svc-redis"},"spec":{"selector":{"app.kubernetes.io/name":"redis"},"ports":[{"name":"redis","port":6379}]}}`,
				},
				"serviceaccount": {
					`{"kind":"ServiceAccount","metadata":{"name":"default","uid":"sa-default"}}`,
					`{"kind":"ServiceAccount","metadata":{"name":"mecatl-execution","uid":"sa-provider"}}`,
					`{"kind":"ServiceAccount","metadata":{"name":"mecatl-execution-executor","uid":"sa-executor"},"automountServiceAccountToken":false}`,
					`{"kind":"ServiceAccount","metadata":{"name":"mecak8s","uid":"sa-agent"},"automountServiceAccountToken":true}`,
				},
				"role": {
					`{"kind":"Role","metadata":{"name":"mecatl-execution","uid":"role-provider"},"rules":[{"apiGroups":["execution.mecatl.dev"],"resources":["executionenvironments"],"verbs":["get","list","watch","create","update","patch","delete"]}]}`,
					`{"kind":"Role","metadata":{"name":"mecak8s-lease-holder","uid":"role-agent"},"rules":[{"apiGroups":["coordination.k8s.io"],"resources":["leases"],"verbs":["get","create","update","delete"]}]}`,
				},
				"rolebinding": {
					`{"kind":"RoleBinding","metadata":{"name":"mecatl-execution","uid":"rb-provider"},"roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"Role","name":"mecatl-execution"},"subjects":[{"kind":"ServiceAccount","name":"mecatl-execution","namespace":"execution-qualification"}]}`,
					`{"kind":"RoleBinding","metadata":{"name":"mecak8s-lease-holder","uid":"rb-agent"},"roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"Role","name":"mecak8s-lease-holder"},"subjects":[{"kind":"ServiceAccount","name":"mecak8s","namespace":"execution-qualification"}]}`,
				},
			}
			for resource, items := range inventory {
				writeFixture(t, filepath.Join(root, "inventory", resource), `{"items":[`+strings.Join(items, ",")+`]}`, 0o600)
			}
			prefix := "#!/bin/sh\nset -eu\ntest -z \"${OPENROUTER_API_KEY+x}\" && test -z \"${AWS_SECRET_ACCESS_KEY+x}\"\nfault=$(cat \"$HOME/fault\")\n"
			writeFixture(t, filepath.Join(root, "bin/git"), prefix+"case \"$1\" in rev-parse) printf '%s\\n' '"+sha+"' ;; diff) test \"$fault\" != checkout ;; *) exit 99 ;; esac\n", 0o700)
			writeFixture(t, filepath.Join(root, "bin/docker"), prefix+`
case "$1" in
inspect)
  case "$4" in
  *Labels*) printf '%s\n' '`+cluster+`' ;;
  *) marker=stable; test "$fault" != node || marker=drift; printf 'node-%s image-id\n' "$marker" ;;
  esac ;;
exec) cat "$HOME/aliases" ;;
image) printf '%s\n' '`+digest+`' ;;
build) printf 'build\n' >> "$HOME/events" ;;
save) printf 'save\n' >> "$HOME/events"; : > "$4" ;;
*) exit 99 ;;
esac
`, 0o700)
			writeFixture(t, filepath.Join(root, "bin/kind"), prefix+"printf 'load\\n' >> \"$HOME/events\"\n", 0o700)
			writeFixture(t, filepath.Join(root, "bin/ko"), prefix+`
printf 'build\n' >> "$HOME/events"
case "$*" in *mecatl-execution-provider*) printf 'ko.local/provider:head\n' ;; *mecak8s*) printf 'ko.local/agent:head\n' ;; *) exit 99 ;; esac
`, 0o700)
			writeFixture(t, filepath.Join(root, "bin/helm"), prefix+"printf 'helm\\n' >> \"$HOME/events\"\n", 0o700)
			writeFixture(t, filepath.Join(root, "bin/go"), prefix+`
case "$*" in
*'credentialloader stage '*) printf 'stage\n' >> "$HOME/events"; exit 17 ;;
*'^TestKindExecutionQualification$'*) printf 'mock-test\n' >> "$HOME/events" ;;
*) exit 99 ;;
esac
`, 0o700)
			writeFixture(t, filepath.Join(root, "bin/kubectl"), prefix+`
if [ "$1" = --kubeconfig ]; then shift 4; fi
case "$*" in
'config current-context') printf 'kind-`+cluster+`\n' ;;
'config view '*)
  marker=stable; test "$fault" != config || marker=drift
  printf '{"clusters":[{"name":"owned","cluster":{"server":"%s"}}],"users":[{"name":"owned","user":{"client-certificate-data":"DATA+OMITTED","client-key-data":"DATA+OMITTED"}}]}\n' "$marker" ;;
'-n execution-qualification rollout status deployment/mecatl-execution --timeout=240s'|'-n execution-qualification rollout status deployment/mecak8s --timeout=240s'|'-n execution-qualification rollout status deployment/oidc-issuer --timeout=240s'|'-n execution-qualification wait --for=condition=Ready pod -l app.kubernetes.io/name=mecatl-execution --timeout=240s'|'-n execution-qualification wait --for=condition=Ready pod -l app.kubernetes.io/name=mecak8s --timeout=240s'|'-n execution-qualification wait --for=condition=Ready pod -l app.kubernetes.io/name=oidc-issuer --timeout=240s')
  test "$fault" != ready
  case "$*" in *oidc-issuer*) test "$fault" != oidc-ready ;; esac ;;
*'rollout restart '*) printf 'restart\n' >> "$HOME/events" ;;
*'create configmap '*) printf 'mock-setup\n' >> "$HOME/events"; printf 'mock\n' ;;
'apply -f -') cat >/dev/null ;;
'-n execution-qualification get secret '*)
  case "$5" in
  execution-security|execution-client-tls|execution-trust|execution-fixture-ca|oidc-fixture-identity) ;;
  *) echo 'unknown Secret rejected' >&2; exit 99 ;;
  esac
  case "$*" in
  *'.metadata.uid'*) marker=stable; test "$fault" != secret-uid || marker=drift; printf 'uid-%s\n' "$marker" ;;
  *'.data.clients\.pem'*|*'.data.ca\.crt'*|*'secret execution-fixture-ca -o jsonpath={.data.tls\.crt}'*) marker=stable; test "$fault" != trust || marker=drift; printf 'public-trust-%s' "$marker" ;;
  *) echo 'private Secret read rejected' >&2; exit 99 ;;
  esac ;;
'-n execution-qualification get '*)
  test "$fault" != read || exit 1
  marker=stable
  case "$*:$fault" in
  *deployment/*:spec|*configmap/execution-mock*:config|*configmap/mecatl-execution-templates*:catalog|*configmap/mecatl-execution-security-authority*:authority) marker=drift ;;
  esac
  case "$4" in
  deployment/mecatl-execution) image=ko.local/provider@`+digest+`; app=mecatl-execution; container=provider ;;
  deployment/mecak8s) image=ko.local/agent@`+digest+`; app=mecak8s; container=agent ;;
  deployment/oidc-issuer) image=ko.local/oidc@`+digest+`; app=oidc-issuer; container=issuer ;;
  namespace/execution-qualification|configmap/execution-mock|configmap/mecatl-execution-templates|configmap/mecatl-execution-security-manifest|configmap/mecatl-execution-security-authority|issuer/execution-fixture-ca|certificate/execution-provider-tls|certificate/execution-client-tls)
    case "$4" in namespace/*) kind=Namespace ;; configmap/*) kind=ConfigMap ;; issuer/*) kind=Issuer ;; certificate/*) kind=Certificate ;; esac
    printf '{"kind":"%s","metadata":{"name":"%s","uid":"uid-%s"},"spec":{"marker":"%s"},"data":{"marker":"%s"}}\n' "$kind" "${4#*/}" "$4" "$marker" "$marker"
    exit 0 ;;
  networkpolicy|service|serviceaccount|role|rolebinding)
    if [ "$fault" = "$4" ]; then
      case "$4" in
      networkpolicy) change='.items[0].spec.ingress = []' ;;
      service) change='.items[0].spec.selector = {"app.kubernetes.io/name":"intruder"}' ;;
      serviceaccount) change='.items[2].automountServiceAccountToken = true' ;;
      role) change='.items[0].rules[0].verbs = ["*"]' ;;
      rolebinding) change='.items[0].subjects[0].name = "intruder"' ;;
      esac
      jq "$change" "$HOME/inventory/$4"
    else cat "$HOME/inventory/$4"; fi
    exit 0 ;;
  *) echo "unknown resource rejected: $4" >&2; exit 99 ;;
  esac
  uid=uid
  case "$4:$fault" in
  deployment/*:deployment-uid|deployment/oidc-issuer:oidc-uid) uid=changed ;;
  deployment/*:image|deployment/oidc-issuer:oidc-image) image=changed ;;
  deployment/oidc-issuer:oidc-spec) marker=drift ;;
  esac
  printf '{"kind":"Deployment","metadata":{"name":"%s","uid":"%s"},"spec":{"marker":"%s","selector":{"matchLabels":{"app.kubernetes.io/name":"%s"}},"template":{"metadata":{"labels":{"app.kubernetes.io/name":"%s"}},"spec":{"containers":[{"name":"%s","image":"%s"}]}}},"status":{"clock":"%s"}}\n' "${4#*/}" "$uid" "$marker" "$app" "$app" "$container" "$image" "$fault" ;;
*) echo "unexpected kubectl request: $*" >&2; exit 99 ;;
esac
`, 0o700)
			env := []string{"PATH=" + filepath.Join(root, "bin") + ":" + os.Getenv("PATH"), "USER=fixture", "TMPDIR=" + t.TempDir(), "GITHUB_ACTIONS=true", "GITHUB_SHA=" + sha, "GITHUB_RUN_ID=123", "GITHUB_RUN_ATTEMPT=2", "MECATL_EXECUTION_HANDOFF_SHA=" + sha, "MECATL_EXECUTION_QUAL_STATE=" + state}
			out, err := runStep(t, root, `set -eu
state=$MECATL_EXECUTION_QUAL_STATE
cluster=`+cluster+`
context=kind-$cluster
runtime=docker
kubeconfig=$state/kubeconfig
kube() { kubectl --kubeconfig "$kubeconfig" --context "$context" "$@"; }
. ./deploy/mecatl-execution-kind/handoff.sh
handoff_digest
`, env...)
			qualified := strings.TrimSpace(string(out))
			if err != nil || len(qualified) != 64 {
				t.Fatalf("produce offline digest: %v: %s", err, out)
			}
			// Only modeled state changes affect the digest; status/leaf clocks don't.
			commandFault := ""
			if strings.Contains(" config spec deployment-uid image trust catalog authority node secret-uid read ready checkout leaf-renewal oidc-spec oidc-uid oidc-image oidc-ready networkpolicy service serviceaccount role rolebinding ", " "+fault+" ") {
				commandFault = fault
			}
			writeFixture(t, filepath.Join(root, "fault"), commandFault, 0o600)
			if fault == "alias" {
				writeFixture(t, filepath.Join(root, "aliases"), strings.ReplaceAll(aliases.String(), "type "+digest, "type sha256:"+strings.Repeat("d", 64)), 0o600)
			}
			selector, complete, run, attempt, capturedCluster, capturedState, session := "1", "1", "123", "2", cluster, state, ""
			switch fault {
			case "default":
				selector = "0"
			case "sha":
				sha = strings.Repeat("d", 40)
			case "run":
				run = "124"
			case "attempt":
				attempt = "3"
			case "cluster":
				capturedCluster = "mecatl-execution-qual-other"
			case "state":
				capturedState = state + "/../owned"
			case "session":
				session = "continued"
			case "missing":
				complete = ""
			case "malformed":
				qualified = "$(touch injected)"
			case "selector":
				selector = "$(touch injected)"
			case "revision":
				writeFixture(t, filepath.Join(state, "go-template-revision"), "v1-"+strings.Repeat("d", 64)+"\n", 0o600)
			case "leaf-renewal":
				writeFixture(t, filepath.Join(state, "pki/mecak8s.crt"), "renewed public leaf", 0o600)
			}
			// Replace explicitly instead of relying on duplicate environment names.
			env = env[:len(env)-2]
			env = append(env, "MECATL_EXECUTION_HANDOFF_SHA="+sha, "MECATL_EXECUTION_QUAL_STATE="+capturedState, "MECATL_EXECUTION_HANDOFF_COMPLETE="+complete, "MECATL_EXECUTION_HANDOFF_DIGEST="+qualified, "MECATL_EXECUTION_HANDOFF_RUN_ID="+run, "MECATL_EXECUTION_HANDOFF_RUN_ATTEMPT="+attempt, "MECATL_EXECUTION_HANDOFF_CLUSTER="+capturedCluster, "MECATL_EXECUTION_LIVE_SESSION="+session, "MECATL_EXECUTION_LIVE_QUALIFIED="+selector, "MECATL_EXECUTION_CREDENTIAL_FILE="+filepath.Join(root, "provider;touch injected"), "OPENROUTER_API_KEY=PROVIDER-SENTINEL", "AWS_SECRET_ACCESS_KEY=PRIVATE-SENTINEL")
			verifyEnv := make([]string, 0, len(env))
			for _, entry := range env {
				if !strings.HasPrefix(entry, "MECATL_EXECUTION_CREDENTIAL_FILE=") {
					verifyEnv = append(verifyEnv, entry)
				}
			}
			out, err = runStep(t, root, "sh ./deploy/mecatl-execution-kind/live.sh --verify-handoff", verifyEnv...)
			if (err == nil) != (fault == "" || fault == "leaf-renewal") {
				t.Fatalf("secret-free verification admission: %v: %s", err, out)
			}
			if events, err := os.ReadFile(filepath.Join(root, "events")); !os.IsNotExist(err) {
				t.Fatalf("verify-only prepared, mutated, or staged credentials: %q: %v", events, err)
			}
			out, err = runStep(t, root, "sh ./deploy/mecatl-execution-kind/live.sh", env...)
			if err == nil {
				t.Fatal("fake credentialloader must stop before real live mutation")
			}
			events, readErr := os.ReadFile(filepath.Join(root, "events"))
			wantStage := fault == "" || fault == "leaf-renewal" || fault == "default"
			if wantStage {
				if readErr != nil || !strings.HasSuffix(string(events), "stage\n") {
					t.Fatalf("valid admission did not stage: %q: %v: %s", events, readErr, out)
				}
				if fault != "default" && string(events) != "stage\n" {
					t.Fatalf("qualified reuse rebuilt/mutated/reran: %q", events)
				}
				if fault == "default" && (!strings.Contains(string(events), "mock-test\nstage\n") || !strings.Contains(string(events), "load\n") || !strings.Contains(string(events), "restart\n")) {
					t.Fatalf("standalone prerequisite lost: %q", events)
				}
			} else if !os.IsNotExist(readErr) {
				t.Fatalf("drift mutated or reached credentials: %q: %v: %s", events, readErr, out)
			}
			if strings.Contains(string(out), "PRIVATE-SENTINEL") || strings.Contains(string(out), "PROVIDER-SENTINEL") {
				t.Fatal("handoff disclosed secret data")
			}
			if _, err := os.Stat(filepath.Join(root, "injected")); !os.IsNotExist(err) {
				t.Fatal("selector/output executed as shell code")
			}
		})
	}
}

func TestProductionThenLiveImageAlias(t *testing.T) {
	// The alias fixture exercises the load loop without the runner's timing helper.
	production := "image_step_done() { :; }\n" + scriptRange(t, "run.sh", "load_index=0\nfor item in", "printf 'provider=%s")
	live := scriptRange(t, "live.sh", ". \"$root/deploy/mecatl-execution-kind/images.sh\"", "\nif [ \"$qualified\" = 1 ]; then")
	helper, err := os.ReadFile("images.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, runtime := range []string{"docker", "podman"} {
		for _, conflict := range []bool{false, true} {
			t.Run(runtime+"/conflict="+map[bool]string{false: "false", true: "true"}[conflict], func(t *testing.T) {
				root := t.TempDir()
				for _, dir := range []string{"bin", "images", "deploy/mecatl-execution-kind"} {
					if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				writeFixture(t, filepath.Join(root, "deploy/mecatl-execution-kind/images.sh"), string(helper), 0o600)
				digest := "sha256:" + strings.Repeat("a", 64)
				tag := "ko.local/provider:head"
				alias := "ko.local/provider@" + digest
				db := filepath.Join(root, "db")
				var sourceRows strings.Builder
				for _, name := range []string{"provider", "agent", "oidc", "netprobe", "workload", "derivative", "incompatible"} {
					sourceRows.WriteString("ko.local/" + name + ":head type " + digest + "\n")
				}
				writeFixture(t, db, sourceRows.String(), 0o600)
				writeFixture(t, filepath.Join(root, "bin", runtime), `#!/bin/sh
set -eu
case "$1" in
save) : > "$4" ;;
exec)
  test "$2" = owned-control-plane
  shift 5
  test "$1" = images
  case "$2" in
  ls) cat "$DB" ;;
  tag)
    test "$#" = 4
    if awk -v ref="$4" '$1 == ref {found=1} END {exit !found}' "$DB"; then
      echo 'already exists' >&2; exit 1
    fi
    digest=$(awk -v ref="$3" '$1 == ref {print $3}' "$DB")
    printf '%s type %s\n' "$4" "$digest" >> "$DB"
    printf 'tag\n' >> "$TAGS"
    ;;
  *) exit 1 ;;
  esac ;;
*) exit 1 ;;
esac
`, 0o700)
				writeFixture(t, filepath.Join(root, "bin/kind"), "#!/bin/sh\nset -eu\ntest \"$1 $2\" = 'load image-archive'\ntest -f \"$3\"\ntest \"$4 $5\" = '--name owned'\n", 0o700)
				env := []string{"PATH=" + filepath.Join(root, "bin") + ":" + os.Getenv("PATH"), "root=" + root, "state=" + root, "runtime=" + runtime, "cluster=owned", "DB=" + db, "TAGS=" + filepath.Join(root, "tags")}
				for _, name := range []string{"provider", "agent", "oidc", "netprobe", "workload", "derivative", "incompatible"} {
					env = append(env, name+"_tag=ko.local/"+name+":head")
				}
				out, err := runStep(t, root, production, env...)
				if err != nil {
					t.Fatalf("fresh production load: %v: %s", err, out)
				}
				if conflict {
					writeFixture(t, db, tag+" type "+digest+"\n"+alias+" type sha256:"+strings.Repeat("b", 64)+"\n", 0o600)
				}
				out, err = runStep(t, root, live+"\nload_image \"$provider_tag\"", env...)
				if (err == nil) == conflict {
					t.Fatalf("retained live load: %v: %s", err, out)
				}
				if !conflict && strings.TrimSpace(string(out)) != alias {
					t.Fatalf("wrong alias: %s", out)
				}
				tags, err := os.ReadFile(filepath.Join(root, "tags"))
				if err != nil || string(tags) != strings.Repeat("tag\n", 7) {
					t.Fatalf("must create each alias once, never overwrite/re-tag: %q, %v", tags, err)
				}
			})
		}
	}
}

func TestLivePreservesQualifiedStorageAndHelmDigestThroughRestoration(t *testing.T) {
	live := scriptRange(t, "live.sh", "printf 'provider=%s", "\nfi # standalone preparation") + "\n" + scriptRange(t, "live.sh", "secret=", "\necho \"live qualification passed")
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "helm-calls")
	kubeMarker := filepath.Join(root, "kube-calls")
	devMarker := filepath.Join(root, "dev-calls")
	digest := "sha256:" + strings.Repeat("a", 64)
	out, err := runStep(t, root, `
set -u
kube() {
  case "$*" in
  "-n execution-qualification create configmap execution-mock --from-file=mock-script.json=$root/deploy/mecatl-execution-kind/mock-script.json --dry-run=client -o yaml")
    printf 'mock-config\n' >> "$KUBE_MARKER"
    printf 'synthetic mock config\n' ;;
  'apply -f -')
    payload=$(cat)
    test "$payload" = 'synthetic mock config' || { echo 'forbidden applied manifest' >&2; return 1; } ;;
  '-n execution-qualification rollout status deployment/mecatl-execution --timeout=240s')
    printf 'provider-ready\n' >> "$KUBE_MARKER" ;;
  '-n execution-qualification rollout restart deployment/mecak8s')
    printf 'agent-restart\n' >> "$KUBE_MARKER" ;;
  '-n execution-qualification rollout status deployment/mecak8s --timeout=240s')
    printf 'agent-ready\n' >> "$KUBE_MARKER" ;;
  *) echo "forbidden Kubernetes operation: $*" >&2; return 1 ;;
  esac
}
kube_jq() { jq "$@"; }
dev() {
  case "$*" in
  *'go test -tags kind_execution_e2e -run ^TestKindExecutionQualification$ '*) printf 'mock-test\n' >> "$DEV_MARKER" ;;
  *'go run -tags kind_execution_e2e ./e2e/k8s_execution/fixture/credentialloader stage '*) printf 'stage\n' >> "$DEV_MARKER" ;;
  *'go test -tags kind_execution_e2e -run ^TestKindExecutionLiveQualification$ '*) printf 'live-test\n' >> "$DEV_MARKER" ;;
  *) echo "unexpected dev operation: $*" >&2; return 1 ;;
  esac
}
helm_kube() {
  test "$1 $2 $3" = 'upgrade --install mecak8s' || return 1
  # Model a nonempty inherited tag; apply the actual CLI overrides in order.
  tag=e2e digest= repository= profile=mock
  while [ "$#" -gt 0 ]; do
    case "$1" in
    --set-string|--set)
      shift
      case "$1" in
      image.tag=*) tag=${1#*=} ;;
      image.digest=*) digest=${1#*=} ;;
      image.repository=*) repository=${1#*=} ;;
      mockProvider=false) profile=live ;;
      esac ;;
    esac
    shift
  done
  test -z "$tag" && test "$repository@$digest" = "$agent_image" || return 1
  printf '%s\n' "$profile" >> "$MARKER"
}
`+live, "root="+root, "state="+root, "MARKER="+marker, "KUBE_MARKER="+kubeMarker, "DEV_MARKER="+devMarker,
		"agent_image=ko.local/mecak8s@"+digest, "provider_image=synthetic", "workload_image=synthetic", "go_image=synthetic",
		"kubeconfig=synthetic", "context=synthetic", "go_revision=v1-"+strings.Repeat("a", 64),
		"cluster=owned", "MECATL_EXECUTION_CREDENTIAL_FILE=unused")
	if err != nil {
		t.Fatalf("live must preserve qualified storage and use digest-only Helm images: %v: %s", err, out)
	}
	calls, err := os.ReadFile(marker)
	if err != nil || string(calls) != "mock\nlive\nmock\n" {
		t.Fatalf("expected digest-only mock setup, live upgrade, and mock restoration: %q: %v", calls, err)
	}
	calls, err = os.ReadFile(kubeMarker)
	if err != nil || string(calls) != "mock-config\nprovider-ready\nagent-restart\nagent-ready\nagent-ready\n" {
		t.Fatalf("expected only mock configuration and readiness operations: %q: %v", calls, err)
	}
	calls, err = os.ReadFile(devMarker)
	if err != nil || string(calls) != "mock-test\nstage\nlive-test\n" {
		t.Fatalf("expected mock qualification before credential staging and live qualification: %q: %v", calls, err)
	}
}

func TestLiveResumeDoesNotAllocateMockWorkspaces(t *testing.T) {
	preflight := scriptRange(t, "live.sh", "# Resuming an already-created", "\nfi # standalone preparation")
	out, err := runStep(t, t.TempDir(), "dev() { echo 'unexpected allocation'; return 99; }\n"+preflight, "MECATL_EXECUTION_LIVE_SESSION="+strings.Repeat("a", 32))
	if err != nil || len(out) != 0 {
		t.Fatalf("resume allocated mock workspaces: %v: %s", err, out)
	}
}

func TestLiveSignalPreservesFailureAndAttemptsCleanup(t *testing.T) {
	restore := scriptRange(t, "live.sh", "restore() {", "\ndev env -i HOME=\"$HOME\" PATH=\"$PATH\" KUBECONFIG=\"$kubeconfig\" MECATL_KUBE_CONTEXT=\"$context\" MECATL_EXECUTION_CREDENTIAL_FILE=")
	for _, signal := range []struct {
		name    string
		code    string
		command string
	}{{"INT", "130", "kill -INT $$"}, {"TERM", "143", "kill -TERM $$"}, {"live-failed", "1", "exit 1"}, {"collector-failed", "1", "exit 1"}} {
		t.Run(signal.name, func(t *testing.T) {
			root := t.TempDir()
			scripts := filepath.Join(root, "deploy/mecatl-execution-kind")
			if err := os.MkdirAll(scripts, 0o700); err != nil {
				t.Fatal(err)
			}
			collector := "#!/bin/sh\nset -eu\ntest \"$1 $2\" = 'synthetic kind-owned'\nprintf 'collect\\n' >> \"$MARKER\"\nprintf '{\"kind\":\"collection\"}\\n' > \"$3\"\n"
			if signal.name == "collector-failed" {
				collector += "exit 17\n"
			}
			writeFixture(t, filepath.Join(scripts, "collect-failure.sh"), collector, 0o700)
			writeFixture(t, filepath.Join(root, "receipt"), "synthetic", 0o600)
			writeFixture(t, filepath.Join(root, "signal.sh"), "#!/bin/sh\nset -eu\n"+`
helm_kube() { printf 'restore\n' >> "$MARKER"; }
dev() { printf 'delete\n' >> "$MARKER"; }
restore_needed=1
`+restore+"\n"+signal.command+"\nexit 99\n", 0o700)
			out, err := runStep(t, root, "if sh ./signal.sh; then exit 1; else test \"$?\" -eq "+signal.code+"; fi", "root="+root, "state="+root, "MARKER="+filepath.Join(root, "cleanup"), "receipt="+filepath.Join(root, "receipt"), "agent_image=synthetic@sha256:fake", "go_revision=v1-"+strings.Repeat("a", 64), "kubeconfig=synthetic", "context=kind-owned", "secret=synthetic")
			if err != nil {
				t.Fatalf("signal was reported as success: %v: %s", err, out)
			}
			calls, err := os.ReadFile(filepath.Join(root, "cleanup"))
			if err != nil || string(calls) != "collect\nrestore\ndelete\n" {
				t.Fatalf("evidence must precede restoration and UID cleanup, even on collection failure: %q: %v", calls, err)
			}
			status, err := os.ReadFile(filepath.Join(root, "live-diagnostics.status"))
			want := "complete\n"
			if signal.name == "collector-failed" {
				want = "incomplete\n"
			}
			if err != nil || string(status) != want {
				t.Fatal("diagnostic failure status lost")
			}
		})
	}
}

func TestProductionPublishesOwnershipBeforePartialCreation(t *testing.T) {
	data, err := os.ReadFile("run.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"partial", "discovery", "collision"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			scripts := filepath.Join(root, "deploy/mecatl-execution-kind")
			bin := filepath.Join(root, "bin")
			for _, dir := range []string{scripts, bin} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			writeFixture(t, filepath.Join(scripts, "run.sh"), string(data), 0o700)
			writeFixture(t, filepath.Join(bin, "docker"), "#!/bin/sh\nexit 99\n", 0o700)
			writeFixture(t, filepath.Join(root, "fault"), fault+"\n", 0o600)
			writeFixture(t, filepath.Join(bin, "kind"), `#!/bin/sh
set -eu
IFS= read -r fault < "$HOME/fault"
case "$1" in
get)
  case "$fault" in
  discovery) exit 1 ;;
  collision) awk -F= '$1 == "cluster" {print $2}' .scratch/k8s-execution/*/ownership ;;
  esac ;;
create)
  test -s "$MECATL_EXECUTION_QUAL_OUTPUT"
  printf 'creation attempted\n' > "$HOME/attempted"
  exit 1 ;;
*) exit 99 ;;
esac
`, 0o700)
			output := filepath.Join(root, "output")
			marker := filepath.Join(root, "attempted")
			out, err := runStep(t, root, "./deploy/mecatl-execution-kind/run.sh", "PATH="+bin+":"+os.Getenv("PATH"), "CONTAINER_ENGINE=docker", "MECATL_EXECUTION_QUAL_PROFILE=production", "MECATL_EXECUTION_QUAL_OUTPUT="+output)
			if err == nil {
				t.Fatalf("partial production unexpectedly succeeded: %s", out)
			}
			outputs, err := os.ReadFile(output)
			if fault != "partial" {
				if !os.IsNotExist(err) {
					t.Fatal("ownership published before collision/discovery gate")
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("creation attempted after failed preflight")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(outputs)), "\n")
			if len(lines) != 2 || !strings.HasPrefix(lines[0], "state=") || !strings.HasPrefix(lines[1], "cluster=mecatl-execution-qual-") {
				t.Fatalf("invalid ownership outputs: %q", outputs)
			}
			state := strings.TrimPrefix(lines[0], "state=")
			ownership, err := os.ReadFile(filepath.Join(state, "ownership"))
			if err != nil || !strings.Contains(string(ownership), lines[1]+"\n") {
				t.Fatal("output does not bind the initial ownership record")
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatal("fixture did not reach partial cluster creation")
			}
			if _, err := os.Stat(filepath.Join(state, "kubeconfig")); !os.IsNotExist(err) {
				t.Fatal("partial fixture unexpectedly has a kubeconfig")
			}
		})
	}
}
