package governance

import "testing"

// --- Audience matching (issue #32) -------------------------------------------

// TestAudienceMatchMatrix pins the symmetric-permissive audience rule: a rule
// binds an evaluator iff the rule's audience is AudienceAll, the evaluator's is
// AudienceAll, or they are equal.
func TestAudienceMatchMatrix(t *testing.T) {
	cases := []struct {
		name      string
		rule      Audience
		evaluator Audience
		matches   bool
	}{
		{"all rule / all evaluator", AudienceAll, AudienceAll, true},
		{"all rule / main evaluator", AudienceAll, AudienceMain, true},
		{"all rule / subagent evaluator", AudienceAll, AudienceSubagent, true},
		{"main rule / all evaluator", AudienceMain, AudienceAll, true},
		{"main rule / main evaluator", AudienceMain, AudienceMain, true},
		{"main rule / subagent evaluator", AudienceMain, AudienceSubagent, false},
		{"subagent rule / all evaluator", AudienceSubagent, AudienceAll, true},
		{"subagent rule / main evaluator", AudienceSubagent, AudienceMain, false},
		{"subagent rule / subagent evaluator", AudienceSubagent, AudienceSubagent, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A single Deny rule with the case's audience: when it matches, the
			// decision is Deny; when filtered, the no-match default Ask stands.
			rules := []Rule{{Scope: ScopeSharedProject, Tool: "Shell", Pattern: "rm *", Effect: Deny, Audience: tc.rule}}
			e := NewEvaluator(rules, WithAudience(tc.evaluator))
			got := e.Evaluate("Shell", bashArgs("rm x"), false)
			if tc.matches && got.Effect != Deny {
				t.Fatalf("rule audience %v should bind evaluator audience %v; got %v", tc.rule, tc.evaluator, got.Effect)
			}
			if !tc.matches && got.Effect == Deny {
				t.Fatalf("rule audience %v must NOT bind evaluator audience %v; got Deny", tc.rule, tc.evaluator)
			}
		})
	}
}

// TestSubagentRuleInvisibleToMainAndViceVersa pins the two leak directions: a
// subagent-tagged Allow must not loosen the main evaluator, and a main-tagged
// Allow must not loosen a subagent evaluator (each falls back to the builtin
// mutate-ask floor / the no-match default).
func TestSubagentRuleInvisibleToMainAndViceVersa(t *testing.T) {
	subAllow := Rule{Scope: ScopeSharedProject, Tool: "Shell", Pattern: "rm *", Effect: Allow, Audience: AudienceSubagent}
	mainAllow := Rule{Scope: ScopeSharedProject, Tool: "Shell", Pattern: "rm *", Effect: Allow, Audience: AudienceMain}

	mainEval := NewEvaluator([]Rule{subAllow}, WithAudience(AudienceMain))
	if got := mainEval.Evaluate("Shell", bashArgs("rm x"), false); got.Effect != Ask {
		t.Fatalf("subagent allow must be invisible to the main evaluator (default Ask), got %v", got.Effect)
	}
	subEval := NewEvaluator([]Rule{mainAllow}, WithAudience(AudienceSubagent))
	if got := subEval.Evaluate("Shell", bashArgs("rm x"), false); got.Effect != Ask {
		t.Fatalf("main allow must be invisible to a subagent evaluator (default Ask), got %v", got.Effect)
	}
}

// TestDenyDominantWithMixedAudiences pins that an AudienceAll deny beats an
// audience-matched allow on BOTH evaluator classes, and that a subagent-tagged
// deny still binds a subagent evaluator over an all-audience allow.
func TestDenyDominantWithMixedAudiences(t *testing.T) {
	rules := []Rule{
		{Scope: ScopeUser, Tool: "Shell", Pattern: "rm *", Effect: Allow, Audience: AudienceSubagent},
		{Scope: ScopeSharedProject, Tool: "Shell", Pattern: "rm *", Effect: Deny, Audience: AudienceAll},
	}
	for _, aud := range []Audience{AudienceMain, AudienceSubagent, AudienceAll} {
		e := NewEvaluator(rules, WithAudience(aud))
		if got := e.Evaluate("Shell", bashArgs("rm x"), false); got.Effect != Deny {
			t.Fatalf("AudienceAll deny must dominate for evaluator audience %v; got %v", aud, got.Effect)
		}
	}
	subRules := []Rule{
		{Scope: ScopeUser, Tool: "Shell", Pattern: "rm *", Effect: Allow, Audience: AudienceAll},
		{Scope: ScopeSharedProject, Tool: "Shell", Pattern: "rm *", Effect: Deny, Audience: AudienceSubagent},
	}
	e := NewEvaluator(subRules, WithAudience(AudienceSubagent))
	if got := e.Evaluate("Shell", bashArgs("rm x"), false); got.Effect != Deny {
		t.Fatalf("subagent deny must dominate the all-audience allow on a subagent evaluator; got %v", got.Effect)
	}
}

