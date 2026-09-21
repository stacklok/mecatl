package permconfig

import (
	"math"
	"strings"
	"testing"
)

func TestJevRouterStrictSchema(t *testing.T) {
	for _, tc := range []struct{ name, yaml string }{
		{"unknown router key", "models:\n  router:\n    disabled: true\n    unknown: value\n"},
		{"wrong jev shape", "models:\n  router:\n    disabled: true\n    jev: scalar\n"},
		{"unknown backend", "models:\n  router:\n    disabled: true\n    backend: other\n"},
		{"negative confidence", "models:\n  router:\n    disabled: true\n    jev:\n      minimum-confidence: -0.1\n"},
		{"over confidence", "models:\n  router:\n    jev:\n      minimum-confidence: 1.1\n"},
		{"nan confidence", "models:\n  router:\n    jev:\n      minimum-confidence: .nan\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseYAML([]byte(tc.yaml)); err == nil {
				t.Fatal("invalid router schema accepted")
			}
		})
	}
	cfg, err := parseYAML([]byte("models:\n  router:\n    backend: jev\n    jev:\n      model: custom\n      base-url: https://jev.example.com\n      minimum-confidence: 0.25\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models == nil || cfg.Models.Router == nil || cfg.Models.Router.Jev == nil {
		t.Fatal("valid Jev block not parsed")
	}
	r := cfg.Models.Router
	if r.Backend != "jev" || r.Jev.Model != "custom" || r.Jev.BaseURL != "https://jev.example.com" || r.Jev.MinimumConfidence != 0.25 {
		t.Fatalf("router = %+v", r)
	}
	if math.IsNaN(r.Jev.MinimumConfidence) || strings.TrimSpace(r.Backend) == "" {
		t.Fatal("validated values were not retained")
	}
}
