package executionkind_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestK8sWorkflowQualificationTriggers(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../.github/workflows/k8s-e2e.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On struct {
			PullRequest struct {
				Paths []string `yaml:"paths"`
			} `yaml:"pull_request"`
		} `yaml:"on"`
		Jobs map[string]struct {
			If string `yaml:"if"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(workflow.On.PullRequest.Paths, "Taskfile.yml") {
		t.Error("Taskfile-only PRs must qualify the tasks that invoke the Kubernetes suites")
	}
	// Pin the complete gate, including conjunctions, so a branch name or a
	// workflow rerun's actor cannot independently authorize a release skip.
	const want = "${{ !(github.event_name == 'pull_request' && github.event.pull_request.user.login == 'toolhive-release-app[bot]' && github.event.pull_request.head.repo.full_name == github.repository && startsWith(github.head_ref, 'release/')) }}"
	for _, name := range []string{"k8s-e2e", "execution-production-e2e"} {
		t.Run(name, func(t *testing.T) {
			if got := strings.Join(strings.Fields(workflow.Jobs[name].If), " "); got != want {
				t.Errorf("only same-repository release PRs authored by the release App may skip qualification; got gate %q", got)
			}
		})
	}
}
