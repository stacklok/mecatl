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

func TestSDKTypescriptCore_Scenario3_ErrorCodeParity(t *testing.T) {
	t.Parallel()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate parity test source")
	}
	manifestPath := filepath.Join(filepath.Dir(filename), "..", "..", "..", "sdk", "typescript", "src", "errors.ts")
	typed := parseTypescriptStringManifest(
		t,
		manifestPath,
		"// BEGIN MECATL_ERROR_CODES",
		"// END MECATL_ERROR_CODES",
		regexp.MustCompile(`^[a-z0-9_]+$`),
	)

	wire := map[string]struct{}{genericErrorEntry.Code: {}}
	for _, entry := range errorRegistry {
		wire[entry.Code] = struct{}{}
	}
	for _, entry := range httpStatusEntries {
		wire[entry.Code] = struct{}{}
	}

	missing := setDifference(wire, typed)
	extra := setDifference(typed, wire)
	if len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("Go/TypeScript error-code drift: missing in TypeScript=%v extra in TypeScript=%v", missing, extra)
	}
}

func parseTypescriptStringManifest(t *testing.T, path, begin, end string, valid *regexp.Regexp) map[string]struct{} {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read TypeScript manifest: %v", err)
	}
	text := string(source)
	start := strings.Index(text, begin)
	finish := strings.Index(text, end)
	if start < 0 || finish <= start {
		t.Fatalf("%s must contain the trivially parseable %s / %s manifest", path, begin, end)
	}
	line := regexp.MustCompile(`(?m)^  "([^"]+)",$`)
	matches := line.FindAllStringSubmatch(text[start:finish], -1)
	values := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		value := match[1]
		if !valid.MatchString(value) {
			t.Fatalf("%s contains invalid manifest value %q", path, value)
		}
		if _, duplicate := values[value]; duplicate {
			t.Fatalf("%s contains duplicate manifest value %q", path, value)
		}
		values[value] = struct{}{}
	}
	if len(values) == 0 {
		t.Fatalf("%s manifest is empty", path)
	}
	return values
}

func setDifference(left, right map[string]struct{}) []string {
	var difference []string
	for value := range left {
		if _, ok := right[value]; !ok {
			difference = append(difference, value)
		}
	}
	sort.Strings(difference)
	return difference
}
