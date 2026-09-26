package permconfig

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

func TestConfigurableCommitCoauthorGuidance_Scenario2_OperatorOptOut(t *testing.T) {
	const userPath = "/cfg/mecatl/settings.yaml"
	for _, tc := range []struct {
		name string
		cli  string
		user string
		want *bool
	}{
		{name: "absent", want: nil},
		{name: "user opt-out", user: "system_prompt:\n  commit_coauthor: false\n", want: boolPtr(false)},
		{name: "explicit opt-out", cli: "system_prompt:\n  commit_coauthor: false\n", want: boolPtr(false)},
		{name: "explicit overrides user", cli: "system_prompt:\n  commit_coauthor: true\n", user: "system_prompt:\n  commit_coauthor: false\n", want: boolPtr(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := xdgconfig.ResolveEnv{
				Getenv: func(key string) string {
					if key == "XDG_CONFIG_HOME" {
						return "/cfg"
					}
					return ""
				},
				UserHomeDir: func() (string, error) { return "/home/operator", nil },
				ReadFile: func(path string) ([]byte, error) {
					switch path {
					case "/cli/settings.yaml":
						return []byte(tc.cli), nil
					case userPath:
						return []byte(tc.user), nil
					default:
						return nil, errors.New("not found")
					}
				},
			}
			opts := Options{Conventional: true}
			if tc.cli != "" {
				opts.ExplicitFiles = []string{"/cli/settings.yaml"}
			}
			r := newWithEnv(opts, env)
			if got := r.OperatorCommitCoauthor(); !sameBoolPointer(got, tc.want) {
				t.Fatalf("OperatorCommitCoauthor() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConfigurableCommitCoauthorGuidance_Scenario2_StrictSystemPromptConfig(t *testing.T) {
	if err := ValidateYAML([]byte("system_prompt:\n  unknown: true\n")); err == nil {
		t.Fatal("unknown system_prompt key must fail validation")
	}
}

func TestConfigurableCommitCoauthorGuidance_Scenario2_ProjectConfigIgnored(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)
	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, "system_prompt:\n  commit_coauthor: false\n")

	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	_ = r.Resolve(context.Background(), ws)

	if got := r.OperatorCommitCoauthor(); got != nil {
		t.Fatalf("project system_prompt must not be resolved as operator config; got %v", got)
	}
	log := buf.String()
	if !strings.Contains(log, "system_prompt: IGNORING a project-tier system_prompt block") {
		t.Fatalf("expected project-tier system_prompt warning; got:\n%s", log)
	}
	if strings.Contains(log, "commit_coauthor") || strings.Contains(log, "false") {
		t.Fatalf("project-tier system_prompt warning must be value-free; got:\n%s", log)
	}
}

func boolPtr(v bool) *bool { return &v }

func sameBoolPointer(got, want *bool) bool {
	return got == nil && want == nil || got != nil && want != nil && *got == *want
}
