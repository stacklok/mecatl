package server

import (
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

type sdkScenario11Workflow struct {
	On struct {
		Push struct {
			Tags []string `yaml:"tags"`
		} `yaml:"push"`
	} `yaml:"on"`
	Permissions map[string]string           `yaml:"permissions"`
	Concurrency sdkScenario11Concurrency    `yaml:"concurrency"`
	Jobs        map[string]sdkScenario11Job `yaml:"jobs"`
}

type sdkScenario11Concurrency struct {
	Group            string `yaml:"group"`
	CancelInProgress *bool  `yaml:"cancel-in-progress"`
}

type sdkScenario11Job struct {
	Environment    string              `yaml:"environment"`
	If             string              `yaml:"if"`
	Needs          string              `yaml:"needs"`
	Outputs        map[string]string   `yaml:"outputs"`
	Permissions    map[string]string   `yaml:"permissions"`
	RunsOn         string              `yaml:"runs-on"`
	Steps          []sdkScenario11Step `yaml:"steps"`
	TimeoutMinutes int                 `yaml:"timeout-minutes"`
}

type sdkScenario11Step struct {
	Env  map[string]string `yaml:"env"`
	Name string            `yaml:"name"`
	Run  string            `yaml:"run"`
	Uses string            `yaml:"uses"`
	With map[string]string `yaml:"with"`
}

func TestADR_0304_ManualDispatchIsDryRunOnly(t *testing.T) {
	t.Parallel()

	workflow, source := readSDKScenario11Workflow(t, sdkScenario11ReleaseWorkflow(t))
	if !strings.Contains(source, "\n  workflow_dispatch:\n") {
		t.Fatal("SDK release workflow must expose a no-input workflow_dispatch dry run")
	}
	if got := sortedSDKScenario11Keys(workflow.Jobs); !reflect.DeepEqual(got, []string{"publish", "verify"}) {
		t.Fatalf("release jobs = %v, want exactly verify and publish", got)
	}
	verify := workflow.Jobs["verify"]
	publish := workflow.Jobs["publish"]
	if !reflect.DeepEqual(verify.Permissions, map[string]string{"contents": "read"}) {
		t.Errorf("verify permissions = %v, want contents: read only", verify.Permissions)
	}
	if publish.Needs != "verify" {
		t.Errorf("publish needs = %q, want verify", publish.Needs)
	}
	if publish.Environment != "npm-publish" {
		t.Errorf("publish environment = %q, want npm-publish", publish.Environment)
	}
	const publishCondition = "github.event_name == 'push' && startsWith(github.ref, 'refs/tags/sdk/typescript/v')"
	if publish.If != publishCondition {
		t.Errorf("publish if = %q, want %q", publish.If, publishCondition)
	}
	if !reflect.DeepEqual(publish.Permissions, map[string]string{"contents": "read", "id-token": "write"}) {
		t.Errorf("publish permissions = %v, want contents: read + id-token: write", publish.Permissions)
	}

	uploads := 0
	for jobName, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
				uploads++
				if jobName != "verify" {
					t.Errorf("artifact upload is in %s, want verify", jobName)
				}
				if got := step.With["path"]; !strings.HasSuffix(got, "${{ steps.package.outputs.tarball }}") {
					t.Errorf("uploaded path = %q, want the one packed tarball", got)
				}
			}
		}
	}
	if uploads != 1 {
		t.Errorf("artifact upload count = %d, want exactly one", uploads)
	}
	if sdkScenario11StepNamed(verify, "Inspect exact packed inventory and consumers") < 0 {
		t.Error("verify does not reach the exact tarball inventory oracle")
	}

	installPattern := regexp.MustCompile(`(?m)\b(?:npm|pnpm|yarn)\s+(?:ci|install|add)\b`)
	for _, step := range publish.Steps {
		if installPattern.MatchString(step.Run) || strings.Contains(step.Run, "task sdk:") {
			t.Errorf("publish step %q installs or builds instead of consuming the artifact", step.Name)
		}
	}
}

