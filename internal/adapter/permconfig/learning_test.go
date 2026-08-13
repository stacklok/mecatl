package permconfig

import "testing"

func TestLearningStrictParse(t *testing.T) {
	for _, mode := range []string{"off", "review", "auto"} {
		cfg, err := parseYAML([]byte("learning:\n  mode: " + mode + "\n"))
		if err != nil || cfg.Learning == nil || cfg.Learning.Mode != mode {
			t.Fatalf("mode %q: cfg=%+v err=%v", mode, cfg, err)
		}
	}
	for _, body := range []string{
		"learning:\n  mode: automatic\n",
		"learning:\n  mode: AUTO\n",
		"learning:\n  mode: off\n  queue: x\n",
	} {
		if _, err := parseYAML([]byte(body)); err == nil {
			t.Fatalf("invalid learning config parsed: %q", body)
		}
	}
}
