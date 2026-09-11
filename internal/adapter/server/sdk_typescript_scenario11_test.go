package server

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
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
	Env            map[string]string   `yaml:"env"`
	Environment    string              `yaml:"environment"`
	If             string              `yaml:"if"`
	Needs          sdkScenario11Needs  `yaml:"needs"`
	Outputs        map[string]string   `yaml:"outputs"`
	Permissions    map[string]string   `yaml:"permissions"`
	RunsOn         string              `yaml:"runs-on"`
	Steps          []sdkScenario11Step `yaml:"steps"`
	TimeoutMinutes int                 `yaml:"timeout-minutes"`
}

// sdkScenario11Needs models a job's `needs:`, which GitHub accepts as either a single job id
// or a sequence of them. Typing it as a bare string parses the SDK release workflow but fails
// outright on release.yml's `needs: [guard, publish-mecak8s]`.
type sdkScenario11Needs []string

func (n *sdkScenario11Needs) UnmarshalYAML(b []byte) error {
	var one string
	if err := yaml.Unmarshal(b, &one); err == nil {
		*n = sdkScenario11Needs{one}
		return nil
	}
	var many []string
	if err := yaml.Unmarshal(b, &many); err != nil {
		return err
	}
	*n = many
	return nil
}

type sdkScenario11Step struct {
	Env  map[string]string `yaml:"env"`
	ID   string            `yaml:"id"`
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
	if !reflect.DeepEqual([]string(publish.Needs), []string{"verify"}) {
		t.Errorf("publish needs = %v, want [verify]", publish.Needs)
	}
	if publish.Environment != "npm-publish" {
		t.Errorf("publish environment = %q, want npm-publish", publish.Environment)
	}
	const publishCondition = "github.event_name == 'push' && startsWith(github.ref, 'refs/tags/sdk/typescript/v')"
	if publish.If != publishCondition {
		t.Errorf("publish if = %q, want %q", publish.If, publishCondition)
	}
	wantPublishPermissions := map[string]string{
		"contents": "read",
		"id-token": "write",
	}
	if !reflect.DeepEqual(publish.Permissions, wantPublishPermissions) {
		t.Errorf("publish permissions = %v, want %v", publish.Permissions, wantPublishPermissions)
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
	exerciseSDKScenario11IdentityGate(t, identity)
}

func exerciseSDKScenario11IdentityGate(t *testing.T, script string) {
	t.Helper()

	repo := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	runGit("init", "--initial-branch=main")
	runGit("config", "user.name", "Scenario 11 fixture")
	runGit("config", "user.email", "scenario11@example.invalid")
	manifestPath := filepath.Join(repo, "sdk", "typescript", "package.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatalf("create fixture package directory: %v", err)
	}
	writeManifest := func(version string) {
		t.Helper()
		contents := []byte(`{"version":"` + version + `"}` + "\n")
		if err := os.WriteFile(manifestPath, contents, 0o644); err != nil {
			t.Fatalf("write fixture manifest: %v", err)
		}
	}
	writeManifest("0.0.1")
	runGit("add", "sdk/typescript/package.json")
	runGit("commit", "-m", "fixture main")
	mainSHA := runGit("rev-parse", "HEAD")
	runGit("update-ref", "refs/remotes/origin/main", mainSHA)
	runGit("tag", "sdk/typescript/v0.0.1", mainSHA)
	runGit("tag", "sdk/typescript/v0.0.9", mainSHA)

	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create fixture bin directory: %v", err)
	}
	fakeNode := filepath.Join(binDir, "node")
	if err := os.WriteFile(fakeNode, []byte("#!/bin/sh\nprintf '%s\\n' \"$NODE_MANIFEST_VERSION\"\n"), 0o755); err != nil {
		t.Fatalf("write fake node: %v", err)
	}

	runIdentity := func(name, eventName, ref, sha, manifestVersion string, wantSuccess bool) {
		t.Helper()
		outputFile := filepath.Join(t.TempDir(), "github-output")
		cmd := exec.Command("bash", "-c", script)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"EVENT_NAME="+eventName,
			"GITHUB_REF="+ref,
			"GITHUB_SHA="+sha,
			"GITHUB_OUTPUT="+outputFile,
			"NODE_MANIFEST_VERSION="+manifestVersion,
		)
		output, err := cmd.CombinedOutput()
		if wantSuccess && err != nil {
			t.Fatalf("%s: identity gate failed: %v\n%s", name, err, output)
		}
		if !wantSuccess && err == nil {
			t.Fatalf("%s: identity gate unexpectedly succeeded\n%s", name, output)
		}
		if !wantSuccess {
			return
		}
		got := strings.TrimSpace(string(readSDKScenario11File(t, outputFile)))
		if want := "version=" + manifestVersion; got != want {
			t.Fatalf("%s: identity output = %q, want %q", name, got, want)
		}
	}

	runIdentity("valid release", "push", "refs/tags/sdk/typescript/v0.0.1", mainSHA, "0.0.1", true)
	runIdentity("prerelease tag", "push", "refs/tags/sdk/typescript/v0.0.1-rc.1", mainSHA, "0.0.1", false)
	runIdentity("tag/version mismatch", "push", "refs/tags/sdk/typescript/v0.0.9", mainSHA, "0.0.1", false)

	writeManifest("0.0.2")
	runGit("add", "sdk/typescript/package.json")
	runGit("commit", "-m", "fixture unmerged release")
	unmergedSHA := runGit("rev-parse", "HEAD")
	runGit("tag", "sdk/typescript/v0.0.2", unmergedSHA)
	runIdentity("checkout SHA mismatch", "workflow_dispatch", "refs/heads/fixture", mainSHA, "0.0.1", false)
	runGit("checkout", "--detach", mainSHA)
	runIdentity("tag commit mismatch", "push", "refs/tags/sdk/typescript/v0.0.2", mainSHA, "0.0.2", false)
	runGit("checkout", "--detach", unmergedSHA)
	runIdentity("unmerged release", "push", "refs/tags/sdk/typescript/v0.0.2", unmergedSHA, "0.0.2", false)
	runIdentity("manual dry run", "workflow_dispatch", "refs/heads/fixture", unmergedSHA, "0.0.2", true)
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
		AllowBuilds           map[string]bool `yaml:"allowBuilds"`
		OnlyBuiltDependencies []string        `yaml:"onlyBuiltDependencies"`
	}
	readSDKScenario11YAML(t, filepath.Join(root, "sdk", "typescript", "pnpm-workspace.yaml"), &workspace)
	if !reflect.DeepEqual(workspace.AllowBuilds, map[string]bool{"esbuild": true}) {
		t.Errorf("allowBuilds = %v, want exact esbuild allowlist", workspace.AllowBuilds)
	}
	if !reflect.DeepEqual(workspace.OnlyBuiltDependencies, []string{"esbuild"}) {
		t.Errorf("onlyBuiltDependencies = %v, want exact esbuild allowlist", workspace.OnlyBuiltDependencies)
	}

	var manifest struct {
		License       string `json:"license"`
		PublishConfig struct {
			Access   *string `json:"access"`
			Registry string  `json:"registry"`
		} `json:"publishConfig"`
		Repository struct {
			Directory string `json:"directory"`
			Type      string `json:"type"`
			URL       string `json:"url"`
		} `json:"repository"`
	}
	readSDKScenario11JSON(t, filepath.Join(root, "sdk", "typescript", "package.json"), &manifest)
	if manifest.License != "Apache-2.0" || manifest.PublishConfig.Registry != "https://registry.npmjs.org" || manifest.PublishConfig.Access == nil || *manifest.PublishConfig.Access != "public" {
		t.Errorf("release metadata license/registry/access = %q/%q/%v", manifest.License, manifest.PublishConfig.Registry, manifest.PublishConfig.Access)
	}
	wantRepository := struct {
		Directory string `json:"directory"`
		Type      string `json:"type"`
		URL       string `json:"url"`
	}{"sdk/typescript", "git", "https://github.com/stacklok/mecatl.git"}
	if manifest.Repository != wantRepository {
		t.Errorf("repository = %+v, want %+v", manifest.Repository, wantRepository)
	}
	packedGate := verify.Steps[sdkScenario11StepNamed(verify, "Verify packed manifest release metadata")].Run
	for _, proof := range []string{"package/package.json", "EXPECTED_VERSION", "Apache-2.0", "publishConfig.registry", "publishConfig.access", "https://registry.npmjs.org", "public", "sdk/typescript"} {
		if !strings.Contains(packedGate, proof) {
			t.Errorf("packed manifest gate is missing %q", proof)
		}
	}

	ciWorkflow := string(readSDKScenario11File(t, filepath.Join(root, ".github", "workflows", "ci.yml")))
	if !strings.Contains(ciWorkflow, "run: task sdk:audit") {
		t.Error("SDK CI job lacks the complete dependency-closure vulnerability gate")
	}
	if sdkScenario11RunContaining(verify, "task sdk:audit") < 0 {
		t.Error("release verification does not re-run the dependency vulnerability gate")
	}
}