func TestSDKTypescriptRelease_Scenario11_TagVersionParity(t *testing.T) {
	t.Parallel()

	workflow, _ := readSDKScenario11Workflow(t, sdkScenario11ReleaseWorkflow(t))
	verify := workflow.Jobs["verify"]
	checkout := sdkScenario11Action(verify, "actions/checkout@")
	if checkout == nil {
		t.Fatal("verify has no checkout action")
	}
	if checkout.With["ref"] != "${{ github.sha }}" || checkout.With["fetch-depth"] != "0" {
		t.Errorf("checkout inputs = %v, want immutable SHA with full ancestry", checkout.With)
	}
	identityIndex := sdkScenario11StepNamed(verify, "Validate immutable release identity")
	installIndex := sdkScenario11RunContaining(verify, "task sdk:install")
	if identityIndex < 0 || installIndex < 0 || identityIndex >= installIndex {
		t.Fatalf("identity gate index = %d, install index = %d; identity must run first", identityIndex, installIndex)
	}
	identity := verify.Steps[identityIndex].Run
	for _, proof := range []string{
		`^sdk/typescript/v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`,
		`test "$release_tag" = "sdk/typescript/v${manifest_version}"`,
		`test "$(git rev-parse "${release_tag}^{commit}")" = "$GITHUB_SHA"`,
		`git merge-base --is-ancestor "$GITHUB_SHA" origin/main`,
	} {
		if !strings.Contains(identity, proof) {
			t.Errorf("identity gate is missing %q", proof)
		}
	}
}

func TestSDKTypescriptRelease_Scenario11_GenerationCleanlinessGate(t *testing.T) {
	t.Parallel()

	root := sdkScenario11RepoRoot(t)
	workflow, _ := readSDKScenario11Workflow(t, sdkScenario11ReleaseWorkflow(t))
	verify := workflow.Jobs["verify"]
	cleanIndex := sdkScenario11StepNamed(verify, "Verify generated contracts and SDK lockfile are clean")
	buildIndex := sdkScenario11RunContaining(verify, "task sdk:build")
	if cleanIndex < 0 || buildIndex < 0 || cleanIndex >= buildIndex {
		t.Fatalf("generation gate index = %d, build index = %d; generation gate must run first", cleanIndex, buildIndex)
	}
	clean := verify.Steps[cleanIndex].Run
	for _, proof := range []string{
		"task generate",
		"git diff --exit-code -- contracts/gen sdk/typescript/src/gen sdk/typescript/pnpm-lock.yaml",
		"git status --porcelain --untracked-files=all -- contracts/gen sdk/typescript/src/gen sdk/typescript/pnpm-lock.yaml",
	} {
		if !strings.Contains(clean, proof) {
			t.Errorf("generation cleanliness gate is missing %q", proof)
		}
	}

	var workspace struct {
		OnlyBuiltDependencies []string `yaml:"onlyBuiltDependencies"`
	}
	readSDKScenario11YAML(t, filepath.Join(root, "sdk", "typescript", "pnpm-workspace.yaml"), &workspace)
	if !reflect.DeepEqual(workspace.OnlyBuiltDependencies, []string{"esbuild"}) {
		t.Errorf("onlyBuiltDependencies = %v, want exact esbuild allowlist", workspace.OnlyBuiltDependencies)
	}

	var manifest struct {
		License       string `json:"license"`
		PublishConfig struct {
			Access string `json:"access"`
		} `json:"publishConfig"`
		Repository struct {
			Directory string `json:"directory"`
			Type      string `json:"type"`
			URL       string `json:"url"`
		} `json:"repository"`
	}
	readSDKScenario11JSON(t, filepath.Join(root, "sdk", "typescript", "package.json"), &manifest)
	if manifest.License != "Apache-2.0" || manifest.PublishConfig.Access != "public" {
		t.Errorf("release metadata license/access = %q/%q", manifest.License, manifest.PublishConfig.Access)
	}
	wantRepository := struct {
		Directory string `json:"directory"`
		Type      string `json:"type"`
		URL       string `json:"url"`
	}{"sdk/typescript", "git", "git+https://github.com/stacklok/mecatl.git"}
	if manifest.Repository != wantRepository {
		t.Errorf("repository = %+v, want %+v", manifest.Repository, wantRepository)
	}
	packedGate := verify.Steps[sdkScenario11StepNamed(verify, "Verify packed manifest release metadata")].Run
	for _, proof := range []string{"package/package.json", "EXPECTED_VERSION", "Apache-2.0", "publishConfig.access", "sdk/typescript"} {
		if !strings.Contains(packedGate, proof) {
			t.Errorf("packed manifest gate is missing %q", proof)
		}
	}

	ciWorkflow := string(readSDKScenario11File(t, filepath.Join(root, ".github", "workflows", "ci.yml")))
	if !strings.Contains(ciWorkflow, "run: task sdk:audit:prod") {
		t.Error("SDK CI job lacks the production-closure vulnerability gate")
	}
}

