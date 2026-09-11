package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

type sdkScenario7Workflow struct {
	Jobs map[string]sdkScenario7Job `yaml:"jobs"`
}

type sdkScenario7Job struct {
	Strategy struct {
		FailFast *bool `yaml:"fail-fast"`
		Matrix   struct {
			DenoVersion []string `yaml:"deno-version"`
			NodeVersion []string `yaml:"node-version"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps          []sdkScenario7Step `yaml:"steps"`
	TimeoutMinutes int                `yaml:"timeout-minutes"`
}

type sdkScenario7Step struct {
	Env  map[string]string `yaml:"env"`
	Run  string            `yaml:"run"`
	Uses string            `yaml:"uses"`
	With map[string]string `yaml:"with"`
}

func TestSDKTypescriptRelease_Scenario7_NodeMatrixShape(t *testing.T) {
	t.Parallel()

	root := sdkScenario7RepoRoot(t)
	workflow := readSDKScenario7Workflow(t, filepath.Join(root, ".github", "workflows", "ci.yml"))
	matrixJob, ok := workflow.Jobs["sdk"]
	if !ok {
		t.Fatal("CI workflow has no sdk job")
	}
	if matrixJob.TimeoutMinutes <= 0 {
		t.Fatal("sdk matrix job must declare a positive timeout-minutes")
	}
	if matrixJob.Strategy.FailFast == nil || *matrixJob.Strategy.FailFast {
		t.Fatal("sdk matrix job must declare fail-fast: false")
	}
	wantNodeVersions := []string{"22.x", "24.x"}
	if !reflect.DeepEqual(matrixJob.Strategy.Matrix.NodeVersion, wantNodeVersions) {
		t.Fatalf("sdk node-version matrix = %v, want literal pins %v", matrixJob.Strategy.Matrix.NodeVersion, wantNodeVersions)
	}
	assertSDKScenario7ActionInput(t, matrixJob, "actions/setup-node@", "node-version", "${{ matrix.node-version }}")
	for _, command := range []string{
		"task sdk:typecheck",
		"task sdk:test",
		"task sdk:build",
		"task sdk:api:check",
	} {
		if count := sdkScenario7CommandCount(matrixJob, command); count != 1 {
			t.Errorf("sdk matrix command %q count = %d, want 1", command, count)
		}
	}
	for _, forbidden := range []string{"actions/setup-go@", "bufbuild/buf-setup-action@"} {
		if sdkScenario7HasAction(matrixJob, forbidden) {
			t.Errorf("sdk matrix must not provision Node-independent action %q", forbidden)
		}
	}
	if sdkScenario7CommandCount(matrixJob, "task generate") != 0 || sdkScenario7CommandCount(matrixJob, "task sdk:e2e") != 0 {
		t.Error("sdk matrix must not repeat codegen freshness or the Go-binary e2e")
	}

	integrationJob, ok := workflow.Jobs["sdk-integration"]
	if !ok {
		t.Fatal("CI workflow has no single-run sdk-integration job")
	}
	if integrationJob.TimeoutMinutes <= 0 {
		t.Fatal("sdk-integration job must declare a positive timeout-minutes")
	}
	assertSDKScenario7ActionInput(t, integrationJob, "oven-sh/setup-bun@", "bun-version", "1.4.1")
	assertSDKScenario7ActionInput(t, integrationJob, "actions/setup-node@", "node-version", "24.x")
	for _, command := range []string{
		"task generate",
		"git diff --exit-code -- contracts/gen sdk/typescript/src/gen",
		"task sdk:e2e",
	} {
		if count := sdkScenario7WorkflowCommandCount(workflow, command); count != 1 {
			t.Errorf("workflow command %q count = %d, want exactly one non-matrix run", command, count)
		}
		if count := sdkScenario7CommandCount(integrationJob, command); count != 1 {
			t.Errorf("sdk-integration command %q count = %d, want 1", command, count)
		}
	}

	var packageManifest struct {
		Engines map[string]string `json:"engines"`
	}
	readSDKScenario7JSON(t, filepath.Join(root, "sdk", "typescript", "package.json"), &packageManifest)
	if got := packageManifest.Engines["node"]; got != ">=22" {
		t.Errorf("engines.node = %q, want %q", got, ">=22")
	}
}

func TestSDKTypescriptRelease_Scenario7_CompatibilityGateSeparation(t *testing.T) {
	t.Parallel()

	root := sdkScenario7RepoRoot(t)
	sdkRoot := filepath.Join(root, "sdk", "typescript")
	configs, err := filepath.Glob(filepath.Join(sdkRoot, "api-extractor*.json"))
	if err != nil {
		t.Fatalf("glob API Extractor configs: %v", err)
	}
	if len(configs) != 3 {
		t.Fatalf("API Extractor config count = %d, want root, node, and deno configs: %v", len(configs), configs)
	}

	wantReports := map[string]string{
		"api-extractor.deno.json": "mecatl-sdk-deno.api.md",
		"api-extractor.json":      "mecatl-sdk.api.md",
		"api-extractor.node.json": "mecatl-sdk-node.api.md",
	}
	wantEntries := map[string]string{
		"api-extractor.deno.json": "<projectFolder>/dist/deno.d.ts",
		"api-extractor.json":      "<projectFolder>/dist/index.d.ts",
		"api-extractor.node.json": "<projectFolder>/dist/node.d.ts",
	}
	for _, path := range configs {
		var config struct {
			MainEntryPointFilePath string `json:"mainEntryPointFilePath"`
			APIReport              struct {
				Enabled        *bool  `json:"enabled"`
				ReportFileName string `json:"reportFileName"`
			} `json:"apiReport"`
		}
		readSDKScenario7JSON(t, path, &config)
		name := filepath.Base(path)
		if got := config.MainEntryPointFilePath; got != wantEntries[name] {
			t.Errorf("%s main entry point = %q, want %q", name, got, wantEntries[name])
		}
		if got := config.APIReport.ReportFileName; got != wantReports[name] {
			t.Errorf("%s report = %q, want reviewed artifact %q", name, got, wantReports[name])
		}
		if strings.Contains(config.MainEntryPointFilePath, "/gen/") || strings.Contains(config.APIReport.ReportFileName, "gen") {
			t.Errorf("%s improperly folds generated declarations into API Extractor", name)
		}
		if _, err := os.Stat(filepath.Join(sdkRoot, "etc", wantReports[name])); err != nil {
			t.Errorf("reviewed API report %q is missing: %v", wantReports[name], err)
		}
	}

	var bufConfig struct {
		Breaking struct {
			Use []string `yaml:"use"`
		} `yaml:"breaking"`
	}
	bufBytes := readSDKScenario7File(t, filepath.Join(root, "buf.yaml"))
	if err := yaml.Unmarshal(bufBytes, &bufConfig); err != nil {
		t.Fatalf("parse buf.yaml: %v", err)
	}
	if !reflect.DeepEqual(bufConfig.Breaking.Use, []string{"FILE"}) {
		t.Errorf("buf breaking policy = %v, want declared FILE policy", bufConfig.Breaking.Use)
	}

	workflowPaths, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	if err != nil {
		t.Fatalf("glob workflows: %v", err)
	}
	for _, path := range workflowPaths {
		if strings.Contains(string(readSDKScenario7File(t, path)), "buf breaking --against") {
			t.Errorf("%s claims the deferred proto breaking-change gate", filepath.Base(path))
		}
	}
	workflow := readSDKScenario7Workflow(t, filepath.Join(root, ".github", "workflows", "ci.yml"))
	if count := sdkScenario7WorkflowCommandCount(workflow, "task generate"); count != 1 {
		t.Errorf("codegen freshness task count = %d, want 1", count)
	}
	if count := sdkScenario7WorkflowCommandCount(workflow, "git diff --exit-code -- contracts/gen sdk/typescript/src/gen"); count != 1 {
		t.Errorf("generated Go/TypeScript diff gate count = %d, want 1", count)
	}
}

// TestTypeScriptSDKDeno_Scenario1_RuntimeMatrix is AC1.3: the package claim,
// CI floor/current matrix, release floor, and repository task stay one contract.
func TestTypeScriptSDKDeno_Scenario1_RuntimeMatrix(t *testing.T) {
	t.Parallel()

	root := sdkScenario7RepoRoot(t)
	ci := readSDKScenario7Workflow(t, filepath.Join(root, ".github", "workflows", "ci.yml"))
	job, ok := ci.Jobs["sdk-deno"]
	if !ok {
		t.Fatal("CI workflow has no sdk-deno job")
	}
	if job.TimeoutMinutes <= 0 {
		t.Fatal("sdk-deno job must declare a positive timeout-minutes")
	}
	if job.Strategy.FailFast == nil || *job.Strategy.FailFast {
		t.Fatal("sdk-deno job must declare fail-fast: false")
	}
	wantVersions := []string{"2.9.3", "2.x"}
	if !reflect.DeepEqual(job.Strategy.Matrix.DenoVersion, wantVersions) {
		t.Fatalf("sdk-deno matrix = %v, want floor/current pins %v", job.Strategy.Matrix.DenoVersion, wantVersions)
	}
	assertSDKScenario7ActionInput(t, job, "denoland/setup-deno@", "deno-version", "${{ matrix.deno-version }}")
	if count := sdkScenario7CommandCount(job, "task sdk:deno"); count != 1 {
		t.Errorf("sdk-deno task count = %d, want 1", count)
	}

	release := readSDKScenario7Workflow(t, filepath.Join(root, ".github", "workflows", "release-sdk-typescript.yml"))
	verify, ok := release.Jobs["verify"]
	if !ok {
		t.Fatal("SDK release workflow has no verify job")
	}
	assertSDKScenario7ActionInput(t, verify, "denoland/setup-deno@", "deno-version", "2.9.3")
	if count := sdkScenario7CommandCount(verify, "task sdk:deno"); count != 1 {
		t.Errorf("release Deno task count = %d, want 1", count)
	}
	for _, step := range verify.Steps {
		if strings.TrimSpace(step.Run) == "task sdk:deno" && step.Env["MECATL_SDK_PACKED_TARBALL"] != "${{ github.workspace }}/sdk/typescript/.release/${{ steps.package.outputs.tarball }}" {
			t.Error("release Deno gate must consume the exact packed release artifact")
		}
	}

	var manifest struct {
		Engines map[string]string `json:"engines"`
		Exports map[string]any    `json:"exports"`
	}
	readSDKScenario7JSON(t, filepath.Join(root, "sdk", "typescript", "package.json"), &manifest)
	if got := manifest.Engines["deno"]; got != ">=2.9.3 <3" {
		t.Errorf("engines.deno = %q, want %q", got, ">=2.9.3 <3")
	}
	if _, ok := manifest.Exports["./deno"]; !ok {
		t.Error("package exports have no ./deno entry point")
	}

	taskfile := string(readSDKScenario7File(t, filepath.Join(root, "sdk", "typescript", "Taskfile.yml")))
	for _, required := range []string{"node scripts/run-deno-integration.mjs", "MECATL_SDK_PACKED_TARBALL"} {
		if !strings.Contains(taskfile, required) {
			t.Errorf("SDK Taskfile does not wire %q into the Deno gate", required)
		}
	}
}

func sdkScenario7RepoRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
}

func readSDKScenario7Workflow(t *testing.T, path string) sdkScenario7Workflow {
	t.Helper()
	var workflow sdkScenario7Workflow
	if err := yaml.Unmarshal(readSDKScenario7File(t, path), &workflow); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return workflow
}

func readSDKScenario7JSON(t *testing.T, path string, target any) {
	t.Helper()
	if err := json.Unmarshal(readSDKScenario7File(t, path), target); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

func readSDKScenario7File(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func assertSDKScenario7ActionInput(t *testing.T, job sdkScenario7Job, actionPrefix, key, want string) {
	t.Helper()
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, actionPrefix) {
			if got := step.With[key]; got != want {
				t.Errorf("%s input %s = %q, want %q", actionPrefix, key, got, want)
			}
			return
		}
	}
	t.Errorf("job is missing action %q", actionPrefix)
}

func sdkScenario7HasAction(job sdkScenario7Job, actionPrefix string) bool {
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, actionPrefix) {
			return true
		}
	}
	return false
}

func sdkScenario7CommandCount(job sdkScenario7Job, command string) int {
	count := 0
	for _, step := range job.Steps {
		for _, line := range strings.Split(step.Run, "\n") {
			if strings.TrimSpace(line) == command {
				count++
			}
		}
	}
	return count
}

func sdkScenario7WorkflowCommandCount(workflow sdkScenario7Workflow, command string) int {
	count := 0
	for _, job := range workflow.Jobs {
		count += sdkScenario7CommandCount(job, command)
	}
	return count
}
