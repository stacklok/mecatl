package permconfig

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

// --- subagent block parsing + audience tagging (issue #32) --------------------

// TestSubagentBlockParseAudienceAndScope pins the D1 audience table: top-level
// deny → AudienceAll, top-level allow/ask → AudienceMain, every subagent-block
// bucket → AudienceSubagent — all at the file's tier scope.
func TestSubagentBlockParseAudienceAndScope(t *testing.T) {
	const yamlDoc = `
permissions:
  allow:
    - "Bash(go test:*)"
  ask:
    - "Bash(go vet:*)"
  deny:
    - "Bash(rm:*)"
  subagent:
    allow:
      - "Bash(cat:*)"
    ask:
      - "Bash(go build:*)"
    deny:
      - "Bash(curl:*)"
`
	cfg, err := parseYAML([]byte(yamlDoc))
	if err != nil {
		t.Fatalf("parseYAML: %v", err)
	}
	var report Report
	rules := rulesFromConfig(cfg, governance.ScopeSharedProject, &report)
	if !report.Empty() {
		t.Fatalf("unexpected lossy report: %+v", report)
	}
	want := []struct {
		pattern  string
		effect   governance.Effect
		audience governance.Audience
	}{
		{"go test*", governance.Allow, governance.AudienceMain},
		{"go vet*", governance.Ask, governance.AudienceMain},
		{"rm*", governance.Deny, governance.AudienceAll},
		{"cat*", governance.Allow, governance.AudienceSubagent},
		{"go build*", governance.Ask, governance.AudienceSubagent},
		{"curl*", governance.Deny, governance.AudienceSubagent},
	}
	for _, w := range want {
		r := findRule(rules, "Bash", w.pattern)
		if r == nil {
			t.Fatalf("rule %q missing: %+v", w.pattern, rules)
			return
		}
		if r.Effect != w.effect || r.Audience != w.audience || r.Scope != governance.ScopeSharedProject {
			t.Fatalf("rule %q = {effect %v, audience %v, scope %v}, want {%v, %v, %v}",
				w.pattern, r.Effect, r.Audience, r.Scope, w.effect, w.audience, governance.ScopeSharedProject)
		}
	}
}

