package server

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// Every currently relay-skipped kind has a protobuf projection and is typed by
// the SDK. A future replay-inaccessible kind may be excluded only with a reason.
var sdkTypescriptLogOnlyKindExclusions = map[string]string{}

func TestSDKTypescriptCore_Scenario6_EventKindParity(t *testing.T) {
	t.Parallel()

	paths := sdkTypescriptEventParityPaths(t)
	typed := parseTypescriptStringManifest(
		t,
		paths.manifest,
		"// BEGIN MECATL_EVENT_KINDS",
		"// END MECATL_EVENT_KINDS",
		regexp.MustCompile(`^[a-z0-9_.]+$`),
	)
	_, wire := sessionEventKinds(t, paths.eventSource)

	// steer.outcome is a server-authored Converse event rather than a domain
	// session.Event. Parse production projections so this exceptional vocabulary
	// remains covered without inventing a second hand-maintained Go manifest.
	grpcSource := readParitySource(t, paths.grpcSource)
	projection := regexp.MustCompile(`Type:\s*"([a-z0-9_.]+)"`)
	for _, match := range projection.FindAllStringSubmatch(grpcSource, -1) {
		wire[match[1]] = struct{}{}
	}

	missing := setDifference(wire, typed)
	extra := setDifference(typed, wire)
	if len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("Go/TypeScript event-kind drift: missing in TypeScript=%v extra in TypeScript=%v", missing, extra)
	}
}

func TestSDKTypescriptCore_Scenario6_LogOnlyKindsAudited(t *testing.T) {
	t.Parallel()

	paths := sdkTypescriptEventParityPaths(t)
	typed := parseTypescriptStringManifest(
		t,
		paths.manifest,
		"// BEGIN MECATL_EVENT_KINDS",
		"// END MECATL_EVENT_KINDS",
		regexp.MustCompile(`^[a-z0-9_.]+$`),
	)
	byName, _ := sessionEventKinds(t, paths.eventSource)
	skippedNames := make(map[string]struct{})

	grpcSource := readParitySource(t, paths.grpcSource)
	publicBody := sourceBetween(t, grpcSource, "func isPublicEvent", "\n}")
	for _, match := range regexp.MustCompile(`ev\.Type != session\.(Ev[A-Za-z0-9]+)`).FindAllStringSubmatch(publicBody, -1) {
		skippedNames[match[1]] = struct{}{}
	}

	serviceSource := readParitySource(t, paths.serviceSource)
	skipCondition := regexp.MustCompile(`(?s)if !isPublicEvent\(ev\) \|\| (.*?)\{\s*return false`).FindStringSubmatch(serviceSource)
	if len(skipCondition) != 2 {
		t.Fatal("locate relayEvent live-wire skip condition")
	}
	for _, match := range regexp.MustCompile(`ev\.Type == session\.(Ev[A-Za-z0-9]+)`).FindAllStringSubmatch(skipCondition[1], -1) {
		skippedNames[match[1]] = struct{}{}
	}

	skipped := make(map[string]struct{}, len(skippedNames))
	for name := range skippedNames {
		kind, ok := byName[name]
		if !ok {
			t.Fatalf("relay skips unknown session event constant %s", name)
		}
		skipped[kind] = struct{}{}
	}
	if len(skipped) == 0 {
		t.Fatal("live-relay skip audit found no kinds")
	}

	var unaudited []string
	for kind := range skipped {
		if _, ok := typed[kind]; ok {
			continue
		}
		if reason := strings.TrimSpace(sdkTypescriptLogOnlyKindExclusions[kind]); reason == "" {
			unaudited = append(unaudited, kind)
		}
	}
	if len(unaudited) != 0 {
		sort.Strings(unaudited)
		t.Fatalf("relay-skipped event kinds must be typed or explicitly excluded with a reason: %v", unaudited)
	}
	for kind, reason := range sdkTypescriptLogOnlyKindExclusions {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("log-only exclusion %q has no reason", kind)
		}
		if _, ok := skipped[kind]; !ok {
			t.Errorf("log-only exclusion %q is stale: the live relay does not skip it", kind)
		}
	}
}

type eventParityPaths struct {
	eventSource   string
	grpcSource    string
	manifest      string
	serviceSource string
}

func sdkTypescriptEventParityPaths(t *testing.T) eventParityPaths {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate parity test source")
	}
	serverDir := filepath.Dir(filename)
	repoRoot := filepath.Join(serverDir, "..", "..", "..")
	return eventParityPaths{
		eventSource:   filepath.Join(repoRoot, "engine", "session", "event.go"),
		grpcSource:    filepath.Join(serverDir, "grpc.go"),
		manifest:      filepath.Join(repoRoot, "sdk", "typescript", "src", "events.ts"),
		serviceSource: filepath.Join(serverDir, "service.go"),
	}
}

func sessionEventKinds(t *testing.T, path string) (map[string]string, map[string]struct{}) {
	t.Helper()
	source := readParitySource(t, path)
	constant := regexp.MustCompile(`(?m)^\s*(Ev[A-Za-z0-9]+)\s+EventType\s+=\s+"([a-z0-9_.]+)"`)
	matches := constant.FindAllStringSubmatch(source, -1)
	if len(matches) == 0 {
		t.Fatalf("find session.EventType constants in %s", path)
	}
	byName := make(map[string]string, len(matches))
	byKind := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if _, duplicate := byKind[match[2]]; duplicate {
			t.Fatalf("duplicate session event kind %q", match[2])
		}
		byName[match[1]] = match[2]
		byKind[match[2]] = struct{}{}
	}
	return byName, byKind
}

func readParitySource(t *testing.T, path string) string {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read parity source %s: %v", path, err)
	}
	return string(source)
}

func sourceBetween(t *testing.T, source, begin, end string) string {
	t.Helper()
	start := strings.Index(source, begin)
	if start < 0 {
		t.Fatalf("source does not contain %q", begin)
	}
	finish := strings.Index(source[start:], end)
	if finish < 0 {
		t.Fatalf("source after %q does not contain %q", begin, end)
	}
	return source[start : start+finish]
}
