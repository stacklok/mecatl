package server

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var sdkTypescriptAttachFilterDivergences = map[string]string{
	"user_prompt": "the SDK filters every user prompt because reproducing the server's fenced scheduled-delivery-note heuristic client-side would duplicate a security-sensitive framing parser; includeLogOnly restores it",
}

func TestSDKTypescriptAttach_Scenario1_WatchPhaseParity(t *testing.T) {
	t.Parallel()

	paths := sdkTypescriptAttachParityPaths(t)
	typed := parseTypescriptStringManifest(
		t,
		paths.watchManifest,
		"// BEGIN MECATL_WATCH_PHASES",
		"// END MECATL_WATCH_PHASES",
		regexp.MustCompile(`^[a-z0-9_]+$`),
	)
	wire := constantsByValue(t, paths.watchSource, regexp.MustCompile(`(?m)^\s*WatchPhase[A-Za-z0-9]+\s*=\s*"([a-z0-9_]+)"`))

	missing := setDifference(wire, typed)
	extra := setDifference(typed, wire)
	if len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("Go/TypeScript watch-phase drift: missing in TypeScript=%v extra in TypeScript=%v", missing, extra)
	}
}

func TestSDKTypescriptAttach_Scenario1_WatchFeatureIdParity(t *testing.T) {
	t.Parallel()

	paths := sdkTypescriptAttachParityPaths(t)
	typescript := readParitySource(t, paths.watchManifest)
	server := readParitySource(t, paths.featureSource)
	typed := singleSourceValue(t, typescript, regexp.MustCompile(`(?m)^const watchSessionEventsFeature = "([a-z0-9_]+)";$`), "TypeScript watch feature")
	if strings.Contains(typescript, "WATCH_SESSION_EVENTS_FEATURE") {
		t.Fatal("deprecated WATCH_SESSION_EVENTS_FEATURE alias remains")
	}
	wire := singleSourceValue(t, server, regexp.MustCompile(`(?m)^\s*FeatureWatchSessionEvents = "([a-z0-9_]+)"$`), "server watch feature")
	if typed != wire {
		t.Fatalf("Go/TypeScript watch-feature drift: TypeScript=%q Go=%q", typed, wire)
	}
}

func TestSDKTypescriptAttach_Scenario1_FilteredKindParity(t *testing.T) {
	t.Parallel()

	paths := sdkTypescriptAttachParityPaths(t)
	typed := parseTypescriptStringManifest(
		t,
		paths.watchManifest,
		"// BEGIN MECATL_ATTACH_FILTERED_KINDS",
		"// END MECATL_ATTACH_FILTERED_KINDS",
		regexp.MustCompile(`^[a-z0-9_.]+$`),
	)
	byName, _ := sessionEventKinds(t, paths.eventSource)
	grpcSource := readParitySource(t, paths.grpcSource)

	serverSkippedNames := make(map[string]struct{})
	publicBody := sourceBetween(t, grpcSource, "func isPublicEvent", "\n}")
	for _, match := range regexp.MustCompile(`ev\.Type != session\.(Ev[A-Za-z0-9]+)`).FindAllStringSubmatch(publicBody, -1) {
		serverSkippedNames[match[1]] = struct{}{}
	}

	relayBody := sourceBetween(t, grpcSource, "func relayLiveEvent", "\n}")
	falseCases := regexp.MustCompile(`(?s)case\s+([^:]+):\s*return false`).FindAllStringSubmatch(relayBody, -1)
	if len(falseCases) == 0 {
		t.Fatal("locate relayLiveEvent unconditional skip cases")
	}
	for _, falseCase := range falseCases {
		for _, match := range regexp.MustCompile(`session\.(Ev[A-Za-z0-9]+)`).FindAllStringSubmatch(falseCase[1], -1) {
			serverSkippedNames[match[1]] = struct{}{}
		}
	}

	serverSkipped := make(map[string]struct{}, len(serverSkippedNames))
	for name := range serverSkippedNames {
		kind, ok := byName[name]
		if !ok {
			t.Fatalf("watch filter derives unknown session event constant %s", name)
		}
		serverSkipped[kind] = struct{}{}
	}
	if len(serverSkipped) == 0 {
		t.Fatal("watch filter parity found no server-skipped kinds")
	}

	missing := setDifference(serverSkipped, typed)
	var falseClaims []string
	for kind := range typed {
		if _, ok := serverSkipped[kind]; ok {
			continue
		}
		if strings.TrimSpace(sdkTypescriptAttachFilterDivergences[kind]) == "" {
			falseClaims = append(falseClaims, kind)
		}
	}
	sort.Strings(falseClaims)
	if len(missing) != 0 || len(falseClaims) != 0 {
		t.Fatalf("server/SDK attachment-filter drift: missing in SDK=%v false SDK claims=%v", missing, falseClaims)
	}

	if len(sdkTypescriptAttachFilterDivergences) != 1 {
		t.Fatalf("expected exactly the one documented user_prompt divergence, got %d", len(sdkTypescriptAttachFilterDivergences))
	}
	for kind, reason := range sdkTypescriptAttachFilterDivergences {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("attachment-filter divergence %q has no reason", kind)
		}
		if _, ok := typed[kind]; !ok {
			t.Errorf("attachment-filter divergence %q is not claimed by the SDK manifest", kind)
		}
		if _, ok := serverSkipped[kind]; ok {
			t.Errorf("attachment-filter divergence %q is stale: the server now skips it unconditionally", kind)
		}
		if kind != "user_prompt" || !strings.Contains(relayBody, "case session.EvUserPrompt:") || !strings.Contains(relayBody, "isDeliveryNoteText") {
			t.Errorf("attachment-filter divergence %q no longer matches relayLiveEvent's delivery-note exception", kind)
		}
	}
}

type attachParityPaths struct {
	eventSource   string
	featureSource string
	grpcSource    string
	watchManifest string
	watchSource   string
}

func sdkTypescriptAttachParityPaths(t *testing.T) attachParityPaths {
	t.Helper()
	base := sdkTypescriptEventParityPaths(t)
	serverDir := filepath.Dir(base.grpcSource)
	repoRoot := filepath.Join(serverDir, "..", "..", "..")
	return attachParityPaths{
		eventSource:   base.eventSource,
		featureSource: filepath.Join(serverDir, "features.go"),
		grpcSource:    base.grpcSource,
		watchManifest: filepath.Join(repoRoot, "sdk", "typescript", "src", "watch.ts"),
		watchSource:   filepath.Join(serverDir, "watch.go"),
	}
}

func constantsByValue(t *testing.T, path string, pattern *regexp.Regexp) map[string]struct{} {
	t.Helper()
	matches := pattern.FindAllStringSubmatch(readParitySource(t, path), -1)
	if len(matches) == 0 {
		t.Fatalf("find constants in %s", path)
	}
	values := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if _, duplicate := values[match[1]]; duplicate {
			t.Fatalf("duplicate constant value %q in %s", match[1], path)
		}
		values[match[1]] = struct{}{}
	}
	return values
}

func singleSourceValue(t *testing.T, source string, pattern *regexp.Regexp, label string) string {
	t.Helper()
	matches := pattern.FindAllStringSubmatch(source, -1)
	if len(matches) != 1 {
		t.Fatalf("locate exactly one %s, got %d", label, len(matches))
	}
	return matches[0][1]
}