func TestADR_0304_TrustedPublishingOnly(t *testing.T) {
	t.Parallel()

	workflow, source := readSDKScenario11Workflow(t, sdkScenario11ReleaseWorkflow(t))
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Errorf("workflow permissions = %v, want contents: read only", workflow.Permissions)
	}
	idTokenJobs := 0
	for jobName, job := range workflow.Jobs {
		if _, ok := job.Permissions["id-token"]; ok {
			idTokenJobs++
			if jobName != "publish" || job.Permissions["id-token"] != "write" {
				t.Errorf("unexpected id-token permission on %s: %q", jobName, job.Permissions["id-token"])
			}
		}
		for permission := range job.Permissions {
			switch permission {
			case "contents", "id-token":
			default:
				t.Errorf("job %s has forbidden permission %q", jobName, permission)
			}
		}
	}
	if idTokenJobs != 1 {
		t.Errorf("id-token job count = %d, want exactly one", idTokenJobs)
	}
	for _, forbidden := range []string{
		"secrets.", "NPM_TOKEN", "NODE_AUTH_TOKEN", "_authToken", "npm_config_",
		"registry-url:", "contents: write", "packages:", "attestations:",
		"pull-requests:", "issues:", "--provenance", ".npmrc",
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("release workflow contains forbidden credential/authority text %q", forbidden)
		}
	}

	for jobName, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Uses != "" && !regexp.MustCompile(`@[0-9a-f]{40}$`).MatchString(step.Uses) {
				t.Errorf("%s action is not SHA-pinned: %s", jobName, step.Uses)
			}
		}
	}
	publish := workflow.Jobs["publish"]
	downloadIndex := sdkScenario11ActionIndex(publish, "actions/download-artifact@")
	integrityIndex := sdkScenario11StepNamed(publish, "Re-verify downloaded tarball integrity")
	publishIndex := sdkScenario11StepNamed(publish, "Publish tarball and assert generated provenance")
	if downloadIndex < 0 || integrityIndex <= downloadIndex || publishIndex <= integrityIndex {
		t.Fatalf("download/integrity/publish ordering = %d/%d/%d", downloadIndex, integrityIndex, publishIndex)
	}
	publishRun := publish.Steps[publishIndex].Run
	if !strings.Contains(publishRun, `npm publish "./${TARBALL}" --access public`) {
		t.Error("publish must use the downloaded tarball path with public access")
	}
	if !strings.Contains(publishRun, "dist.attestations") {
		t.Error("publish does not assert generated dist.attestations")
	}
	setupNode := sdkScenario11Action(publish, "actions/setup-node@")
	if setupNode == nil || setupNode.With["node-version"] != "24.7.0" {
		t.Errorf("publish Node pin = %v, want exact 24.7.0 distribution", setupNode)
	}
	floorRun := publish.Steps[sdkScenario11StepNamed(publish, "Assert trusted-publishing toolchain floors")].Run
	if !strings.Contains(floorRun, "22.14.0") || !strings.Contains(floorRun, `test "$(npm --version)" = "11.5.1"`) {
		t.Error("publish does not assert the Node and npm trusted-publishing floors")
	}
}

