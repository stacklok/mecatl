package executionkind_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

type workflowStep struct {
	ID      string            `yaml:"id"`
	Timeout int               `yaml:"timeout-minutes"`
	Run     string            `yaml:"run"`
	If      string            `yaml:"if"`
	Env     map[string]string `yaml:"env"`
	With    map[string]any    `yaml:"with"`
}

func nativeSteps(t *testing.T) map[string]workflowStep {
	t.Helper()
	data, err := os.ReadFile("../../.github/workflows/e2e-live.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			If      string         `yaml:"if"`
			Steps   []workflowStep `yaml:"steps"`
			Timeout int            `yaml:"timeout-minutes"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	job := workflow.Jobs["native-execution-live"]
	if strings.Join(strings.Fields(job.If), " ") != "github.repository == 'stacklok/mecatl' && github.event_name == 'workflow_dispatch' && inputs.native_execution" || job.Timeout != 100 {
		t.Fatal("native job must remain explicitly dispatched, repo-gated, and bounded")
	}
	steps := make(map[string]workflowStep)
	total := 0
	for _, step := range job.Steps {
		if step.Timeout <= 0 {
			t.Fatal("every native step needs an explicit ceiling")
		}
		total += step.Timeout
		if step.ID != "" {
			steps[step.ID] = step
		}
		if strings.Contains(step.Run, "${{") {
			t.Fatal("workflow expressions must cross into shell through env")
		}
		if step.With["cache"] == true {
			t.Fatal("native credential job must not save a cache")
		}
	}
	if total > job.Timeout-2 || steps["cleanup"].Timeout < 10 {
		t.Fatal("stage ceilings must reserve cleanup and job overhead")
	}
	for _, id := range []string{"live", "cleanup"} {
		step := steps[id]
		if step.Env["MECATL_EXECUTION_QUAL_STATE"] != "${{ steps.production.outputs.state }}" || step.Env["NATIVE_CLUSTER"] != "${{ steps.production.outputs.cluster }}" || strings.Contains(step.Run, "/current") {
			t.Fatal("downstream ownership must use production outputs, never current")
		}
	}
	if steps["production"].Env["MECATL_EXECUTION_QUAL_CI"] != "0" || len(steps["credential"].Env) != 1 || steps["credential"].Env["OPENROUTER_API_KEY"] != "${{ secrets.OPENROUTER_API_KEY }}" {
		t.Fatal("production retention or step-scoped credential wiring drifted")
	}
	if steps["credential"].If != "success() && steps.production.outcome == 'success'" || steps["live"].If != "success() && steps.production.outcome == 'success' && steps.credential.outcome == 'success'" || steps["cleanup"].If != "always() && steps.prepare.outcome == 'success'" {
		t.Fatal("qualification must depend on actual production success and always clean up")
	}
	return steps
}

func writeFixture(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func runStep(t *testing.T, root, script string, env ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "-c", script)
	cmd.Dir = root
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + root}, env...)
	return cmd.CombinedOutput()
}

func TestNativeWorkflowCommitAndCredentialBoundary(t *testing.T) {
	steps := nativeSteps(t)
	for _, expected := range []string{"", "reviewed", "wrong", "$(touch injected)"} {
		t.Run("sha="+expected, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(bin, "git"), "#!/bin/sh\nprintf 'reviewed\\n'\n", 0o700)
			dir := filepath.Join(root, ".scratch", "ci")
			out, err := runStep(t, root, steps["prepare"].Run, "PATH="+bin+":"+os.Getenv("PATH"), "NATIVE_CI_DIR="+dir, "EVENT_SHA=reviewed", "EXPECTED_SHA="+expected, "GITHUB_STEP_SUMMARY="+filepath.Join(root, "summary"))
			wantOK := expected == "" || expected == "reviewed"
			if (err == nil) != wantOK {
				t.Fatalf("SHA gate: %v: %s", err, out)
			}
			if _, err := os.Stat(filepath.Join(root, "injected")); !os.IsNotExist(err) {
				t.Fatal("expected SHA was executed")
			}
			if !wantOK {
				return
			}
			info, err := os.Stat(dir)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatal("CI directory is not private")
			}
			if _, err := runStep(t, root, steps["credential"].Run, "NATIVE_CI_DIR="+dir, "OPENROUTER_API_KEY="); err == nil {
				t.Fatal("missing credential skipped successfully")
			}
			const fake = "synthetic-offline-key"
			out, err = runStep(t, root, steps["credential"].Run+"\ntest -z \"${OPENROUTER_API_KEY+x}\"", "NATIVE_CI_DIR="+dir, "OPENROUTER_API_KEY="+fake)
			if err != nil || strings.Contains(string(out), fake) {
				t.Fatal("credential staging failed or disclosed synthetic credential")
			}
			info, err = os.Stat(filepath.Join(dir, "provider-key"))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatal("credential file is not private")
			}
			if _, err := runStep(t, root, steps["credential"].Run, "NATIVE_CI_DIR="+dir, "OPENROUTER_API_KEY=replacement"); err == nil {
				t.Fatal("credential staging overwrote existing file")
			}
			data, err := os.ReadFile(filepath.Join(dir, "provider-key"))
			if err != nil || string(data) != fake {
				t.Fatal("existing synthetic credential changed")
			}
		})
	}
}

func TestNativeWorkflowCleanupOwnership(t *testing.T) {
	steps := nativeSteps(t)
	for _, fault := range []string{"", "owner", "context", "label", "duplicate", "symlink", "delete", "list", "switched-pointer", "captured-cluster", "missing-kubeconfig", "missing-ownership", "missing-output", "partial-with-kubeconfig", "collector-error", "collector-timeout", "collector-error-delete", "collector-timeout-delete", "live-failed", "live-failed-preserved"} {
		t.Run("fault="+fault, func(t *testing.T) {
			collectorFails := strings.HasPrefix(fault, "collector-")
			root := t.TempDir()
			state := filepath.Join(root, ".scratch", "k8s-execution", "owned")
			dir := filepath.Join(root, ".scratch", "ci")
			bin := filepath.Join(root, "bin")
			for _, path := range []string{state, dir, bin} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			const cluster = "mecatl-execution-qual-offline"
			context := "kind-" + cluster
			owner := "fixture"
			if fault == "owner" {
				owner = "someone-else"
			}
			if fault == "context" {
				context = "ambient"
			}
			kubeconfig := filepath.Join(state, "kubeconfig")
			if fault != "missing-kubeconfig" {
				writeFixture(t, kubeconfig, "synthetic", 0o600)
			}
			ownership := "cluster=" + cluster + "\ncontext=" + context + "\nowner=" + owner + "\nruntime=docker\nprofile=production\nnamespace=execution-qualification\nkubeconfig=" + kubeconfig + "\n"
			if fault == "duplicate" {
				ownership += "owner=fixture\n"
			}
			if fault != "missing-ownership" {
				writeFixture(t, filepath.Join(state, "ownership"), ownership, 0o600)
			}
			pointer := filepath.Join(filepath.Dir(state), "current")
			writeFixture(t, pointer, state+"\n", 0o600)
			if fault == "switched-pointer" {
				foreign := filepath.Join(filepath.Dir(state), "foreign")
				if err := os.Mkdir(foreign, 0o700); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, filepath.Join(foreign, "ownership"), strings.ReplaceAll(ownership, cluster, "mecatl-execution-qual-foreign"), 0o600)
				writeFixture(t, pointer, foreign+"\n", 0o600)
			}
			capturedState, capturedCluster := state, cluster
			if fault == "symlink" {
				capturedState = filepath.Join(filepath.Dir(state), "alias")
				if err := os.Symlink(state, capturedState); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "captured-cluster" {
				capturedCluster = "mecatl-execution-qual-other"
			}
			if fault == "missing-output" {
				capturedState, capturedCluster = "", ""
			}
			writeFixture(t, filepath.Join(dir, "provider-key"), "synthetic", 0o600)
			writeFixture(t, filepath.Join(state, "live-summary.json"), "{}\n", 0o600)
			writeFixture(t, filepath.Join(bin, "kubectl"), "#!/bin/sh\nprintf '%s\\n' 'kind-"+cluster+"'\n", 0o700)
			writeFixture(t, filepath.Join(bin, "docker"), "#!/bin/sh\nif [ \"$FAULT\" = label ]; then exit 1; fi\nprintf '%s\\n' '"+cluster+"'\n", 0o700)
			writeFixture(t, filepath.Join(bin, "kind"), "#!/bin/sh\nif [ \"$1\" = delete ]; then\n  test \"$*\" = 'delete cluster --name "+cluster+"' || exit 1\n  printf '%s\\n' \"$*\" >> \"$MARKER.attempt\"\n  case \"$FAULT\" in delete|collector-*-delete) exit 1 ;; esac\n  printf deleted > \"$MARKER\"\nelse\n  test \"$FAULT\" != list || exit 1\nfi\n", 0o700)
			marker := filepath.Join(root, "deleted")
			if fault == "delete" || fault == "list" || fault == "partial-with-kubeconfig" || collectorFails || fault == "live-failed" {
				scripts := filepath.Join(root, "deploy/mecatl-execution-kind")
				if err := os.MkdirAll(scripts, 0o700); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, filepath.Join(scripts, "collect-failure.sh"), "#!/bin/sh\ntest ! -e \"$MARKER.attempt\" || exit 1\nprintf collected > \"$MARKER.collection\"\ncase \"$FAULT\" in collector-error*) exit 17 ;; collector-timeout*) exit 124 ;; esac\nprintf '{\"kind\":\"collection\"}\\n' > \"$3\"\n", 0o700)
			}
			outcome := "success"
			if strings.HasPrefix(fault, "missing-") || fault == "partial-with-kubeconfig" || fault == "delete" || fault == "list" || collectorFails {
				outcome = "failure"
			}
			liveOutcome := "success"
			if strings.HasPrefix(fault, "live-failed") {
				liveOutcome = "failure"
			}
			if fault == "live-failed-preserved" {
				writeFixture(t, filepath.Join(state, "live-diagnostics.jsonl"), "{\"kind\":\"create_stage\",\"stage\":\"bind_ensure\"}\n", 0o600)
				writeFixture(t, filepath.Join(state, "live-diagnostics.status"), "complete\n", 0o600)
			}
			out, err := runStep(t, root, steps["cleanup"].Run, "PATH="+bin+":"+os.Getenv("PATH"), "NATIVE_CI_DIR="+dir, "GITHUB_WORKSPACE="+root, "USER=fixture", "PRODUCTION_OUTCOME="+outcome, "LIVE_OUTCOME="+liveOutcome, "MECATL_EXECUTION_QUAL_STATE="+capturedState, "NATIVE_CLUSTER="+capturedCluster, "FAULT="+fault, "MARKER="+marker)
			wantOK := fault == "" || fault == "switched-pointer" || fault == "partial-with-kubeconfig" || fault == "collector-error" || fault == "collector-timeout" || strings.HasPrefix(fault, "live-failed")
			if fault == "live-failed-preserved" {
				data, err := os.ReadFile(filepath.Join(dir, "live-diagnostics.jsonl"))
				if err != nil || !strings.Contains(string(data), "bind_ensure") {
					t.Fatal("pre-restoration real evidence lost")
				}
				if _, err := os.Stat(filepath.Join(dir, "production-diagnostics.jsonl")); !os.IsNotExist(err) {
					t.Fatal("mock fallback should not replace complete real evidence")
				}
			}
			if collectorFails {
				if !strings.Contains(string(out), "::warning::Bounded production diagnostics incomplete") {
					t.Fatal("collector failure must warn")
				}
				if data, err := os.ReadFile(marker + ".collection"); err != nil || string(data) != "collected" {
					t.Fatal("collector must run before deletion")
				}
				if data, err := os.ReadFile(marker + ".attempt"); err != nil || string(data) != "delete cluster --name "+cluster+"\n" {
					t.Fatal("collector failure must not skip exact owned-cluster deletion")
				}
			}
			if (err == nil) != wantOK {
				t.Fatalf("cleanup result: %v: %s", err, out)
			}
			if _, err := os.Stat(filepath.Join(dir, "provider-key")); !os.IsNotExist(err) {
				t.Fatal("CI credential was not removed independently")
			}
			if fault == "delete" || fault == "list" || fault == "partial-with-kubeconfig" || fault == "live-failed" {
				if data, err := os.ReadFile(filepath.Join(dir, "production-diagnostics.jsonl")); err != nil || !strings.Contains(string(data), "collection") {
					t.Fatal("failure evidence must be captured before cleanup, even when cleanup fails")
				}
			}
			_, deleted := os.Stat(marker)
			if (deleted == nil) != (wantOK || fault == "list") {
				t.Fatal("wrong cluster deletion decision")
			}
		})
	}
}

func TestLiveImageDigestUsesRuntimeSpecificInspect(t *testing.T) {
	data, err := os.ReadFile("live.sh")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(data), "if [ \"$runtime\" = podman ]; then\n  podman image exists")
	end := strings.Index(string(data), "\nworkload_tag=")
	if start < 0 || end < start {
		t.Fatal("image build preparation missing")
	}
	for _, runtime := range []string{"docker", "podman"} {
		t.Run(runtime, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, filepath.Join(root, runtime), "#!/bin/sh\ncase \"$*\" in\n  'image exists docker.io/library/golang:1.27'|'image inspect docker.io/library/golang:1.27') exit 0 ;;\n  'image inspect docker.io/library/golang:1.27 --format {{.Digest}}') test \"$RUNTIME\" = podman || exit 1; printf 'sha256:fake' ;;\n  'image inspect docker.io/library/golang:1.27 --format {{index .RepoDigests 0}}') test \"$RUNTIME\" = docker || exit 1; printf 'golang@sha256:fake' ;;\n  *) exit 1 ;;\nesac\n", 0o700)
			out, err := runStep(t, root, string(data[start:end])+"\ntest \"$go_image\" = docker.io/library/golang@sha256:fake", "PATH="+root+":"+os.Getenv("PATH"), "runtime="+runtime, "RUNTIME="+runtime)
			if err != nil {
				t.Fatalf("digest selection: %v: %s", err, out)
			}
		})
	}
}
