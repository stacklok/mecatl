package server

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func typescriptAPIReportDeclarations(t *testing.T) []map[string]struct{} {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate TypeScript API reports")
	}
	root := filepath.Join(filepath.Dir(filename), "..", "..", "..", "sdk", "typescript", "etc")
	var reports []map[string]struct{}
	for _, name := range []string{"mecatl-sdk.api.md", "mecatl-sdk-node.api.md"} {
		file, err := os.Open(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("open TypeScript API report %s: %v", name, err)
		}
		declarations := make(map[string]struct{})
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "create(") || strings.HasPrefix(line, "run(") || strings.HasPrefix(line, "debugTargetSessionId?") || strings.HasPrefix(line, "sourceSessionId?") {
				declarations[line] = struct{}{}
			}
		}
		if err := scanner.Err(); err != nil {
			_ = file.Close()
			t.Fatalf("scan TypeScript API report %s: %v", name, err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close TypeScript API report %s: %v", name, err)
		}
		reports = append(reports, declarations)
	}
	return reports
}

func requireAPIReportDeclarations(t *testing.T, declarations map[string]struct{}, expected ...string) {
	t.Helper()
	for _, declaration := range expected {
		if _, ok := declarations[declaration]; !ok {
			t.Errorf("TypeScript API report is missing declaration %q", declaration)
		}
	}
}

// The behavior is exercised by the same-named Vitest. This Go pin deliberately
// reads API Extractor's generated contract rather than mirroring TypeScript source.
func TestADR_0291_TypeScriptRawHelperCompatibility(t *testing.T) {
	t.Parallel()
	for _, declarations := range typescriptAPIReportDeclarations(t) {
		requireAPIReportDeclarations(t, declarations,
			`export const SESSION_ID_HEADER_NAME = "X-Mecatl-Session-ID";`,
			"export function withSessionAffinity(sessionId: string, options?: CallOptions): CallOptions;",
		)
	}
}

// The behavior is exercised by the same-named Vitest. This pin keeps ac-trace
// attached to the generated public API contract, not implementation text.
func TestSessionAffinityAndHandoff_Scenario4_TypeScriptHighLevelPropagation(t *testing.T) {
	t.Parallel()
	for _, declarations := range typescriptAPIReportDeclarations(t) {
		requireAPIReportDeclarations(t, declarations,
			"debugTargetSessionId?: string;",
			"sourceSessionId?: string;",
			"create(options: CreateSessionOptions): Promise<Session>;",
			"run(prompt: PromptInput, options?: RunOptions): Promise<Run>;",
		)
	}
}
