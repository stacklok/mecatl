package lint

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var movedADRSlugs = []string{
	"0077-resume-a-failed-subagent.md",
	"0090-background-bash.md",
	"0096-diagnostic-only-posture-reporting.md",
	"0097-permanent-provider-error-signal.md",
	"0100-caller-identity-threading.md",
	"0101-bounded-jwks-staleness.md",
	"0103-oidc-authn-module.md",
	"0104-context-window-overrides.md",
	"0104-execution-environment.md",
	"0104-schedule-origin-run-context.md",
	"0104-openrouter-downstream-provider-steering.md",
	"0105-execution-environment-runtime-seam.md",
	"0102-caller-ownership-enforcement.md",
	"0103-driver-caller-ownership.md",
	"0106-environment-persistence.md",
	"0104-openai-subscription-manual-token.md",
	"0110-provider-session-correlation-header.md",
	"0108-session-discovery-continuation.md",
	"0108-credential-store.md",
	"0109-mcp-oauth-sdk-profile.md",
	"0110-mcp-oauth-controller.md",
	"0111-read-only-credential-source.md",
	"0108-mecatui-ask-args-view.md",
	"0114-mcp-sdk-transport-error-semantics.md",
}

// TestNoStaleMovedADRSlugs guards only the exact paths moved during the ADR
// number repair. Bare historical number references are intentionally outside
// this gate because their meaning cannot be inferred reliably.
func findMovedADRSlugs(root, thisFile string) ([]string, error) {
	var findings []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".scratch", ".worktrees", "node_modules", "build", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if path == thisFile || !entry.Type().IsRegular() {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		for _, slug := range movedADRSlugs {
			if strings.Contains(content, slug) {
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				findings = append(findings, rel+": "+slug)
			}
		}
		return nil
	})
	sort.Strings(findings)
	return findings, err
}

func TestNoStaleMovedADRSlugs(t *testing.T) {
	root := repoRoot(t)
	findings, err := findMovedADRSlugs(root, filepath.Join(root, "docs", "lint", "old_adr_slugs_test.go"))
	if err != nil {
		t.Fatalf("scan repository for stale moved ADR slugs: %v", err)
	}
	if len(findings) > 0 {
		t.Fatalf("stale moved ADR paths remain:\n  %s", strings.Join(findings, "\n  "))
	}
}

func TestMovedADRSlugScanSkipsNestedWorktreesOnly(t *testing.T) {
	root := t.TempDir()
	slug := movedADRSlugs[0]
	for name, content := range map[string]string{
		filepath.Join(".worktrees", "poison", "doc.md"): slug,
		"ordinary.md": slug,
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	findings, err := findMovedADRSlugs(root, "")
	if err != nil {
		t.Fatal(err)
	}
	want := "ordinary.md: " + slug
	if len(findings) != 1 || findings[0] != want {
		t.Fatalf("findings = %v, want [%s]", findings, want)
	}
}
