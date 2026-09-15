package agent_test

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/agent"
)

func TestCommandRunnerEnvironment_Scenario2_ShellPromptContract(t *testing.T) {
	for name, description := range map[string]string{
		"agent":   agent.NewShellTool().Spec().Description,
		"fstools": fstools.NewShellTool().Spec().Description,
	} {
		for _, required := range []string{
			"Credential-shaped environment variables are scrubbed by default",
			"authentication failure does not prove that the operator is unauthenticated",
			"Never inspect, echo, copy, write, or commit credentials",
		} {
			if !strings.Contains(description, required) {
				t.Errorf("%s Shell description missing %q", name, required)
			}
		}
	}
}
