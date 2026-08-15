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
	cfg, err := parseYAML([]byte("learning:\n  mode: auto\n  sensitivity: eager\n  automatic:\n    cooldown: 0s\n    window: 1h\n    max_reflections: 0\n    max_tokens: 100000\n    max_reflections_per_principal: 4\n    max_tokens_per_principal: 50000\n"))
	if err != nil || cfg.Learning.Sensitivity != "eager" || cfg.Learning.Automatic.MaxReflections != 0 || cfg.Learning.Automatic.Window != time.Hour {
		t.Fatalf("complete learning config = %+v, %v", cfg.Learning, err)
	}
	for _, body := range []string{
		"learning:\n  mode: automatic\n",
		"learning:\n  mode: AUTO\n",
		"learning:\n  mode: off\n  queue: x\n",
		"learning:\n  mode: off\n  sensitivity: fast\n",
		"learning:\n  mode: off\n  automatic:\n    window: 30s\n",
		"learning:\n  mode: off\n  automatic:\n    cooldown: -1s\n",
	} {
		if _, err := parseYAML([]byte(body)); err == nil {
			t.Fatalf("invalid learning config parsed: %q", body)
		}
	}
}
