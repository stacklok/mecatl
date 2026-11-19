package publication_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

type step struct {
	ID      string            `yaml:"id"`
	Uses    string            `yaml:"uses"`
	If      string            `yaml:"if"`
	Run     string            `yaml:"run"`
	Timeout int               `yaml:"timeout-minutes"`
	With    map[string]any    `yaml:"with"`
	Env     map[string]string `yaml:"env"`
}

type job struct {
	Env         map[string]string `yaml:"env"`
	Secrets     any               `yaml:"secrets"`
	RunsOn      any               `yaml:"runs-on"`
	Needs       any               `yaml:"needs"`
	Uses        string            `yaml:"uses"`
	If          string            `yaml:"if"`
	Environment string            `yaml:"environment"`
	Permissions map[string]string `yaml:"permissions"`
	With        map[string]string `yaml:"with"`
	Outputs     map[string]string `yaml:"outputs"`
	Steps       []step            `yaml:"steps"`
	Timeout     int               `yaml:"timeout-minutes"`
}

type workflow struct {
	Env         map[string]string `yaml:"env"`
	Secrets     any               `yaml:"secrets"`
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]job    `yaml:"jobs"`
	On          struct {
		Dispatch struct {
			Inputs map[string]struct {
				Type    string `yaml:"type"`
				Default any    `yaml:"default"`
			} `yaml:"inputs"`
		} `yaml:"workflow_dispatch"`
		Call struct {
			Secrets any `yaml:"secrets"`
			Outputs map[string]struct {
				Value string `yaml:"value"`
			} `yaml:"outputs"`
		} `yaml:"workflow_call"`
	} `yaml:"on"`
}

func load(t *testing.T, name string) workflow {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../workflows/", name+".yml"))
	if err != nil {
		t.Fatal(err)
	}
	var result workflow
	if err := yaml.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Permissions) != 1 || result.Permissions["contents"] != "read" {
		t.Fatal("workflow must default to contents:read only")
	}
	for name, job := range result.Jobs {
		if len(job.Permissions) != 0 {
			t.Fatalf("%s unexpectedly overrides GITHUB_TOKEN permissions", name)
		}
	}
	return result
}