func TestADR_0304_TagTriggerIsolation(t *testing.T) {
	t.Parallel()

	root := sdkScenario11RepoRoot(t)
	sdkWorkflow, _ := readSDKScenario11Workflow(t, sdkScenario11ReleaseWorkflow(t))
	rootWorkflow, _ := readSDKScenario11Workflow(t, filepath.Join(root, ".github", "workflows", "release.yml"))
	if !reflect.DeepEqual(sdkWorkflow.On.Push.Tags, []string{"sdk/typescript/v*"}) {
		t.Fatalf("SDK push tags = %v, want exact path-qualified pattern", sdkWorkflow.On.Push.Tags)
	}
	if !reflect.DeepEqual(rootWorkflow.On.Push.Tags, []string{"v*"}) {
		t.Fatalf("root push tags = %v, want exact root pattern", rootWorkflow.On.Push.Tags)
	}
	for _, pattern := range append(append([]string{}, sdkWorkflow.On.Push.Tags...), rootWorkflow.On.Push.Tags...) {
		if strings.Contains(pattern, "**") {
			t.Errorf("tag pattern %q uses recursive globbing", pattern)
		}
	}

	tests := []struct {
		tag       string
		sdk, root bool
	}{
		{"sdk/typescript/v0.1.0", true, false},
		{"v0.1.0", false, true},
		{"sdk/typescript/v9.9.9", true, false},
		{"v1.2.3", false, true},
	}
	for _, test := range tests {
		if got := githubSingleStarTagMatch(t, sdkWorkflow.On.Push.Tags[0], test.tag); got != test.sdk {
			t.Errorf("SDK matcher(%q) = %v, want %v", test.tag, got, test.sdk)
		}
		if got := githubSingleStarTagMatch(t, rootWorkflow.On.Push.Tags[0], test.tag); got != test.root {
			t.Errorf("root matcher(%q) = %v, want %v", test.tag, got, test.root)
		}
	}
}

func TestSDKTypescriptRelease_Scenario11_WorkflowBounding(t *testing.T) {
	t.Parallel()

	workflow, source := readSDKScenario11Workflow(t, sdkScenario11ReleaseWorkflow(t))
	if workflow.Concurrency.Group != "release-sdk-typescript-${{ github.ref }}" {
		t.Errorf("concurrency group = %q", workflow.Concurrency.Group)
	}
	if workflow.Concurrency.CancelInProgress == nil || *workflow.Concurrency.CancelInProgress {
		t.Error("release concurrency must declare cancel-in-progress: false")
	}
	for name, job := range workflow.Jobs {
		if job.RunsOn != "ubuntu-24.04" {
			t.Errorf("job %s runs-on = %q, want ubuntu-24.04", name, job.RunsOn)
		}
		if job.TimeoutMinutes <= 0 {
			t.Errorf("job %s lacks a positive timeout-minutes", name)
		}
	}
	if strings.Contains(source, "ubuntu-latest") {
		t.Error("release workflow must never use ubuntu-latest")
	}
}

