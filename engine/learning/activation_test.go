package learning_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/learning"
)

func TestSkillActivationPolicy(t *testing.T) {
	if got := (learning.SkillActivationPolicy("")).Effective(); got != learning.SkillActivationEvaluated {
		t.Fatalf("zero policy = %q", got)
	}
	for _, want := range []learning.SkillActivationPolicy{learning.SkillActivationValidated, learning.SkillActivationEvaluated} {
		got, err := learning.ParseSkillActivationPolicy(want.String())
		if err != nil || got != want || !got.Valid() {
			t.Fatalf("parse %q = %q, %v", want, got, err)
		}
	}
	if _, err := learning.ParseSkillActivationPolicy("pass"); err == nil {
		t.Fatal("invalid policy accepted")
	}
}
