package server

import (
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

func TestSDKServerDiscovery_Scenario4_ConstantRegistryParity(t *testing.T) {
	t.Parallel()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate server-discovery parity test source")
	}
	serverDir := filepath.Dir(filename)
	repoRoot := filepath.Join(serverDir, "..", "..", "..")
	typescript := readParitySource(t, filepath.Join(repoRoot, "sdk", "typescript", "src", "server.ts"))
	deploymentSource := readParitySource(t, filepath.Join(repoRoot, "cmd", "mecated", "deploymentid.go"))
	typescriptDeploymentLimit := singleSourceValue(
		t,
		typescript,
		regexp.MustCompile(`(?m)^const MAX_DEPLOYMENT_ID_BYTES = ([0-9]+);$`),
		"TypeScript deployment byte limit",
	)
	goDeploymentLimit := singleSourceValue(
		t,
		deploymentSource,
		regexp.MustCompile(`(?m)^const maxDeploymentIDLen = ([0-9]+)$`),
		"Go deployment byte limit",
	)
	if typescriptDeploymentLimit != goDeploymentLimit {
		t.Fatalf("TypeScript deployment byte limit = %s, want Go maxDeploymentIDLen %s", typescriptDeploymentLimit, goDeploymentLimit)
	}

	typedFeatures := sourceCaptureSet(
		t,
		sourceBetween(t, typescript, "export const ServerFeature = {", "} as const;"),
		regexp.MustCompile(`(?m)^  [A-Za-z][A-Za-z0-9]*: "([a-z0-9_]+)",$`),
		"TypeScript server features",
	)
	wireFeatures := make(map[string]struct{}, len(allFeatures))
	for _, feature := range allFeatures {
		wireFeatures[feature] = struct{}{}
	}
	assertParitySets(t, "Go/TypeScript server-feature registry", wireFeatures, typedFeatures)

	typedPostures := sourceCaptureSet(
		t,
		sourceBetween(t, typescript, "export const ServerPosture = {", "} as const;"),
		regexp.MustCompile(`(?m)^  [A-Za-z][A-Za-z0-9]*: "([a-z]+)",$`),
		"TypeScript server postures",
	)
	postureSource := readParitySource(t, filepath.Join(repoRoot, "internal", "app", "posture.go"))
	wirePostures := sourceCaptureSet(
		t,
		sourceBetween(t, postureSource, "func (p Posture) String() string {", "\n}"),
		regexp.MustCompile(`return "([a-z]+)"`),
		"Go server postures",
	)
	assertParitySets(t, "Go/TypeScript server-posture registry", wirePostures, typedPostures)
}