func TestADR_0328_TrustedPublishingNoStoredCredentials(t *testing.T) {
	t.Parallel()

	workflow, source := readSDKScenario11Workflow(t, sdkScenario11ReleaseWorkflow(t))
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Errorf("workflow permissions = %v, want contents: read only", workflow.Permissions)
	}
	publish := workflow.Jobs["publish"]
	wantPublishPermissions := map[string]string{
		"contents": "read",
		"id-token": "write",
	}
	if !reflect.DeepEqual(publish.Permissions, wantPublishPermissions) {
		t.Errorf("publish permissions = %v, want %v", publish.Permissions, wantPublishPermissions)
	}
	idTokenJobs := 0
	for jobName, job := range workflow.Jobs {
		if _, ok := job.Permissions["id-token"]; ok {
			idTokenJobs++
			if jobName != "publish" || job.Permissions["id-token"] != "write" {
				t.Errorf("unexpected id-token permission on %s: %q", jobName, job.Permissions["id-token"])
			}
		}
		if jobName != "publish" && !reflect.DeepEqual(job.Permissions, map[string]string{"contents": "read"}) {
			t.Errorf("non-publish job %s permissions = %v, want contents: read only", jobName, job.Permissions)
		}
	}
	if idTokenJobs != 1 {
		t.Errorf("id-token job count = %d, want exactly one", idTokenJobs)
	}
	if _, ok := publish.Env["NODE_AUTH_TOKEN"]; ok {
		t.Errorf("publish must not set NODE_AUTH_TOKEN, got %q", publish.Env["NODE_AUTH_TOKEN"])
	}
	if secretReferences := regexp.MustCompile(`secrets\.[A-Za-z_][A-Za-z0-9_]*`).FindAllString(source, -1); len(secretReferences) != 0 {
		t.Errorf("secret references = %v, want none", secretReferences)
	}
	for _, forbidden := range []string{
		"NPM_TOKEN", "_authToken", "npm_config_", "contents: write",
		"pull-requests:", "issues:", "--provenance", ".npmrc", "--access",
		"packages: write", "attestations: write", "actions/attest-build-provenance@",
		"gh attestation", "npm.pkg.github.com", "NODE_AUTH_TOKEN",
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
	downloadIndex := sdkScenario11ActionIndex(publish, "actions/download-artifact@")
	integrityIndex := sdkScenario11StepNamed(publish, "Re-verify downloaded tarball integrity")
	publishIndex := sdkScenario11StepNamed(publish, "Publish downloaded tarball")
	provenanceIndex := sdkScenario11StepNamed(publish, "Verify published npm provenance")
	summaryIndex := sdkScenario11StepNamed(publish, "Record published artifact evidence")
	if downloadIndex < 0 || integrityIndex <= downloadIndex || publishIndex <= integrityIndex ||
		provenanceIndex <= publishIndex || summaryIndex <= provenanceIndex {
		t.Fatalf("download/integrity/publish/provenance/summary ordering = %d/%d/%d/%d/%d",
			downloadIndex, integrityIndex, publishIndex, provenanceIndex, summaryIndex)
	}
	if sdkScenario11ActionIndex(publish, "actions/attest-build-provenance@") >= 0 {
		t.Error("publish must not mint a separate GitHub artifact attestation")
	}
	publishRun := publish.Steps[publishIndex].Run
	if !strings.Contains(publishRun, `npm publish "./${TARBALL}"`) {
		t.Error("publish must use the downloaded tarball path")
	}
	provenanceRun := publish.Steps[provenanceIndex].Run
	for _, proof := range []string{"dist.attestations", "dist.integrity", "@stacklok-oss/mecatl-sdk@", "npm", "view"} {
		if !strings.Contains(provenanceRun, proof) {
			t.Errorf("published provenance gate is missing %q", proof)
		}
	}
	summary := publish.Steps[summaryIndex]
	if summary.Env["INTEGRITY"] != "${{ needs.verify.outputs.integrity }}" ||
		!strings.Contains(summary.Run, "$GITHUB_STEP_SUMMARY") ||
		!strings.Contains(summary.Run, "dist.attestations") {
		t.Error("publish summary does not record integrity and npm provenance")
	}
	setupNode := sdkScenario11Action(publish, "actions/setup-node@")
	if setupNode == nil || setupNode.With["node-version"] != "24.7.0" {
		t.Errorf("publish Node setup = %v, want exact Node 24.7.0", setupNode)
	}
	if _, ok := setupNode.With["registry-url"]; ok {
		t.Errorf("publish must not set registry-url, got %v", setupNode.With)
	}
	floorRun := publish.Steps[sdkScenario11StepNamed(publish, "Assert npm toolchain floors")].Run
	if !strings.Contains(floorRun, "22.14.0") || !strings.Contains(floorRun, `test "$(npm --version)" = "11.5.1"`) {
		t.Error("publish does not assert the Node and npm publishing floors")
	}
	packRun := workflow.Jobs["verify"].Steps[sdkScenario11StepNamed(workflow.Jobs["verify"], "Pack SDK and record integrity")].Run
	if !strings.Contains(packRun, `stacklok-oss-mecatl-sdk-${VERSION}.tgz`) {
		t.Error("verify does not assert the scoped public tarball basename")
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
		{"sdk/typescript/v0.0.1", true, false},
		{"v0.0.1", false, true},
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
	if len(blockers) != 0 {
		t.Errorf("non-Apache packed provenance blockers = %v, want none", blockers)
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