// TestAudienceDefaultIsAllBackCompat pins back-compat: an evaluator built
// WITHOUT WithAudience matches every rule regardless of its tag.
func TestAudienceDefaultIsAllBackCompat(t *testing.T) {
	rules := []Rule{{Scope: ScopeSharedProject, Tool: "Shell", Pattern: "ls*", Effect: Allow, Audience: AudienceSubagent}}
	e := NewEvaluator(rules)
	if got := e.Evaluate("Shell", bashArgs("ls"), false); got.Effect != Allow {
		t.Fatalf("default (AudienceAll) evaluator must match a tagged rule; got %v", got.Effect)
	}
}

// --- ConfiguredAsk (issue #32) ------------------------------------------------

// TestConfiguredAskTruthTable pins the ConfiguredAsk bit across every Ask
// provenance: configured rule → true; builtin-floor rule, no-match default,
// substitution-floor escalation → false; non-Ask decisions → false.
func TestConfiguredAskTruthTable(t *testing.T) {
	floorAllow := Rule{Scope: ScopeBuiltinDefault, Effect: Allow}
	cases := []struct {
		name          string
		rules         []Rule
		cmd           string
		wantEffect    Effect
		wantConfAsk   bool
		wantFloorBits bool // FlooredConfiguredAllow
	}{
		{
			name:        "configured ask wins → ConfiguredAsk",
			rules:       []Rule{floorAllow, {Scope: ScopeSharedProject, Tool: "Shell", Pattern: "go test*", Effect: Ask}},
			cmd:         "go test ./...",
			wantEffect:  Ask,
			wantConfAsk: true,
		},
		{
			name:        "builtin-floor ask → NOT configured",
			rules:       []Rule{{Scope: ScopeBuiltinDefault, Tool: "Shell", Pattern: "go test*", Effect: Ask}},
			cmd:         "go test ./...",
			wantEffect:  Ask,
			wantConfAsk: false,
		},
		{
			name:        "no matching rule default ask → NOT configured",
			rules:       nil,
			cmd:         "go test ./...",
			wantEffect:  Ask,
			wantConfAsk: false,
		},
		{
			name:        "substitution-floor escalation → NOT configured",
			rules:       []Rule{floorAllow},
			cmd:         "cat $(zap)",
			wantEffect:  Ask,
			wantConfAsk: false,
		},
		{
			name:        "allow decision → bits zero",
			rules:       []Rule{floorAllow},
			cmd:         "ls",
			wantEffect:  Allow,
			wantConfAsk: false,
		},
		{
			name:        "deny decision → bits zero",
			rules:       []Rule{floorAllow, {Scope: ScopeSharedProject, Tool: "Shell", Pattern: "rm *", Effect: Deny}},
			cmd:         "rm x",
			wantEffect:  Deny,
			wantConfAsk: false,
		},
		{
			name: "configured ask on one segment gates the compound",
			rules: []Rule{
				floorAllow,
				{Scope: ScopeUser, Tool: "Shell", Pattern: "go vet*", Effect: Ask},
			},
			cmd:         "ls && go vet ./...",
			wantEffect:  Ask,
			wantConfAsk: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEvaluator(tc.rules)
			got := e.Evaluate("Shell", bashArgs(tc.cmd), false)
			if got.Effect != tc.wantEffect {
				t.Fatalf("effect = %v, want %v (%s)", got.Effect, tc.wantEffect, got.Reason)
			}
			if got.ConfiguredAsk != tc.wantConfAsk {
				t.Fatalf("ConfiguredAsk = %v, want %v (%s)", got.ConfiguredAsk, tc.wantConfAsk, got.Reason)
			}
			if got.FlooredConfiguredAllow != tc.wantFloorBits {
				t.Fatalf("FlooredConfiguredAllow = %v, want %v", got.FlooredConfiguredAllow, tc.wantFloorBits)
			}
			if got.ConfiguredAsk && got.FlooredConfiguredAllow {
				t.Fatalf("ConfiguredAsk and FlooredConfiguredAllow must be mutually exclusive")
			}
		})
	}
}

