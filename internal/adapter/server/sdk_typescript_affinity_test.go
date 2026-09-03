package server

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSessionAffinityAndHandoff_Scenario4_TypeScriptHighLevelPropagation(t *testing.T) {
	t.Parallel()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate TypeScript affinity test source")
	}
	repoRoot := filepath.Join(filepath.Dir(filename), "..", "..", "..")
	client := readParitySource(t, filepath.Join(repoRoot, "sdk", "typescript", "src", "client.ts"))
	http := readParitySource(t, filepath.Join(repoRoot, "sdk", "typescript", "src", "http.ts"))
	behavior := readParitySource(t, filepath.Join(repoRoot, "sdk", "typescript", "test", "session-affinity.test.ts"))

	for _, want := range []string{
		"function sessionAffinityOperations(",
		"withSessionAffinity(sessionId, options)",
		"withSessionAffinity(sourceSessionId)",
		"withSessionAffinity(sessionId)",
	} {
		if !strings.Contains(client, want) {
			t.Errorf("TypeScript high-level client does not centrally bind session affinity: missing %q", want)
		}
	}
	for _, want := range []string{
		"requestHeaders?: HeadersInit",
		"this.#request(route, body, signal, requestHeaders)",
	} {
		if !strings.Contains(http, want) {
			t.Errorf("HTTP control path does not retain stream affinity: missing %q", want)
		}
	}
	if !strings.Contains(behavior, "expect(httpSeen.every(({ affinity }) => affinity === sessionId)).toBe(true)") ||
		!strings.Contains(behavior, "expect(seen.every(({ affinity }) => affinity !== run.id)).toBe(true)") {
		t.Error("behavioral transport test must prove exact session affinity on HTTP and reject run-id substitution")
	}
}