// TestPermissionsStrictUnknownKeys pins the strict permissions-subtree contract:
// a typo'd key inside permissions: or permissions.subagent: is a LOUD parse
// error (caught by the per-file fail-soft), never silently-ignored config.
func TestPermissionsStrictUnknownKeys(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		frag string // expected error fragment
	}{
		{
			name: "typo'd subagent key",
			doc:  "permissions:\n  subagnet:\n    allow:\n      - \"Bash(ls)\"\n",
			frag: `unknown key "subagnet"`,
		},
		{
			name: "typo'd allow key",
			doc:  "permissions:\n  alow:\n    - \"Bash(ls)\"\n",
			frag: `unknown key "alow"`,
		},
		{
			name: "typo'd key inside the subagent block",
			doc:  "permissions:\n  subagent:\n    dany:\n      - \"Bash(rm:*)\"\n",
			frag: `permissions.subagent: unknown key "dany"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseYAML([]byte(tc.doc))
			if err == nil {
				t.Fatalf("expected a loud unknown-key error for:\n%s", tc.doc)
			}
			if !strings.Contains(err.Error(), "permissions") {
				t.Fatalf("error %q should identify the strict permissions section", err.Error())
			}
		})
	}
}

// TestPermissionsTopLevelStaysLenient pins that strictness is permissions-
// subtree-only: unknown TOP-level keys (trustedWorkspaces etc.) keep parsing.
func TestPermissionsTopLevelStaysLenient(t *testing.T) {
	const yamlDoc = `
trustedWorkspaces:
  - /some/repo
someFutureKey: true
permissions:
  allow:
    - "Bash(ls)"
`
	cfg, err := parseYAML([]byte(yamlDoc))
	if err != nil {
		t.Fatalf("top-level unknown keys must stay lenient: %v", err)
	}
	if len(cfg.Permissions.Allow) != 1 {
		t.Fatalf("permissions should still parse: %+v", cfg.Permissions)
	}
}

// TestPermissionsNullAndEmptySubtrees pins that bare `permissions:` and bare
// `subagent:` lines (null values) parse to zero values, preserving parseYAML's
// empty/comment-only early-outs.
func TestPermissionsNullAndEmptySubtrees(t *testing.T) {
	for _, doc := range []string{"permissions:\n", "permissions:\n  subagent:\n", "# only a comment\n", ""} {
		if _, err := parseYAML([]byte(doc)); err != nil {
			t.Fatalf("doc %q should parse clean: %v", doc, err)
		}
	}
}

// TestRuleCapDropOrderSafestSurvive pins the cap order (deny(all) →
// subagent-deny → ask → subagent-ask → allow → subagent-allow): at
// maxRulesPerConfig the allows are dropped FIRST and every deny/ask survives.
func TestRuleCapDropOrderSafestSurvive(t *testing.T) {
	mk := func(n int, verb string) []string {
		specs := make([]string, n)
		for i := range specs {
			specs[i] = fmt.Sprintf("Bash(%s-%d:*)", verb, i)
		}
		return specs
	}
	// Fill the whole cap with deny+ask buckets, then add allows that MUST drop.
	per := maxRulesPerConfig / 4 // 4 safe buckets fill the cap exactly
	cfg := Config{Permissions: Permissions{
		Deny:  mk(per, "deny"),
		Ask:   mk(per, "ask"),
		Allow: mk(8, "allow"),
		Subagent: SubagentPermissions{
			Deny:  mk(per, "subdeny"),
			Ask:   mk(per, "subask"),
			Allow: mk(8, "suballow"),
		},
	}}
	var report Report
	rules := rulesFromConfig(cfg, governance.ScopeUser, &report)
	if len(rules) != maxRulesPerConfig {
		t.Fatalf("expected exactly the cap (%d) rules, got %d", maxRulesPerConfig, len(rules))
	}
	for _, r := range rules {
		if r.Effect == governance.Allow {
			t.Fatalf("an allow survived the cap while denies/asks filled it: %+v", r)
		}
	}
	if len(report.Dropped) != 16 {
		t.Fatalf("expected all 16 allows dropped, got %d: %+v", len(report.Dropped), report.Dropped)
	}
	for _, d := range report.Dropped {
		if !strings.Contains(d.Spec, "allow") {
			t.Fatalf("only allows should drop at the cap; dropped %q", d.Spec)
		}
	}
}

// TestClaudeImportAudienceTagging pins the import's audience posture: Claude
// settings have no subagent block, so imported buckets tag like the mecatl
// top-level ones — deny → AudienceAll, ask/allow → AudienceMain (a demoted
// WebFetch domain allow stays AudienceMain: it came from the allow bucket).
func TestClaudeImportAudienceTagging(t *testing.T) {
	const doc = `{
  "permissions": {
    "allow": ["Bash(go test:*)", "WebFetch(domain:example.com)"],
    "ask": ["Bash(go vet:*)"],
    "deny": ["Bash(rm:*)"]
  }
}`
	var report Report
	rules, err := importClaude([]byte(doc), governance.ScopeSharedProject, &report)
	if err != nil {
		t.Fatalf("importClaude: %v", err)
	}
	checks := []struct {
		tool, pattern string
		effect        governance.Effect
		audience      governance.Audience
	}{
		{"Bash", "go test*", governance.Allow, governance.AudienceMain},
		{"WebFetch", "domain:example.com", governance.Ask, governance.AudienceMain}, // demoted, bucket = allow
		{"Bash", "go vet*", governance.Ask, governance.AudienceMain},
		{"Bash", "rm*", governance.Deny, governance.AudienceAll},
	}
	for _, c := range checks {
		r := findRule(rules, c.tool, c.pattern)
		if r == nil {
			t.Fatalf("imported rule %s(%s) missing: %+v", c.tool, c.pattern, rules)
			return
		}
		if r.Effect != c.effect || r.Audience != c.audience {
			t.Fatalf("imported %s(%s) = {%v, audience %v}, want {%v, %v}",
				c.tool, c.pattern, r.Effect, r.Audience, c.effect, c.audience)
		}
	}
}

// TestTrustGateDropsSubagentAllowsKeepsDenyAsk pins the trust gate over the
// subagent block: an UNTRUSTED project's subagent ALLOWS are dropped while its
// subagent deny/ask (and the top-level deny) are kept — allows are the only
// loosening direction, so they are the only thing trust gates.
func TestTrustGateDropsSubagentAllowsKeepsDenyAsk(t *testing.T) {
	const yamlDoc = `
permissions:
  deny:
    - "Bash(rm:*)"
  subagent:
    allow:
      - "Bash(cat:*)"
    ask:
      - "Bash(go build:*)"
    deny:
      - "Bash(curl:*)"
`
	r := newWithEnv(Options{Conventional: true, TrustProject: false}, fakeEnv())
	ws := newProjectWS(t, "/repo", yamlDoc)
	rules := r.Resolve(context.Background(), ws)

	if findRule(rules, "Bash", "cat*") != nil {
		t.Fatalf("untrusted project's SUBAGENT allow must be dropped: %+v", rules)
	}
	if got := findRule(rules, "Bash", "go build*"); got == nil || got.Effect != governance.Ask || got.Audience != governance.AudienceSubagent {
		t.Fatalf("untrusted project's subagent ask must be kept (AudienceSubagent): %+v", got)
	}
	if got := findRule(rules, "Bash", "curl*"); got == nil || got.Effect != governance.Deny || got.Audience != governance.AudienceSubagent {
		t.Fatalf("untrusted project's subagent deny must be kept (AudienceSubagent): %+v", got)
	}
	if findRule(rules, "Bash", "rm*") == nil {
		t.Fatalf("top-level deny must be kept: %+v", rules)
	}

	// Trusted: the subagent allow resolves, tagged AudienceSubagent.
	rt := newWithEnv(Options{Conventional: true, TrustProject: true}, fakeEnv())
	trusted := rt.Resolve(context.Background(), newProjectWS(t, "/repo2", yamlDoc))
	if got := findRule(trusted, "Bash", "cat*"); got == nil || got.Effect != governance.Allow || got.Audience != governance.AudienceSubagent {
		t.Fatalf("trusted project's subagent allow must resolve (AudienceSubagent): %+v", got)
	}
}

// TestPermissionsStrictNonMappingAndDecodeErrors pins the remaining strict-arms
// (issue #32 panel 7): a non-mapping permissions: value is a loud shape error,
// and a per-key decode failure is wrapped with the failing key's path.
func TestPermissionsStrictNonMappingAndDecodeErrors(t *testing.T) {
	for _, document := range []string{
		"permissions: [x]\n",
		"permissions:\n  allow:\n    nested: map\n",
		"permissions:\n  subagent:\n    deny:\n      nested: map\n",
	} {
		if _, err := parseYAML([]byte(document)); err == nil || !strings.Contains(err.Error(), "permissions") {
			t.Fatalf("strict invalid permissions must fail without YAML-derived detail; got %v", err)
		}
	}
}

// TestStrictSkipWarnNamesLostRuleCounts pins the issue-#32 panel 8a diagnostic:
// a strict-parse skip (typo'd key) drops the file's DENY/ASK rules too —
// strictness loosening policy — so the WARN must name the lost per-effect
// counts.
func TestStrictSkipWarnNamesLostRuleCounts(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelInfo)
	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	// A typo'd sibling key fails the strict parse; the file ALSO carries one
	// deny + one subagent deny + one ask that are about to be lost.
	const doc = `
permissions:
  deny:
    - "Bash(zap:*)"
  ask:
    - "Bash(go vet:*)"
  alow:
    - "Bash(ls)"
  subagent:
    deny:
      - "Bash(curl:*)"
`
	ws := newProjectWS(t, "/repo", doc)
	rules := r.Resolve(context.Background(), ws)
	if len(rules) != 0 {
		t.Fatalf("the strict-failed file must contribute no rules; got %+v", rules)
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "rules are LOST") {
		t.Fatalf("expected the WARN-level lost-rules skip line; got: %s", out)
	}
	if !strings.Contains(out, "lost_deny=2") || !strings.Contains(out, "lost_ask=1") || !strings.Contains(out, "lost_allow=0") {
		t.Fatalf("the skip WARN must name the per-effect lost counts (deny=2 ask=1 allow=0); got: %s", out)
	}
}

// TestLostRuleCountsUnavailableOnSyntaxError pins the counts-unavailable arm: a
// true YAML syntax error cannot be leniently counted; the WARN still fires with
// counts_known=false.
func TestLostRuleCountsUnavailableOnSyntaxError(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelInfo)
	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	ws := newProjectWS(t, "/repo", "permissions: [this is: not: valid")
	_ = r.Resolve(context.Background(), ws)
	out := buf.String()
	if !strings.Contains(out, "counts_known=false") {
		t.Fatalf("a syntax-error skip must mark counts_known=false; got: %s", out)
	}
}