// TestConfiguredAskNonShell pins the bit on the simple (non-Shell) path too.
func TestConfiguredAskNonShell(t *testing.T) {
	rules := []Rule{{Scope: ScopeLocalProject, Tool: "Write", Effect: Ask}}
	e := NewEvaluator(rules)
	got := e.Evaluate("Write", fileArgs("/x"), false)
	if got.Effect != Ask || !got.ConfiguredAsk {
		t.Fatalf("configured non-Shell Ask must set ConfiguredAsk; got %+v", got)
	}
}

// --- FlooredConfiguredAllow (issue #32) ---------------------------------------

// TestFlooredConfiguredAllow pins the bit's full condition: the floor-free fold
// is Allow, every floored segment is covered by a CONFIGURED Allow, every
// recursively-extracted INNER is positively read-only (the configured Allow
// vouches only for the OUTER — a hidden non-read-only inner must never
// auto-run), and the blanked outer passes the worktree-escape rejections —
// including compound mixes and the escape bounds (git push / path-bearing git
// flags / go -exec must NEVER qualify).
func TestFlooredConfiguredAllow(t *testing.T) {
	floorAllow := Rule{Scope: ScopeBuiltinDefault, Effect: Allow}
	goTestAllow := Rule{Scope: ScopeSharedProject, Tool: "Shell", Pattern: "go test*", Effect: Allow}
	echoAllow := Rule{Scope: ScopeSharedProject, Tool: "Shell", Pattern: "echo *", Effect: Allow}
	cases := []struct {
		name       string
		rules      []Rule
		cmd        string
		wantEffect Effect
		want       bool
	}{
		{
			name:       "read-only inner + configured outer allow qualifies",
			rules:      []Rule{floorAllow, goTestAllow},
			cmd:        "go test $(git rev-parse HEAD)",
			wantEffect: Ask,
			want:       true,
		},
		{
			name:       "read-only inner + non-read-only outer (redirection) under configured allow",
			rules:      []Rule{floorAllow, echoAllow},
			cmd:        "echo $(ls) > out.txt",
			wantEffect: Ask,
			want:       true,
		},
		{
			name:       "SHIP-BLOCKER bound: unknown inner must NOT qualify (hidden command)",
			rules:      []Rule{floorAllow, goTestAllow},
			cmd:        "go test $(zap)",
			wantEffect: Ask,
			want:       false,
		},
		{
			name:       "mutating inner must NOT qualify",
			rules:      []Rule{floorAllow, goTestAllow},
			cmd:        "go test $(touch SAFE_MARKER)",
			wantEffect: Ask,
			want:       false,
		},
		{
			name:       "inner with redirection must NOT qualify",
			rules:      []Rule{floorAllow, echoAllow},
			cmd:        "echo $(ls > f)",
			wantEffect: Ask,
			want:       false,
		},
		{
			name:       "floor allow-all alone does NOT qualify (not configured)",
			rules:      []Rule{floorAllow},
			cmd:        "go test $(git rev-parse HEAD)",
			wantEffect: Ask,
			want:       false,
		},
		{
			name: "compound mix: floored configured-allow + plain allow segment",
			rules: []Rule{floorAllow, goTestAllow,
				{Scope: ScopeSharedProject, Tool: "Shell", Pattern: "go vet*", Effect: Allow}},
			cmd:        "go test $(git rev-parse HEAD) && go vet ./...",
			wantEffect: Ask,
			want:       true,
		},
		{
			name:       "compound: TWO covered floored segments both qualify",
			rules:      []Rule{floorAllow, goTestAllow, echoAllow},
			cmd:        "go test $(git rev-parse HEAD) && echo $(ls) > f",
			wantEffect: Ask,
			want:       true,
		},
		{
			name: "compound mix kills !flooredBad: one safe floored + one unsafe floored segment",
			rules: []Rule{floorAllow, goTestAllow,
				{Scope: ScopeSharedProject, Tool: "Shell", Pattern: "git push*", Effect: Allow}},
			cmd:        "go test $(git rev-parse HEAD) && git push $(x)",
			wantEffect: Ask,
			want:       false,
		},
		{
			name:       "compound mix: another segment asks → floor-free fold not Allow",
			rules:      []Rule{floorAllow, goTestAllow, {Scope: ScopeSharedProject, Tool: "Shell", Pattern: "go vet*", Effect: Ask}},
			cmd:        "go test $(git rev-parse HEAD) && go vet ./...",
			wantEffect: Ask,
			want:       false,
		},
		{
			name:       "escape bound: git push under a configured allow",
			rules:      []Rule{floorAllow, {Scope: ScopeSharedProject, Tool: "Shell", Pattern: "git push*", Effect: Allow}},
			cmd:        "git push $(git rev-parse HEAD)",
			wantEffect: Ask,
			want:       false,
		},
		{
			name:       "escape bound: path-bearing git -C under a configured allow",
			rules:      []Rule{floorAllow, {Scope: ScopeSharedProject, Tool: "Shell", Pattern: "git *", Effect: Allow}},
			cmd:        "git -C /outside $(ls) log",
			wantEffect: Ask,
			want:       false,
		},
		{
			name:       "escape bound: go test -exec under a configured allow",
			rules:      []Rule{floorAllow, goTestAllow},
			cmd:        "go test -exec=$(ls) ./...",
			wantEffect: Ask,
			want:       false,
		},
		{
			name:       "no configured rule at all",
			rules:      nil,
			cmd:        "go test $(git rev-parse HEAD)",
			wantEffect: Ask,
			want:       false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEvaluator(tc.rules)
			got := e.Evaluate("Shell", bashArgs(tc.cmd), false)
			if got.Effect != tc.wantEffect {
				t.Fatalf("effect = %v, want %v (%s)", got.Effect, tc.wantEffect, got.Reason)
			}
			if got.FlooredConfiguredAllow != tc.want {
				t.Fatalf("FlooredConfiguredAllow = %v, want %v (%s)", got.FlooredConfiguredAllow, tc.want, got.Reason)
			}
			if got.ConfiguredAsk && got.FlooredConfiguredAllow {
				t.Fatalf("bits must be mutually exclusive")
			}
		})
	}
}