func credential(key, value string) bool {
	text := strings.ToLower(key + " " + value)
	for _, marker := range []string{"token", "secret", "password", "credential", "private-key", "private_key", "app_", "app-", "steps.app."} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isolation(w workflow) error {
	if w.Secrets != nil || w.On.Call.Secrets != nil {
		return fmt.Errorf("workflow must not accept or forward secrets")
	}
	for key, value := range w.Env {
		if credential(key, value) {
			return fmt.Errorf("workflow credential env: %s", key)
		}
	}
	for name, j := range w.Jobs {
		if j.Secrets != nil {
			return fmt.Errorf("%s forwards reusable secrets", name)
		}
		if j.Uses == "" && j.RunsOn != "ubuntu-24.04" {
			return fmt.Errorf("%s must use a fresh hosted ubuntu-24.04 runner", name)
		}
		for key, value := range j.Env {
			if credential(key, value) {
				return fmt.Errorf("%s job credential env: %s", name, key)
			}
		}
		for key, value := range j.With {
			if credential(key, value) {
				return fmt.Errorf("%s forwards credential input: %s", name, key)
			}
		}
		operation := map[string]string{"publish": "publish", "adoption-pr": "open-pr"}[name]
		for _, s := range j.Steps {
			for key, value := range s.Env {
				if !credential(key, value) {
					continue
				}
				if operation == "" || key != "GH_TOKEN" || value != "${{ steps.app.outputs.token }}" ||
					!strings.HasPrefix(s.Run, "python3 tooling/.github/scripts/module-publication.py "+operation+" ") {
					return fmt.Errorf("%s credential outside exact writer helper step: %s", name, key)
				}
			}
			for key, value := range s.With {
				if key == "persist-credentials" && value == false {
					continue
				}
				text := fmt.Sprint(value)
				if operation != "" && strings.HasPrefix(s.Uses, "actions/create-github-app-token@") &&
					((key == "private-key" && text == "${{ secrets.RELEASE_APP_PRIVATE_KEY }}") ||
						(key == "client-id" && text == "${{ vars.RELEASE_APP_CLIENT_ID }}")) {
					continue
				}
				if credential(key, text) {
					return fmt.Errorf("%s hidden credential action input: %s", name, key)
				}
			}
		}
	}
	return nil
}

func TestPublicationRunnerAndCredentialIsolation(t *testing.T) {
	for _, name := range []string{"publish-module", "publish-modules"} {
		if err := isolation(load(t, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPublicationIsolationMutations(t *testing.T) {
	for _, mutation := range []struct {
		name string
		edit func(*workflow)
	}{
		{"missing runner", func(w *workflow) { j := w.Jobs["publish"]; j.RunsOn = nil; w.Jobs["publish"] = j }},
		{"self hosted", func(w *workflow) {
			j := w.Jobs["prove"]
			j.RunsOn = []any{"self-hosted", "ubuntu-24.04"}
			w.Jobs["prove"] = j
		}},
		{"workflow token", func(w *workflow) { w.Env["GH_TOKEN"] = "${{ github.token }}" }},
		{"job secret", func(w *workflow) {
			j := w.Jobs["prepare"]
			j.Env = map[string]string{"APP_KEY": "${{ secrets.RELEASE_APP_PRIVATE_KEY }}"}
			w.Jobs["prepare"] = j
		}},
		{"step token", func(w *workflow) { w.Jobs["prove"].Steps[0].Env = map[string]string{"GH_TOKEN": "${{ github.token }}"} }},
		{"hidden action token", func(w *workflow) { w.Jobs["prepare"].Steps[0].With["token"] = "${{ github.token }}" }},
		{"hidden innocuous input", func(w *workflow) { w.Jobs["prove"].Steps[0].With["data"] = "${{ secrets.RELEASE_APP_PRIVATE_KEY }}" }},
		{"writer token on checkout", func(w *workflow) {
			w.Jobs["publish"].Steps[0].Env = map[string]string{"GH_TOKEN": "${{ steps.app.outputs.token }}"}
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			w := load(t, "publish-module")
			if err := isolation(w); err != nil {
				t.Fatalf("invalid baseline: %v", err)
			}
			mutation.edit(&w)
			if isolation(w) == nil {
				t.Fatal("planted isolation violation was accepted")
			}
		})
	}
	for _, secrets := range []any{"inherit", map[string]any{"key": "${{ secrets.RELEASE_APP_PRIVATE_KEY }}"}} {
		w := load(t, "publish-modules")
		j := w.Jobs["engine"]
		j.Secrets = secrets
		w.Jobs["engine"] = j
		if isolation(w) == nil {
			t.Fatal("reusable secret forwarding was accepted")
		}
	}
}

func needs(j job, want string) bool {
	if one, ok := j.Needs.(string); ok {
		return one == want
	}
	list, _ := j.Needs.([]any)
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func TestPublicationProofChain(t *testing.T) {
	w := load(t, "publish-modules")
	modules := []string{"agentdef", "engine", "contracts/gen/go/mecatl/driver", "provider/ssefilter", "internal/adaptersupport", "authn/oidc", "provider/anthropic", "provider/openai", "provider/openaichat", "adapters"}
	names := []string{"agentdef", "engine", "driver", "ssefilter", "adaptersupport", "oidc", "anthropic", "openai", "openaichat", "adapters"}
	previous := "preflight"
	for i, name := range names {
		j := w.Jobs[name]
		condition := ""
		if i == 0 {
			condition = "${{ !inputs.preflight_only }}"
		}
		if j.Uses != "./.github/workflows/publish-module.yml" || j.With["module"] != modules[i] || !needs(j, previous) || j.If != condition {
			t.Fatalf("%s must follow %s without selection skips or failure bypasses", name, previous)
		}
		parent := "${{ needs." + previous + ".outputs.head }}"
		if i == 0 {
			parent = "${{ needs.preflight.outputs.source }}"
		}
		if j.With["parent"] != parent {
			t.Fatalf("%s parent does not come from preceding proof", name)
		}
		previous = name
	}
	for name, prerequisite := range map[string]string{"reconcile": "adapters", "adoption-pr": "reconcile"} {
		j := w.Jobs[name]
		if !needs(j, prerequisite) || j.If != "${{ !inputs.preflight_only }}" {
			t.Fatalf("%s bypasses preflight-only, public proof or reconciliation failure", name)
		}
	}
	one := load(t, "publish-module")
	if one.On.Call.Outputs["head"].Value != "${{ jobs.prove.outputs.head }}" {
		t.Fatal("reusable workflow leaked writer head before proof")
	}
	prove := one.Jobs["prove"]
	if !needs(prove, "prepare") || !needs(prove, "publish") || !needs(one.Jobs["publish"], "prepare") {
		t.Fatal("prepare/test, writer and public proof must be separate sequential jobs")
	}
	for _, condition := range []string{"!cancelled()", "needs.prepare.result == 'success'", "needs.prepare.outputs.selected == 'true' && needs.publish.result == 'success'", "needs.prepare.outputs.selected == 'false' && needs.publish.result == 'skipped'"} {
		if !strings.Contains(prove.If, condition) {
			t.Fatalf("missing proof gate %s", condition)
		}
	}
	if prove.Outputs["head"] != "${{ steps.prove.outputs.head }}" || one.Jobs["publish"].If != "needs.prepare.outputs.selected == 'true'" {
		t.Fatal("selected publish/proof output gate drift")
	}
}

func TestPublicationPreflightDiagnostics(t *testing.T) {
	w := load(t, "publish-modules")
	for _, name := range []string{"preflight_only", "verbose"} {
		input, ok := w.On.Dispatch.Inputs[name]
		if !ok || input.Type != "boolean" || input.Default != false {
			t.Fatalf("%s must be an explicit boolean defaulting to false", name)
		}
	}
	if w.Jobs["preflight"].If != "github.ref == 'refs/heads/main'" || w.Jobs["preflight"].Timeout != 75 {
		t.Fatal("preflight must retain main-only authority with a 75-minute outer bound")
	}
	for name, j := range w.Jobs {
		switch name {
		case "preflight", "summary":
		case "agentdef", "reconcile", "adoption-pr":
			if j.If != "${{ !inputs.preflight_only }}" {
				t.Fatalf("%s must be disabled in diagnostic mode", name)
			}
		default:
			// Default success() propagation must skip every remaining reusable call.
			if j.Uses != "./.github/workflows/publish-module.yml" || j.If != "" {
				t.Fatalf("%s can bypass the skipped chain", name)
			}
		}
	}
	summary := w.Jobs["summary"]
	if summary.If != "always()" || len(summary.Steps) != 1 || summary.Steps[0].Env["PREFLIGHT_ONLY"] != "${{ inputs.preflight_only }}" {
		t.Fatal("summary must run even when the publication chain is skipped and report the mode")
	}
	var commands []step
	for _, s := range w.Jobs["preflight"].Steps {
		if strings.HasPrefix(s.Run, "python3 tooling/.github/scripts/module-publication.py preflight ") {
			commands = append(commands, s)
		}
	}
	if len(commands) != 3 {
		t.Fatal("preflight needs three native command steps")
	}
	for i, phase := range []string{"validate", "workspace", "final"} {
		s := commands[i]
		if s.Timeout != 30 || s.If != "" || !strings.Contains(s.Run, "--phase "+phase+" ") ||
			!strings.Contains(s.Run, `--environment-dir "$RUNNER_TEMP/mp"`) {
			t.Fatalf("%s lost step ordering, success gating, bounds or shared environment", phase)
		}
		if (s.ID == "preflight") != (phase == "final") {
			t.Fatal("only final source integrity may expose publication outputs")
		}
	}
	preflight := w.Jobs["preflight"]
	if preflight.Env["SOURCE"] != "${{ inputs.source }}" || preflight.Env["VERSIONS"] != "${{ inputs.versions }}" || preflight.Env["VERBOSE"] != "${{ inputs.verbose }}" {
		t.Fatal("preflight must retain exact dispatch inputs across steps")
	}
	for _, key := range []string{"source", "versions", "branch"} {
		if preflight.Outputs[key] != "${{ steps.preflight.outputs."+key+" }}" {
			t.Fatalf("%s bypasses final integrity outputs", key)
		}
	}
	data, err := json.Marshal(map[string]any{"preflight": commands, "summary": summary.Steps[0]})
	if err != nil {
		t.Fatal(err)
	}
	_, file, _, _ := runtime.Caller(0)
	cmd := exec.Command("python3", filepath.Join(filepath.Dir(file), "module-publication_test.py"), "DiagnosticsWorkflowTests", "LocalTests.test_preflight_workflow_boundaries")
	cmd.Env = append(os.Environ(), "PUBLICATION_DIAGNOSTICS_WORKFLOW="+string(data), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("diagnostics shell boundary: %v\n%s", err, output)
	}
}

func TestPublicationWriterIsolation(t *testing.T) {
	w := load(t, "publish-modules")
	one := load(t, "publish-module")
	for name, j := range map[string]job{"publish": one.Jobs["publish"], "open-pr": w.Jobs["adoption-pr"]} {
		t.Run(name, func(t *testing.T) {
			if j.Environment != "release" {
				t.Fatal("writer must be environment-bound")
			}
			checkouts, apps, downloads, commands := 0, 0, 0, 0
			for _, s := range j.Steps {
				if s.Run != "" {
					commands++
					if !strings.HasPrefix(s.Run, "python3 tooling/.github/scripts/module-publication.py "+name+" ") || strings.Contains(s.Run, "${{") {
						t.Fatalf("writer runs candidate or interpolated commands: %s", s.Run)
					}
					if s.Env["GH_TOKEN"] != "${{ steps.app.outputs.token }}" {
						t.Fatal("writer credential must be scoped to helper step")
					}
					continue
				}
				if !strings.Contains(s.Uses, "@") || len(strings.Split(s.Uses, "@")[1]) != 40 {
					t.Fatalf("unpinned writer action: %s", s.Uses)
				}
				switch strings.Split(s.Uses, "@")[0] {
				case "actions/checkout":
					checkouts++
					if s.With["ref"] != "${{ github.sha }}" || s.With["path"] != "tooling" || s.With["persist-credentials"] != false {
						t.Fatal("writer checkout must be immutable trusted tooling only")
					}
				case "actions/download-artifact":
					downloads++
					if s.With["artifact-ids"] == nil || s.With["name"] != nil || s.With["run-id"] != nil || s.With["github-token"] != nil {
						t.Fatal("writer must download by current-run artifact ID")
					}
				case "actions/create-github-app-token":
					apps++
					if s.With["owner"] != "${{ github.repository_owner }}" || s.With["repositories"] != "${{ github.event.repository.name }}" || s.With["permission-contents"] != "write" {
						t.Fatal("writer token is not repository-scoped contents:write")
					}
					if (s.With["permission-pull-requests"] == "write") != (name == "open-pr") {
						t.Fatal("only final writer may create PRs")
					}
				default:
					t.Fatalf("unexpected writer action (no candidate, Go, Task or caches): %s", s.Uses)
				}
			}
			if checkouts != 1 || apps != 1 || downloads != 1 || commands != 1 {
				t.Fatal("writer surface grew beyond tooling, artifact, token, helper")
			}
		})
	}
}

func TestPublicationCommandsOffline(t *testing.T) {
	one := load(t, "publish-module")
	many := load(t, "publish-modules")
	commands := map[string]step{}
	environments := map[string]map[string]string{}
	for _, j := range []job{one.Jobs["prepare"], one.Jobs["publish"], one.Jobs["prove"], many.Jobs["reconcile"], many.Jobs["adoption-pr"]} {
		for _, s := range j.Steps {
			for _, operation := range []string{"prepare", "publish", "prove", "reconcile", "open-pr"} {
				if strings.HasPrefix(s.Run, "python3 tooling/.github/scripts/module-publication.py "+operation+" ") && !strings.Contains(s.Run, "--selection-only") {
					commands[operation] = s
					env := map[string]string{}
					base := one.Env
					if operation == "reconcile" || operation == "open-pr" {
						base = many.Env
					}
					for key, value := range base {
						env[key] = value
					}
					for key, value := range j.Env {
						env[key] = value
					}
					environments[operation] = env
				}
			}
		}
	}
	if len(commands) != 5 {
		t.Fatal("missing publication helper commands")
	}
	_, file, _, _ := runtime.Caller(0)
	for _, mutation := range []string{"", "prove passthrough", "stale HEAD", "reconcile parent", "adoption parent"} {
		t.Run("commands/"+mutation, func(t *testing.T) {
			copyCommands := make(map[string]step, len(commands))
			copyEnvironments := map[string]map[string]string{}
			for name, s := range commands {
				copyCommands[name] = s
				copyEnvironments[name] = map[string]string{}
				for key, value := range environments[name] {
					copyEnvironments[name][key] = value
				}
			}
			s := copyCommands["prove"]
			switch mutation {
			case "prove passthrough":
				s.Run = `echo "head=$HEAD" >> "$GITHUB_OUTPUT"`
			case "stale HEAD":
				s.Env = map[string]string{"HEAD": "${{ inputs.parent }}"}
			case "reconcile parent":
				copyEnvironments["reconcile"]["PARENT"] = "${{ needs.preflight.outputs.source }}"
			case "adoption parent":
				copyEnvironments["open-pr"]["PARENT"] = "${{ needs.preflight.outputs.source }}"
			}
			copyCommands["prove"] = s
			data, err := json.Marshal(map[string]any{"commands": copyCommands, "environments": copyEnvironments})
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("python3", filepath.Join(filepath.Dir(file), "module-publication-writer_test.py"), "WorkflowTests")
			cmd.Env = append(os.Environ(), "PUBLICATION_WORKFLOW="+string(data), "PYTHONDONTWRITEBYTECODE=1")
			output, err := cmd.CombinedOutput()
			if mutation == "" && err != nil {
				t.Fatalf("offline workflow commands: %v\n%s", err, output)
			}
			if mutation != "" {
				want := map[string]string{
					"prove passthrough": "workflow bypassed the publication helper",
					"stale HEAD":        "public module tag does not identify the expected head",
					"reconcile parent":  "workflow parent does not match preceding proof",
					"adoption parent":   "workflow parent does not match preceding proof",
				}[mutation]
				if err == nil || !strings.Contains(string(output), want) {
					t.Fatalf("planted proof bypass must fail at %q: %v\n%s", want, err, output)
				}
			}
		})
	}
}

func TestPublicationReadOnlyIsolation(t *testing.T) {
	w := load(t, "publish-modules")
	one := load(t, "publish-module")
	for name, j := range map[string]job{"preflight": w.Jobs["preflight"], "reconcile": w.Jobs["reconcile"], "prepare": one.Jobs["prepare"], "prove": one.Jobs["prove"]} {
		if j.Environment != "" {
			t.Fatalf("%s must never receive release environment", name)
		}
		for _, s := range j.Steps {
			if strings.Contains(s.Uses, "create-github-app-token") {
				t.Fatalf("%s must not mint tokens", name)
			}
			if strings.Contains(s.Uses, "actions/checkout@") && (s.With["persist-credentials"] != false || (s.With["path"] == "tooling" && s.With["ref"] != "${{ github.sha }}")) {
				t.Fatalf("%s checkout trust drift", name)
			}
			if strings.Contains(s.Uses, "setup-go@") && s.With["cache"] != false {
				t.Fatalf("%s must use a fresh public cache", name)
			}
			if name == "prepare" && (s.With["path"] == "candidate" || strings.Contains(s.Uses, "setup-go@")) && s.If != "steps.select.outputs.selected == 'true'" {
				t.Fatal("unselected prepare must skip candidate checkout and Go setup")
			}
			if strings.Contains(s.Run, "${{") {
				t.Fatalf("%s interpolates inputs into shell code", name)
			}
		}
	}
}
