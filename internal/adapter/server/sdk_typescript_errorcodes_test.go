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
	source, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read TypeScript error-code manifest: %v", err)
	}

	const begin = "// BEGIN MECATL_ERROR_CODES"
	const end = "// END MECATL_ERROR_CODES"
	text := string(source)
	start := strings.Index(text, begin)
	finish := strings.Index(text, end)
	if start < 0 || finish <= start {
		t.Fatalf("%s must contain the trivially parseable %s / %s manifest", manifestPath, begin, end)
	}
	line := regexp.MustCompile(`(?m)^  "([a-z0-9_]+)",$`)
	matches := line.FindAllStringSubmatch(text[start:finish], -1)
	typed := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		typed[match[1]] = struct{}{}
	}

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