// TestFlooredConfiguredAllowNeverOnDeny pins that a deny anywhere in the
// compound zeroes both bits (deny resolves in the fold; the bit must not
// suggest an auto-approve for a denied line).
func TestFlooredConfiguredAllowNeverOnDeny(t *testing.T) {
	rules := []Rule{
		{Scope: ScopeBuiltinDefault, Effect: Allow},
		{Scope: ScopeSharedProject, Tool: "Shell", Pattern: "cat *", Effect: Allow},
		{Scope: ScopeSharedProject, Tool: "Shell", Pattern: "go vet*", Effect: Deny},
	}
	e := NewEvaluator(rules)
	got := e.Evaluate("Shell", bashArgs("cat $(zap) && go vet ./..."), false)
	if got.Effect != Deny {
		t.Fatalf("expected Deny, got %v", got.Effect)
	}
	if got.ConfiguredAsk || got.FlooredConfiguredAllow {
		t.Fatalf("deny decision must carry zero bits; got %+v", got)
	}
}

// --- flooredAllowSafe (issue #32) ---------------------------------------------

// TestFlooredAllowSafe pins the floored-configured-allow bound: every extracted
// INNER positively read-only (the A1 inner contract), the blanked OUTER free of
// the shared worktree-escape rejections (with the pure-subshell discipline for
// command-position substitutions), fail-safe on extraction ambiguity. The outer
// is deliberately NOT required to be read-only — the configured Allow vouches
// for it.
func TestFlooredAllowSafe(t *testing.T) {
	cases := []struct {
		seg  string
		want bool
	}{
		// Read-only inners + vouched-for outers.
		{"go test $(git rev-parse HEAD)", true},
		{"echo $(ls) > out.txt", true},
		{"go test $(cat pkgs.txt) ./...", true},
		{"cat $(ls)", true},                            // (in practice A1 clears this before the floor)
		{"go test `git rev-parse HEAD`", true},         // backtick substitution
		{"echo $(cat $(ls))", true},                    // nested read-only substitution inner
		{"for p in $(cat pkgs.txt); do go test", true}, // for-header scaffolding outer + read-only inner
		{"(ls)", true},                                 // pure subshell, read-only inner
		// NON-read-only inners: the ship-blocker bound.
		{"go test $(zap)", false},
		{"echo $(touch SAFE_MARKER)", false},
		{"go test $(cp a b)", false},
		{"echo $(sh -c zap)", false},
		{"echo $(ls > f)", false},                       // inner redirection
		{"cat $(ls\nzap)", false},                       // newline-smuggled second inner command
		{"go test $(git rev-parse HEAD && zap)", false}, // compound inner, one bad
		// Command-position substitution executes its OUTPUT — never cleared.
		{"$(ls)", false},
		{"`ls`", false},
		// Worktree-escape git subcommands on the OUTER (defense-in-depth).
		{"git push $(git rev-parse HEAD)", false},
		{"git fetch $(ls)", false},
		{"git config $(ls)", false},
		{"git remote $(ls)", false},
		{"git pull $(ls)", false},
		{"git clone $(ls)", false},
		{"git worktree $(ls)", false},
		{"git submodule $(ls)", false},
		// Path-bearing git global flags point OUTSIDE the worktree.
		{"git -C /outside $(ls) log", false},
		{"git --git-dir=/x $(ls) status", false},
		{"git --work-tree=/y $(ls) status", false},
		// go escape flags on the OUTER.
		{"go test -exec=$(ls) ./...", false},
		{"go build -o /tmp/$(ls) ./...", false},
		{"go test -toolexec=$(ls) ./...", false},
		{"go build -overlay=$(ls) ./...", false},
		// Extraction ambiguity fails safe.
		{"cat $(unclosed", false},
		{"cat ${x:-$(y)}", false},
		// No substitution at all: FAIL-SAFE false (this classifier only relaxes the
		// substitution floor; a plain segment has no floor to relax). See
		// TestFlooredAllowSafeNoSubstitutionFailsSafe for the security rationale.
		{"git push origin main", false},
		{"go test ./...", false},
		{"ls", false},
	}
	for _, tc := range cases {
		t.Run(tc.seg, func(t *testing.T) {
			if got := flooredAllowSafe(tc.seg); got != tc.want {
				t.Fatalf("flooredAllowSafe(%q) = %v, want %v", tc.seg, got, tc.want)
			}
		})
	}
}

