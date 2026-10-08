package permconfig

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestProjectInstructionMaxBytesOperatorYAML(t *testing.T) {
	yamlPolicy := "harness_context:\n  project_instruction_max_bytes: %s\n  kinds:\n    instructions: {mode: combine}\n    commands: {mode: combine}\n    rules: {mode: combine}\n    skills: {mode: combine}\n    agent_defs: {mode: combine}\n"
	for _, tc := range []struct {
		raw   string
		valid bool
		want  int
	}{
		{"65537", true, 65537}, {"0", false, 0}, {"-1", false, 0}, {"2.1", false, 0}, {"true", false, 0}, {"999999999999999999999999999999999999", false, 0},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var cfg Config
			err := yaml.Unmarshal([]byte(strings.Replace(yamlPolicy, "%s", tc.raw, 1)), &cfg)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if tc.valid && (cfg.HarnessContext == nil || cfg.HarnessContext.ProjectInstructionMaxBytes == nil || *cfg.HarnessContext.ProjectInstructionMaxBytes != tc.want) {
				t.Fatalf("parsed=%+v", cfg.HarnessContext)
			}
		})
	}
}
