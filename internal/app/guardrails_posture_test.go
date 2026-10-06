package app

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/modelhook"
)

// TestLogGuardrailsPostureBranches pins the #159 build-once posture line: exactly ONE
// INFO per Build, one case per UX-B branch, with honest substrings (OFF no-config +
// hint, OFF kill-switch, ON via slot defaults, ON via slot superseding gate, ON via
// gate no slot, ON with custom rules). Each case asserts exactly one line.
func TestLogGuardrailsPostureBranches(t *testing.T) {
	cases := []struct {
		name      string
		cfg       Config
		wantCount int
		wantSubs  []string
		notSubs   []string
	}{
		{
			name:      "OFF no config + hint",
			cfg:       Config{},
			wantCount: 1,
			wantSubs:  []string{"guardrails: OFF", "no checker model configured", "guardrail", "--guardrails-model"},
		},
		{
			name:      "OFF kill-switch",
			cfg:       Config{GuardrailsDisabled: true, GuardrailsModel: "gpt-5-mini"},
			wantCount: 1,
			wantSubs:  []string{"guardrails: OFF", "kill-switch"},
		},
		{
			name: "ON via slot defaults (block + default set)",
			cfg: Config{
				UseMock:      true,
				ModelSlots:   map[string]string{slotGuardrail: "cheap"},
				ModelAliases: map[string]string{"cheap": "slot-id"},
			},
			wantCount: 1,
			// The default set is BLOCK (ADR 0060), so the posture line reports mode=block.
			wantSubs: []string{"guardrails: ON", "checker=slot-id", "via slot `guardrail`", "mode=block", "default set"},
		},
		{
			name: "ON via slot superseding gate",
			cfg: Config{
				UseMock:         true,
				GuardrailsModel: "gate-id",
				ModelSlots:      map[string]string{slotGuardrail: "cheap"},
				ModelAliases:    map[string]string{"cheap": "slot-id"},
			},
			wantCount: 1,
			wantSubs:  []string{"guardrails: ON", "checker=slot-id", "supersedes gate value", "via slot `guardrail`"},
			notSubs:   []string{"checker=gate-id"},
		},
		{
			name: "ON via gate no slot",
			cfg: Config{
				UseMock:         true,
				GuardrailsModel: "gpt-5-mini",
			},
			wantCount: 1,
			wantSubs:  []string{"guardrails: ON", "checker=gpt-5-mini", "via --guardrails-model", "mode=block", "default set"},
		},
		{
			name: "ON with custom rules (block + rules)",
			cfg: Config{
				UseMock:         true,
				GuardrailsModel: "gpt-5-mini",
				GuardrailsRules: []GuardrailRule{
					{Match: "WebFetch", Phases: []string{"post"}, Mode: string(modelhook.ModeBlock)},
				},
			},
			wantCount: 1,
			wantSubs:  []string{"guardrails: ON", "checker=gpt-5-mini", "mode=block", "rules=1"},
			notSubs:   []string{"default set"},
		},
		{
			// SHIP-BLOCKER regression guard (issue #159 iter-2 review Finding 1): a slot
			// that is BOUND but UNRESOLVABLE (a bare token with no model-alias / no
			// separator, so lookupModelAlias returns known=false and resolveSlotModel
			// fail-softs to "") must emit "guardrails: OFF" — NOT a false "guardrails:
			// ON". guardrailsConfigured returns true for a bound slot (it gates only the
			// hook WIRING), but the resolver's `configured` is false, so the posture line
			// (which now branches on the resolver's `configured`) and the live checker
			// (buildGuardrailsChecker returns nil) agree on OFF.
			name: "OFF bound-but-unresolvable slot (no false ON)",
			cfg: Config{
				UseMock:    true,
				ModelSlots: map[string]string{slotGuardrail: "bogusalias"},
			},
			wantCount: 1,
			wantSubs:  []string{"guardrails: OFF", "no checker model configured"},
			notSubs:   []string{"guardrails: ON"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			diag := &capturingDiag{}
			c.cfg.Diagnostics = diag
			logGuardrailsPosture(c.cfg)
			if got := len(diag.lines); got != c.wantCount {
				t.Fatalf("lines = %d, want %d; lines=%v", got, c.wantCount, diag.lines)
			}
			for _, sub := range c.wantSubs {
				if !diag.has(sub) {
					t.Fatalf("missing substring %q; lines=%v", sub, diag.lines)
				}
			}
			for _, sub := range c.notSubs {
				if diag.has(sub) {
					t.Fatalf("unexpected substring %q; lines=%v", sub, diag.lines)
				}
			}
		})
	}
}