// TestFlooredAllowSafeNoSubstitutionFailsSafe pins the cheap footgun guard: a
// no-substitution input — even an arbitrary destructive-looking one — returns
// FALSE, not a permissive escape-free answer. flooredAllowSafe only decides
// whether the SUBSTITUTION floor may be relaxed, so a segment with no
// substitution has no floor to relax. Unreachable on the live evaluator path
// (the floor branch always passes a substitution-bearing segment), but a future
// caller dropping that guard must get the safe answer.
func TestFlooredAllowSafeNoSubstitutionFailsSafe(t *testing.T) {
	for _, seg := range []string{"zap -rf x", "ls", "go test ./...", "git status", ""} {
		if flooredAllowSafe(seg) {
			t.Fatalf("flooredAllowSafe(%q) on a no-substitution input must fail safe (false)", seg)
		}
	}
}

// TestExistingClassifiersUntouchedByAudienceWork is a sentinel: the issue-#32
// work must not have widened or narrowed the pre-existing classifiers. A small
// canary set per classifier (the full tables live in their own test files).
func TestExistingClassifiersUntouchedByAudienceWork(t *testing.T) {
	if !ReadOnlyShell("git status && ls") {
		t.Fatal("ReadOnlyShell regressed: read-only compound must stay true")
	}
	if ReadOnlyShell("cat $(ls)") {
		t.Fatal("ReadOnlyShell regressed: substitution must stay fail-safe false")
	}
	if !SubstitutionReadOnly("cat $(ls)") {
		t.Fatal("SubstitutionReadOnly regressed: read-only substitution must stay true")
	}
	if SubstitutionReadOnly("cat $(zap)") {
		t.Fatal("SubstitutionReadOnly regressed: unknown inner must stay false")
	}
	if !IsolationApprovable("go test ./...") {
		t.Fatal("IsolationApprovable regressed: worktree-safe go test must stay true")
	}
	if IsolationApprovable("git push origin main") {
		t.Fatal("IsolationApprovable regressed: git push must stay false")
	}
}
