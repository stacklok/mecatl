package permconfig

import (
	"context"
	"os"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestParseYAMLLoad(t *testing.T) {
	data := []byte(`
permissions:
  allow:
    - "Shell(go test:*)"
    - "Read"
  ask:
    - "Shell(git push:*)"
  deny:
    - "Shell(rm:*)"
`)
	cfg, err := parseYAML(data)
	if err != nil {
		t.Fatalf("parseYAML: %v", err)
	}
	if len(cfg.Permissions.Allow) != 2 || len(cfg.Permissions.Ask) != 1 || len(cfg.Permissions.Deny) != 1 {
		t.Fatalf("unexpected buckets: %+v", cfg.Permissions)
	}

	var report Report
	rules := rulesFromConfig(cfg, governance.ScopeSharedProject, &report)
	if len(rules) != 4 {
		t.Fatalf("expected 4 rules, got %d (%+v)", len(rules), rules)
	}
	if !report.Empty() {
		t.Fatalf("clean config should report nothing, got %+v", report)
	}
	// The Claude-style "go test:*" must normalise to the glob "go test*".
	var found bool
	for _, r := range rules {
		if r.Tool == "Shell" && r.Effect == governance.Allow {
			if r.Pattern != "go test*" {
				t.Fatalf("expected normalised pattern %q, got %q", "go test*", r.Pattern)
			}
			if r.Scope != governance.ScopeSharedProject {
				t.Fatalf("expected ScopeSharedProject, got %v", r.Scope)
			}
			if r.Exact {
				t.Fatalf("config rules must NOT be Exact (glob semantics)")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("did not find the normalised Shell allow rule")
	}
}

func TestGoccyYAMLMigration_SemanticMatrixPermconfig(t *testing.T) {
	data, err := os.ReadFile("../../../engine/testdata/semantic-matrix.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var matrix struct {
		Cases []struct {
			Name     string            `yaml:"name"`
			Document string            `yaml:"document"`
			Readers  map[string]string `yaml:"readers"`
		} `yaml:"cases"`
	}
	if err := yaml.Unmarshal(data, &matrix); err != nil {
		t.Fatal(err)
	}
	for _, tc := range matrix.Cases {
		outcome, ok := tc.Readers["permconfig"]
		if !ok {
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			_, err := parseYAML([]byte(tc.Document))
			if accepted, want := err == nil, outcome == "accept"; accepted != want {
				t.Fatalf("parseYAML() accepted=%v, want %v (error=%v)", accepted, want, err)
			}
		})
	}
}

func TestParseYAMLEmptyAndNil(t *testing.T) {
	for _, in := range [][]byte{nil, {}, []byte("   \n  ")} {
		cfg, err := parseYAML(in)
		if err != nil {
			t.Fatalf("empty input should not error: %v", err)
		}
		if len(cfg.Permissions.Allow)+len(cfg.Permissions.Ask)+len(cfg.Permissions.Deny) != 0 {
			t.Fatalf("empty input should yield no rules, got %+v", cfg)
		}
	}
}

func TestParseYAMLMalformedErrors(t *testing.T) {
	if _, err := parseYAML([]byte("permissions: [this is not: valid: yaml")); err == nil {
		t.Fatal("expected an error for malformed YAML")
	}
}

// A config file over the byte cap is rejected before parsing (CWE-770 guard).
func TestParseYAMLSizeCap(t *testing.T) {
	big := make([]byte, maxConfigBytes+1)
	for i := range big {
		big[i] = ' '
	}
	if _, err := parseYAML(big); err == nil {
		t.Fatalf("expected an over-cap (%d-byte) config to be rejected", len(big))
	}
	// A file at the cap is allowed (boundary). Fill with spaces (valid YAML: an
	// all-whitespace doc parses as the empty config) rather than NUL bytes.
	atCap := make([]byte, maxConfigBytes)
	for i := range atCap {
		atCap[i] = ' '
	}
	if _, err := parseYAML(atCap); err != nil {
		t.Fatalf("a config exactly at the cap should parse, got %v", err)
	}
}

// The per-config rule-count cap drops specs beyond the limit (reported), keeping
// the merged set bounded.
func TestRulesFromConfigRuleCountCap(t *testing.T) {
	var cfg Config
	for i := 0; i < maxRulesPerConfig+10; i++ {
		cfg.Permissions.Allow = append(cfg.Permissions.Allow, "Read")
	}
	var report Report
	rules := rulesFromConfig(cfg, governance.ScopeUser, &report)
	if len(rules) != maxRulesPerConfig {
		t.Fatalf("expected the rule set capped at %d, got %d", maxRulesPerConfig, len(rules))
	}
	if len(report.Dropped) != 10 {
		t.Fatalf("expected 10 dropped (over-cap) specs reported, got %d", len(report.Dropped))
	}
}

func TestParseSpec(t *testing.T) {
	cases := []struct {
		spec        string
		ok          bool
		wantTool    string
		wantPattern string
	}{
		{"Read", true, "Read", ""},
		{"Shell(go test:*)", true, "Shell", "go test*"},
		{"Shell(git push:)", true, "Shell", "git push*"},
		{"Shell(rm -rf *)", true, "Shell", "rm -rf *"},
		{"Edit(src/**)", true, "Edit", "src/**"},
		{"", false, "", ""},
		{"Shell(unterminated", false, "", ""},
		{"(noTool)", false, "", ""},
	}
	for _, c := range cases {
		rule, ok := parseSpec(c.spec, governance.ScopeUser, governance.Allow)
		if ok != c.ok {
			t.Fatalf("%q: ok=%v want %v", c.spec, ok, c.ok)
		}
		if !ok {
			continue
		}
		if rule.Tool != c.wantTool || rule.Pattern != c.wantPattern {
			t.Fatalf("%q: got tool=%q pattern=%q want tool=%q pattern=%q",
				c.spec, rule.Tool, rule.Pattern, c.wantTool, c.wantPattern)
		}
	}
}

func TestNormalizeGlob(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"go test:*": "go test*",
		"git push:": "git push*",
		"go test*":  "go test*",
		"src/**":    "src/**",
		"npm run:*": "npm run*",
		"plain":     "plain",
		"a:b:c:*":   "a:b:c*", // only the LAST ":" qualifier is the prefix split
	}
	for in, want := range cases {
		if got := normalizeGlob(in); got != want {
			t.Fatalf("normalizeGlob(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalShellTool_Scenario2_LegacyConfigPreservesDeny(t *testing.T) {
	cfg, err := parseYAML([]byte(`
permissions:
  allow: ["Bash(go test:*)"]
  deny: ["Bash(go test:*)", "BashSystemTemp"]
guardrails:
  model: checker
  rules:
    - match: Bash
    - match: Bash*
    - match: BashStatus
    - match: BashStatus*
    - match: BashSystemTemp
    - match: BashSystemTemp*
`))
	if err != nil {
		t.Fatalf("parseYAML: %v", err)
	}

	var report Report
	rules := rulesFromConfig(cfg, governance.ScopeSharedProject, &report)
	decision := permpolicy.NewPolicy(rules, nil).Evaluate(context.Background(), "legacy", session.ModeDefault, session.NewToolCall("shell", tool.ShellToolName, []byte(`{"command":"go test ./..."}`)), nil)
	if decision.Effect != governance.Deny {
		t.Fatalf("legacy Bash rules decision = %q, want deny", decision.Effect)
	}
	tempDecision := permpolicy.NewPolicy(rules, nil).Evaluate(context.Background(), "legacy", session.ModeDefault, session.NewToolCall("temp", "ShellSystemTemp", nil), nil)
	if tempDecision.Effect != governance.Deny {
		t.Fatalf("legacy BashSystemTemp rule decision = %q, want deny", tempDecision.Effect)
	}
	guardrails := cfg.Guardrails
	if guardrails == nil || len(guardrails.Rules) != 6 {
		t.Fatalf("guardrails = %#v", guardrails)
	}
	for i, want := range []string{tool.ShellToolName, "Shell*", "ShellStatus", "ShellStatus*", "ShellSystemTemp", "ShellSystemTemp*"} {
		if got := guardrails.Rules[i].Match; got != want {
			t.Errorf("legacy guardrail match = %q, want %q", got, want)
		}
	}
}