// TestLogGuardrailsPostureReportsResolvedModelNotGate is the headline must-fix #1: a
// bound `guardrail` slot SUPERSEDES a differing gate value, and the ON posture line's
// `checker=` MUST be the SLOT model — NOT the (inert) gate value the old
// normalizeGuardrailsModel "guardrails ACTIVE" emit reported.
func TestLogGuardrailsPostureReportsResolvedModelNotGate(t *testing.T) {
	diag := &capturingDiag{}
	cfg := Config{
		UseMock:         true,
		GuardrailsModel: "gate-id",
		ModelSlots:      map[string]string{slotGuardrail: "cheap"},
		ModelAliases:    map[string]string{"cheap": "slot-id"},
		Diagnostics:     diag,
	}
	logGuardrailsPosture(cfg)
	if got := len(diag.lines); got != 1 {
		t.Fatalf("expected exactly one posture line, got %d; lines=%v", got, diag.lines)
	}
	line := diag.lines[0]
	if !strings.Contains(line, "checker=slot-id") {
		t.Fatalf("posture line must report the RESOLVED slot model, not the gate value; line=%q", line)
	}
	if strings.Contains(line, "checker=gate-id") {
		t.Fatalf("posture line must NOT report the inert gate value as the checker; line=%q", line)
	}
}

// TestHighestSeverityGuardrailMode pins the supported severity ordering:
// block > advisory, with an empty/unknown mode counting as block.
func TestHighestSeverityGuardrailMode(t *testing.T) {
	spec := func(mode string) modelhook.RuleSpec {
		return modelhook.RuleSpec{Match: "WebFetch", Phases: []string{"post"}, Mode: mode}
	}
	cases := []struct {
		name  string
		specs []modelhook.RuleSpec
		want  string
	}{
		{"empty specs → block (zero-value default)", nil, "block"},
		{"single advisory", []modelhook.RuleSpec{spec(string(modelhook.ModeAdvisory))}, "advisory"},
		{"single block", []modelhook.RuleSpec{spec(string(modelhook.ModeBlock))}, "block"},
		{"single empty-mode → block", []modelhook.RuleSpec{spec("")}, "block"},
		{"single unknown-mode → block", []modelhook.RuleSpec{spec("nuke")}, "block"},
		{"mixed advisory + block → block", []modelhook.RuleSpec{
			spec(string(modelhook.ModeAdvisory)), spec(string(modelhook.ModeBlock))}, "block"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := highestSeverityGuardrailMode(c.specs); got != c.want {
				t.Fatalf("highestSeverityGuardrailMode(%v) = %q, want %q", c.specs, got, c.want)
			}
		})
	}
}

