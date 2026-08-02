package prompt

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// Cap values: the SOUL body cap is 20 KiB (internal/adapter/soul/store.go). Rules
// are project-scoped AND can be MULTIPLE, so mirror the soul's per-body discipline
// with BOTH a per-rule body cap (MaxRuleBytes, on the port side — the
// MaxAgentBodyBytes precedent) and a COMBINED cap across all injected rules — the
// combined cap is the one that bounds the turn-0 prefix tax. A count cap keeps a
// directory of tiny rules from flooding context.
const (
	defaultMaxRulesBytes = 40 * 1024 // combined ceiling across all injected rules
	defaultMaxRulesCount = 32        // count ceiling
)

// RulesAssembler renders all discovered rules as a single user-role message
// recorded once at turn 0 (via the InstructionAssembler seam). It rides AFTER the
// cache-stable system prefix, so it never touches prompt.Build's StablePrefix —
// the prompt-cache invariant (gauntlet #6) is untouched. Each rule is fenced in a
// <rule name="...">...</rule> block, with an "Applies when:" condition rendered
// from the rule's Paths. Rules are injected in the order ListRules returns them
// (name-sorted at the source).
//
// It FAILS SOFT: a nil source, a source error, or an empty rule set yields no
// message and no error — rules are best-effort project guidance, not correctness,
// so a fault must never abort a run.
type RulesAssembler struct {
	// Src is the rules source; nil makes the assembler a no-op (rules disabled).
	Src RulesSource
	// MaxBytes caps the total rendered body size (header + all fenced rules);
	// 0 uses the default (defaultMaxRulesBytes).
	MaxBytes int
	// MaxCount caps how many rules are rendered; 0 uses the default
	// (defaultMaxRulesCount).
	MaxCount int
}

// Compile-time assertion that RulesAssembler satisfies the interface.
var _ InstructionAssembler = RulesAssembler{}

// Assemble renders the discovered rules into one user-role message. The workspace
// is unused: rules are resolved against their source (filesystem / driver), not the
// session workspace root. It fails soft on a nil source, a source error, or an
// empty rule set.
func (a RulesAssembler) Assemble(ctx context.Context, _ tool.Workspace) ([]session.Message, error) {
	if a.Src == nil {
		return nil, nil
	}
	rules, err := a.Src.ListRules(ctx)
	if err != nil {
		// Best-effort context: never fail a run on a rules fault.
		return nil, nil
	}
	if len(rules) == 0 {
		return nil, nil
	}

	text := renderRules(rules, a.maxBytes(), a.maxCount())
	if text == "" {
		return nil, nil
	}
	return []session.Message{session.NewUserMessage(text)}, nil
}

func (a RulesAssembler) maxBytes() int {
	if a.MaxBytes > 0 {
		return a.MaxBytes
	}
	return defaultMaxRulesBytes
}

func (a RulesAssembler) maxCount() int {
	if a.MaxCount > 0 {
		return a.MaxCount
	}
	return defaultMaxRulesCount
}

// sanitizeRuleName makes a rule name safe to embed in the <rule name="...">
// fence: the name comes from a filename stem, which on most filesystems may
// legally contain `"`, `<`, `>`, or a literal newline — any of which would
// break the fence's structure for the model (CWE-74 / LLM01 hardening; the
// trust gate is the real boundary, this keeps the fence well-formed).
func sanitizeRuleName(name string) string {
	r := strings.NewReplacer(
		`"`, `'`,
		"<", "",
		">", "",
		"\n", " ",
		"\r", " ",
	)
	return r.Replace(name)
}

// renderRules renders the rules slice as a single fenced block per rule, under the
// shared rulesHeader (turn0.go). It stops adding rules when the combined byte cap
// or count cap is reached, and appends a footer listing the dropped count.
func renderRules(rules []Rule, maxBytes, maxCount int) string {
	total := len(rules)
	if total == 0 {
		return ""
	}

	// rulesHeader is the package-level const (turn0.go) — the single source of
	// truth shared with the IsInjectedTurn0Fragment predicate.
	var b strings.Builder
	b.WriteString(rulesHeader)

	shown := 0
	for _, r := range rules {
		if shown >= maxCount {
			fmt.Fprintf(&b, "\n...(%d more rule(s) not shown; rules beyond the cap were dropped)\n",
				total-shown)
			break
		}

		// Build the next rule block to measure before committing.
		var ruleB strings.Builder
		ruleB.WriteByte('\n')
		fmt.Fprintf(&ruleB, `<rule name="%s">`, sanitizeRuleName(r.Name))
		ruleB.WriteByte('\n')
		if len(r.Paths) == 0 {
			ruleB.WriteString("Applies when: (always)\n")
		} else {
			ruleB.WriteString("Applies when: ")
			ruleB.WriteString(strings.Join(r.Paths, ", "))
			ruleB.WriteByte('\n')
		}
		ruleB.WriteString(r.Body)
		// Ensure the body is separated from the closing fence by a newline if it
		// does not already end with one.
		if !strings.HasSuffix(r.Body, "\n") {
			ruleB.WriteByte('\n')
		}
		ruleB.WriteString("</rule>")

		next := ruleB.String()
		if b.Len()+len(next) > maxBytes {
			fmt.Fprintf(&b, "\n...(%d more rule(s) not shown; rules beyond the cap were dropped)\n",
				total-shown)
			break
		}
		b.WriteString(next)
		shown++

	}

	return b.String()
}