func TestSDKTypescriptRelease_Scenario11_PackedLicenseProvenance(t *testing.T) {
	t.Parallel()

	root := sdkScenario11RepoRoot(t)
	workflow, _ := readSDKScenario11Workflow(t, sdkScenario11ReleaseWorkflow(t))
	verify := workflow.Jobs["verify"]
	inventoryIndex := sdkScenario11StepNamed(verify, "Inspect exact packed inventory and consumers")
	provenanceIndex := sdkScenario11StepNamed(verify, "Verify packed license provenance")
	uploadIndex := sdkScenario11ActionIndex(verify, "actions/upload-artifact@")
	if inventoryIndex < 0 || provenanceIndex <= inventoryIndex || uploadIndex <= provenanceIndex {
		t.Fatalf("inventory/provenance/upload ordering = %d/%d/%d", inventoryIndex, provenanceIndex, uploadIndex)
	}
	provenance := verify.Steps[provenanceIndex].Run
	for _, proof := range []string{
		"git grep -n 'SPDX-License-Identifier:'",
		"contracts/proto/mecatl/v1",
		"sdk/typescript/src",
		"SPDX-License-Identifier: Apache-2.0",
	} {
		if !strings.Contains(provenance, proof) {
			t.Errorf("license provenance gate is missing %q", proof)
		}
	}

	var blockers []string
	collect := func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || (filepath.Ext(file) != ".ts" && filepath.Ext(file) != ".proto") {
			return nil
		}
		contents := string(readSDKScenario11File(t, file))
		for _, line := range strings.Split(contents, "\n") {
			if strings.Contains(line, "SPDX-License-Identifier:") && !strings.Contains(line, "SPDX-License-Identifier: Apache-2.0") {
				rel, relErr := filepath.Rel(root, file)
				if relErr != nil {
					t.Fatalf("relativize blocker: %v", relErr)
				}
				blockers = append(blockers, filepath.ToSlash(rel))
			}
		}
		return nil
	}
	for _, sourceRoot := range []string{
		filepath.Join(root, "contracts", "proto", "mecatl", "v1"),
		filepath.Join(root, "sdk", "typescript", "src", "gen"),
	} {
		if err := filepath.WalkDir(sourceRoot, collect); err != nil {
			t.Fatalf("walk provenance root %s: %v", sourceRoot, err)
		}
	}
	sort.Strings(blockers)
	want := []string{
		"contracts/proto/mecatl/v1/harness.proto",
		"contracts/proto/mecatl/v1/local_session_context.proto",
		"contracts/proto/mecatl/v1/schedule.proto",
		"sdk/typescript/src/gen/mecatl/v1/harness_pb.ts",
		"sdk/typescript/src/gen/mecatl/v1/local_session_context_pb.ts",
		"sdk/typescript/src/gen/mecatl/v1/schedule_pb.ts",
	}
	if !reflect.DeepEqual(blockers, want) {
		t.Errorf("current non-Apache packed provenance blockers = %v, want %v", blockers, want)
	}
}

// path.Match models GitHub's load-bearing tag rule here: a single '*' does not
// consume '/', while literal slash-separated path segments must match exactly.
func githubSingleStarTagMatch(t *testing.T, pattern, tag string) bool {
	t.Helper()
	matched, err := path.Match(pattern, tag)
	if err != nil {
		t.Fatalf("invalid tag pattern %q: %v", pattern, err)
	}
	return matched
}

func sdkScenario11RepoRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
}

func sdkScenario11ReleaseWorkflow(t *testing.T) string {
	t.Helper()
	return filepath.Join(sdkScenario11RepoRoot(t), ".github", "workflows", "release-sdk-typescript.yml")
}

func readSDKScenario11Workflow(t *testing.T, file string) (sdkScenario11Workflow, string) {
	t.Helper()
	source := string(readSDKScenario11File(t, file))
	var workflow sdkScenario11Workflow
	if err := yaml.Unmarshal([]byte(source), &workflow); err != nil {
		t.Fatalf("parse workflow %s: %v", file, err)
	}
	return workflow, source
}

func readSDKScenario11YAML(t *testing.T, file string, target any) {
	t.Helper()
	if err := yaml.Unmarshal(readSDKScenario11File(t, file), target); err != nil {
		t.Fatalf("parse YAML %s: %v", file, err)
	}
}

func readSDKScenario11JSON(t *testing.T, file string, target any) {
	t.Helper()
	if err := json.Unmarshal(readSDKScenario11File(t, file), target); err != nil {
		t.Fatalf("parse JSON %s: %v", file, err)
	}
}

func readSDKScenario11File(t *testing.T, file string) []byte {
	t.Helper()
	contents, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	return contents
}

func sortedSDKScenario11Keys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sdkScenario11StepNamed(job sdkScenario11Job, name string) int {
	for i, step := range job.Steps {
		if step.Name == name {
			return i
		}
	}
	return -1
}

func sdkScenario11RunContaining(job sdkScenario11Job, fragment string) int {
	for i, step := range job.Steps {
		if strings.Contains(step.Run, fragment) {
			return i
		}
	}
	return -1
}

func sdkScenario11Action(job sdkScenario11Job, prefix string) *sdkScenario11Step {
	index := sdkScenario11ActionIndex(job, prefix)
	if index < 0 {
		return nil
	}
	return &job.Steps[index]
}

func sdkScenario11ActionIndex(job sdkScenario11Job, prefix string) int {
	for i, step := range job.Steps {
		if strings.HasPrefix(step.Uses, prefix) {
			return i
		}
	}
	return -1
}
