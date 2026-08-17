package learning

import "fmt"

// SkillActivationPolicy controls the assurance required for automatic learned-skill activation.
type SkillActivationPolicy string

const (
	// SkillActivationEvaluated requires a trusted evaluator PASS. It is the zero
	// value to preserve the historical engine/embedder policy.
	SkillActivationEvaluated SkillActivationPolicy = "evaluated"
	// SkillActivationValidated permits structurally validated, evidence-backed
	// ABSTAIN candidates to activate without granting any additional capability.
	SkillActivationValidated SkillActivationPolicy = "validated"
)

// ParseSkillActivationPolicy parses the closed activation-policy vocabulary.
func ParseSkillActivationPolicy(value string) (SkillActivationPolicy, error) {
	policy := SkillActivationPolicy(value)
	if !policy.Valid() {
		return "", fmt.Errorf("learning: invalid skill activation policy %q (want validated or evaluated)", value)
	}
	return policy, nil
}

// Valid reports whether policy belongs to the closed vocabulary.
func (p SkillActivationPolicy) Valid() bool {
	return p == SkillActivationValidated || p == SkillActivationEvaluated
}

// Effective returns evaluated for the zero value.
func (p SkillActivationPolicy) Effective() SkillActivationPolicy {
	if p == "" {
		return SkillActivationEvaluated
	}
	return p
}

// String returns the configuration token, treating zero as evaluated.
func (p SkillActivationPolicy) String() string { return string(p.Effective()) }
