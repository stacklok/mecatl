package server

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func typescriptAPIReports(t *testing.T) []string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate TypeScript API reports")
	}
	repoRoot := filepath.Join(filepath.Dir(filename), "..", "..", "..")
	return []string{
		readParitySource(t, filepath.Join(repoRoot, "sdk", "typescript", "etc", "mecatl-sdk.api.md")),
		readParitySource(t, filepath.Join(repoRoot, "sdk", "typescript", "etc", "mecatl-sdk-node.api.md")),
	}
}

func TestADR_0290_TypeScriptRawHelperCompatibility(t *testing.T) {
	t.Parallel()
	for _, report := range typescriptAPIReports(t) {
		for _, declaration := range []string{
			`export const SESSION_ID_HEADER_NAME = "X-Mecatl-Session-ID";`,
			"export function withSessionAffinity(sessionId: string, options?: CallOptions): CallOptions;",
		} {
			if !strings.Contains(report, declaration) {
				t.Errorf("TypeScript API report is missing additive affinity declaration %q", declaration)
			}
		}
	}
}

func TestSessionAffinityAndHandoff_Scenario4_TypeScriptHighLevelPropagation(t *testing.T) {
	t.Parallel()
	for _, report := range typescriptAPIReports(t) {
		for _, declaration := range []string{
			"debugTargetSessionId?: string;",
			"sourceSessionId?: string;",
			"create(options: CreateSessionOptions): Promise<Session>;",
			"run(prompt: PromptInput, options?: RunOptions): Promise<Run>;",
		} {
			if !strings.Contains(report, declaration) {
				t.Errorf("TypeScript high-level API report is missing session-bound surface %q", declaration)
			}
		}
	}
}
