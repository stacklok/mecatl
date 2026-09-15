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

	alias := singleSourceValue(
		t,
		typescript,
		regexp.MustCompile(`(?m)^export const WATCH_SESSION_EVENTS_FEATURE = ServerFeature\.([A-Za-z0-9]+);$`),
		"deprecated watch-session feature alias",
	)
	if alias != "WatchSessionEvents" {
		t.Fatalf("WATCH_SESSION_EVENTS_FEATURE aliases ServerFeature.%s, want WatchSessionEvents", alias)
	}
}
