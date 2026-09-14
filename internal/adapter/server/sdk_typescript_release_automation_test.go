package server

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestADR_0328_SDKReleasePRAndTagAutomation(t *testing.T) {
	t.Parallel()

	root := sdkScenario11RepoRoot(t)
	version := strings.TrimSpace(string(readSDKScenario11File(t, filepath.Join(root, "sdk", "typescript", "VERSION"))))
	var manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	readSDKScenario11JSON(t, filepath.Join(root, "sdk", "typescript", "package.json"), &manifest)
	if manifest.Name != "@stacklok-oss/mecatl-sdk" || manifest.Version != version {
		t.Fatalf("SDK identity/version = %s@%s, VERSION = %s", manifest.Name, manifest.Version, version)
	}

	prWorkflow, prSource := readSDKScenario11Workflow(t,
		filepath.Join(root, ".github", "workflows", "create-sdk-typescript-release-pr.yml"))
	tagWorkflow, tagSource := readSDKScenario11Workflow(t,
		filepath.Join(root, ".github", "workflows", "create-sdk-typescript-release-tag.yml"))

	if !reflect.DeepEqual(prWorkflow.Permissions, map[string]string{"contents": "read"}) ||
		!reflect.DeepEqual(tagWorkflow.Permissions, map[string]string{"contents": "read"}) {
		t.Error("SDK release automation must default to contents: read")
	}
	prJob := prWorkflow.Jobs["release-pr"]
	if prJob.If != "github.ref == 'refs/heads/main'" || prJob.Environment != "release" {
		t.Errorf("release PR branch/environment gate = %q/%q", prJob.If, prJob.Environment)
	}
	if !reflect.DeepEqual(prJob.Permissions, map[string]string{
		"contents": "write", "pull-requests": "read",
	}) {
		t.Errorf("release PR job permissions = %v", prJob.Permissions)
	}

	for _, proof := range []string{
		"workflow_dispatch:", "options: [patch, minor, major]",
		"release/sdk-typescript/v", "sdk/typescript/VERSION", "sdk/typescript/package.json",
		"SDK VERSION and package.json identity do not agree", "git diff --name-only | sort",
		"actions/create-github-app-token@bcd2ba49218906704ab6c1aa796996da409d3eb1",
		"vars.RELEASE_APP_CLIENT_ID", "secrets.RELEASE_APP_PRIVATE_KEY", "gh pr create",
	} {
		if !strings.Contains(prSource, proof) {
			t.Errorf("release PR workflow is missing %q", proof)
		}
	}
	prTokenIndex := sdkScenario11ActionIndex(prJob, "actions/create-github-app-token@")
	prPushIndex := sdkScenario11StepNamed(prJob, "Push the version bump and open the PR")
	if prTokenIndex < 0 || prPushIndex <= prTokenIndex {
		t.Fatalf("release PR App-token/push order = %d/%d", prTokenIndex, prPushIndex)
	}
	prPush := prJob.Steps[prPushIndex]
	if prPush.Env["GH_TOKEN"] != "${{ steps.app-token.outputs.token }}" ||
		!strings.Contains(prPush.Run, `git push "https://x-access-token:${GH_TOKEN}`) {
		t.Error("release PR branch and PR must be created with the release App token")
	}

	if !reflect.DeepEqual(tagWorkflow.On.Push.Branches, []string{"main"}) ||
		!reflect.DeepEqual(tagWorkflow.On.Push.Paths, []string{"sdk/typescript/VERSION"}) {
		t.Errorf("release tag trigger = branches %v, paths %v",
			tagWorkflow.On.Push.Branches, tagWorkflow.On.Push.Paths)
	}
	tagJob := tagWorkflow.Jobs["create-tag"]
	if tagJob.If != "github.ref == 'refs/heads/main'" || tagJob.Environment != "release" {
		t.Errorf("release tag branch/environment gate = %q/%q", tagJob.If, tagJob.Environment)
	}
	if !reflect.DeepEqual(tagJob.Permissions, map[string]string{
		"contents": "read", "pull-requests": "read",
	}) {
		t.Errorf("release tag job permissions = %v", tagJob.Permissions)
	}
	for _, proof := range []string{
		"sdk/typescript/v${version}", "release/sdk-typescript/v${version}",
		`.user.type == "Bot"`, "git diff --name-only", "sdk/typescript/package.json",
		"previous_tag", "git merge-base --is-ancestor", "should_tag=true",
		"actions/create-github-app-token@bcd2ba49218906704ab6c1aa796996da409d3eb1",
		"release-sdk-typescript.yml",
	} {
		if !strings.Contains(tagSource, proof) {
			t.Errorf("release tag workflow is missing %q", proof)
		}
	}
	decideIndex := sdkScenario11StepNamed(tagJob, "Decide whether to tag")
	tagTokenIndex := sdkScenario11ActionIndex(tagJob, "actions/create-github-app-token@")
	tagPushIndex := sdkScenario11StepNamed(tagJob, "Create and push the SDK tag")
	if decideIndex < 0 || tagTokenIndex <= decideIndex || tagPushIndex <= tagTokenIndex {
		t.Fatalf("release tag decision/App-token/push order = %d/%d/%d",
			decideIndex, tagTokenIndex, tagPushIndex)
	}
	tagPush := tagJob.Steps[tagPushIndex]
	if tagPush.Env["GH_TOKEN"] != "${{ steps.app-token.outputs.token }}" ||
		!strings.Contains(tagPush.Run, `git push "https://x-access-token:${GH_TOKEN}`) {
		t.Error("SDK tag must be pushed with the release App token")
	}

	for name, source := range map[string]string{"release PR": prSource, "release tag": tagSource} {
		if strings.Contains(source, "pull_request_target:") {
			t.Errorf("%s workflow must not use pull_request_target", name)
		}
	}
}
