package permconfig

import (
	"testing"
	"time"
)

func TestLearningStrictParse(t *testing.T) {
	for _, mode := range []string{"off", "review", "auto"} {
		cfg, err := parseYAML([]byte("learning:\n  mode: " + mode + "\n"))
		if err != nil || cfg.Learning == nil || cfg.Learning.Mode != mode {
			t.Fatalf("mode %q: cfg=%+v err=%v", mode, cfg, err)
		}
	}
	cfg, err := parseYAML([]byte("learning:\n  mode: auto\n  skills:\n    activation: validated\n  sensitivity: eager\n  automatic:\n    cooldown: 0s\n    window: 1h\n    max_reflections: 0\n    max_tokens: 100000\n    max_reflections_per_principal: 4\n    max_tokens_per_principal: 50000\n"))
	if err != nil || cfg.Learning.Skills == nil || cfg.Learning.Skills.Activation != "validated" || cfg.Learning.Sensitivity != "eager" || cfg.Learning.Automatic.MaxReflections != 0 || cfg.Learning.Automatic.Window != time.Hour {
		t.Fatalf("complete learning config = %+v, %v", cfg.Learning, err)
	}
	for _, body := range []string{
		"learning:\n  mode: auto\n  automatic:\n    cooldown: 30s\n",
		"learning:\n  mode: auto\n  automatic:\n    max_tokens_per_principal: 1234\n",
	} {
		partial, partialErr := parseYAML([]byte(body))
		if partialErr != nil {
			t.Fatalf("partial learning config %q: %v", body, partialErr)
		}
		if partial.Learning.Automatic.Window != time.Hour || partial.Learning.Automatic.MaxReflections != 8 || partial.Learning.Automatic.MaxTokens != 100000 || partial.Learning.Automatic.MaxReflectionsPerPrincipal != 4 {
			t.Fatalf("partial override lost defaults: %+v", partial.Learning.Automatic)
		}
	}
	for _, body := range []string{
		"learning:\n  mode: automatic\n",
		"learning:\n  mode: AUTO\n",
		"learning:\n  mode: off\n  queue: x\n",
		"learning:\n  mode: off\n  sensitivity: fast\n",
		"learning:\n  mode: off\n  automatic:\n    window: 30s\n",
		"learning:\n  mode: off\n  automatic:\n    cooldown: -1s\n",
		"learning:\n  mode: auto\n  skills:\n    activation: pass\n",
		"learning:\n  mode: auto\n  skills:\n    activation: validated\n    unknown: x\n",
		"learning:\n  mode: auto\n  skills: validated\n",
		"learning:\n  mode: auto\n  automatic:\n    cooldown: 1m\n    cooldown: 2m\n",
	} {
		if _, err := parseYAML([]byte(body)); err == nil {
			t.Fatalf("invalid learning config parsed: %q", body)
		}
	}
}