// TestGuardrailsPostureLine directly table-tests the pure ON-posture helper (it was only
// covered indirectly via the diag double). Pins provenance per source, the rules count
// matching len(specs), and the default-set annotation across the usedDefaults matrix.
func TestGuardrailsPostureLine(t *testing.T) {
	advSpecs := []modelhook.RuleSpec{
		{Match: "WebSearch", Phases: []string{"pre", "post"}, Mode: string(modelhook.ModeAdvisory)},
		{Match: "WebFetch", Phases: []string{"post"}, Mode: string(modelhook.ModeAdvisory)},
		{Match: "mcp__*", Phases: []string{"pre", "post"}, Mode: string(modelhook.ModeAdvisory)},
	}
	blockSpecs := []modelhook.RuleSpec{
		{Match: "WebFetch", Phases: []string{"post"}, Mode: string(modelhook.ModeBlock)},
	}
	cases := []struct {
		name         string
		cfg          Config
		model        string
		src          guardrailSource
		specs        []modelhook.RuleSpec
		usedDefaults bool
		wantSubs     []string
		notSubs      []string
	}{
		{
			name:         "srcSlot → via slot `guardrail`",
			cfg:          Config{},
			model:        "slot-id",
			src:          srcSlot,
			specs:        advSpecs,
			usedDefaults: true,
			wantSubs:     []string{"checker=slot-id", "via slot `guardrail`", "rules=3"},
			notSubs:      []string{"supersedes gate value", "via --guardrails-model"},
		},
		{
			name:         "srcSlotSupersedingGate → names the gate value, %q-quoted",
			cfg:          Config{GuardrailsModel: "gate-id"},
			model:        "slot-id",
			src:          srcSlotSupersedingGate,
			specs:        advSpecs,
			usedDefaults: true,
			wantSubs:     []string{"via slot `guardrail`, supersedes gate value \"gate-id\""},
			notSubs:      []string{"via --guardrails-model"},
		},
		{
			name:         "srcGate → via --guardrails-model",
			cfg:          Config{GuardrailsModel: "gpt-5-mini"},
			model:        "gpt-5-mini",
			src:          srcGate,
			specs:        advSpecs,
			usedDefaults: true,
			wantSubs:     []string{"checker=gpt-5-mini", "via --guardrails-model"},
			notSubs:      []string{"via slot `guardrail`"},
		},
		{
			// srcNone is unreachable from logGuardrailsPosture now (the !configured
			// branch emits OFF), but the helper must stay robust: no provenance substring.
			name:         "srcNone → no provenance substring",
			cfg:          Config{},
			model:        "",
			src:          srcNone,
			specs:        advSpecs,
			usedDefaults: true,
			wantSubs:     []string{"checker= ()"},
			notSubs:      []string{"via slot", "via --guardrails-model", "supersedes"},
		},
		{
			name:         "usedDefaults=true → default set annotation present",
			cfg:          Config{},
			model:        "gpt-5-mini",
			src:          srcGate,
			specs:        advSpecs,
			usedDefaults: true,
			wantSubs:     []string{"default set"},
		},
		{
			name:         "usedDefaults=false → explicit rules, no default set annotation",
			cfg:          Config{},
			model:        "gpt-5-mini",
			src:          srcGate,
			specs:        blockSpecs,
			usedDefaults: false,
			wantSubs:     []string{"mode=block", "rules=1"},
			notSubs:      []string{"default set"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line := guardrailsPostureLine(c.cfg, c.model, c.src, c.specs, c.usedDefaults)
			for _, sub := range c.wantSubs {
				if !strings.Contains(line, sub) {
					t.Fatalf("missing substring %q; line=%q", sub, line)
				}
			}
			for _, sub := range c.notSubs {
				if strings.Contains(line, sub) {
					t.Fatalf("unexpected substring %q; line=%q", sub, line)
				}
			}
		})
	}
}

// TestGuardrailsPostureLineStatesYoloDemotion (ADR 0062, item 10): the startup posture
// line must SURFACE the yolo advisory demotion so an operator sees the security
// downgrade in the log, not only in docs. Non-yolo tiers must NOT carry the note.
func TestGuardrailsPostureLineStatesYoloDemotion(t *testing.T) {
	specs := []modelhook.RuleSpec{{Match: "Shell", Phases: []string{"pre"}, Mode: string(modelhook.ModeAdvisory)}}
	yolo := guardrailsPostureLine(Config{Posture: PostureYolo}, "m", srcGate, specs, false)
	if !strings.Contains(yolo, "DEMOTED to advisory by posture yolo") {
		t.Fatalf("the yolo posture line must state the advisory demotion; line=%q", yolo)
	}
	for _, p := range []Posture{PostureStrict, PostureTrusted, PostureAuto} {
		line := guardrailsPostureLine(Config{Posture: p}, "m", srcGate, specs, false)
		if strings.Contains(line, "DEMOTED to advisory by posture yolo") {
			t.Fatalf("posture %s must NOT carry the yolo demotion note; line=%q", p, line)
		}
	}
}
