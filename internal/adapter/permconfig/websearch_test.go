package permconfig

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

func TestWebSearchStrictDecoding(t *testing.T) {
	cfg, err := parseYAML([]byte("websearch:\n  enabled: false\n  url: https://search.example/api\n  auth_header: X-Search-Key\n  query_param: query\n  searxng:\n    url: https://searx.example/search\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebSearch == nil || cfg.WebSearch.Enabled == nil || *cfg.WebSearch.Enabled || cfg.WebSearch.URL != "https://search.example/api" || cfg.WebSearch.AuthHeader != "X-Search-Key" || cfg.WebSearch.QueryParam != "query" || cfg.WebSearch.Searxng == nil || cfg.WebSearch.Searxng.URL != "https://searx.example/search" {
		t.Fatalf("websearch = %#v", cfg.WebSearch)
	}

	for _, input := range []string{
		"websearch: {enabled: false, typo: true}\n",
		"websearch: {searxng_url: https://searx.example/search}\n",
		"websearch: {searxng: {typo: true}}\n",
	} {
		if _, err := parseYAML([]byte(input)); err == nil {
			t.Fatalf("unknown websearch key was accepted: %s", input)
		}
	}
}

func TestOperatorWebSearchPrecedenceAndEnabledAbsence(t *testing.T) {
	env := xdgconfig.ResolveEnv{
		Getenv: func(key string) string {
			if key == "XDG_CONFIG_HOME" {
				return "/config"
			}
			return ""
		},
		UserHomeDir: func() (string, error) { return "/home/user", nil },
		ReadFile: func(path string) ([]byte, error) {
			switch path {
			case "/operator/settings.yaml":
				return []byte("websearch: {enabled: false, url: https://cli.example}\n"), nil
			case "/config/mecatl/settings.yaml":
				return []byte("websearch: {enabled: true, url: https://user.example}\n"), nil
			default:
				return nil, errors.New("not found")
			}
		},
	}
	r := newWithEnv(Options{Conventional: true, ExplicitFiles: []string{"/operator/settings.yaml"}}, env)
	if got := r.OperatorWebSearch(); got == nil || got.Enabled == nil || *got.Enabled || got.URL != "https://cli.example" {
		t.Fatalf("CLI websearch did not take precedence: %#v", got)
	}

	r = newWithEnv(Options{ExplicitFiles: []string{"/operator/settings.yaml"}}, envWithExplicit("/operator/settings.yaml", "websearch: {url: https://operator.example}\n"))
	if got := r.OperatorWebSearch(); got == nil || got.Enabled != nil {
		t.Fatalf("absent websearch.enabled = %#v, want nil", got)
	}
}

func TestMalformedProjectWebSearchPreservesPermissionRules(t *testing.T) {
	const secret = "project-websearch-value-MUST-NOT-LEAK"
	var logs bytes.Buffer
	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, "permissions:\n  deny: [\"Shell(rm:*)\"]\n  ask: [\"Shell(git push:*)\"]\nwebsearch:\n  enabled: not-a-bool\n  typo: "+secret+"\n")

	r := newWithEnv(Options{Conventional: true, Diagnostics: slogdiag.New(&logs, false, port.LevelDebug)}, fakeEnv())
	rules := r.Resolve(context.Background(), ws)
	if got := findRule(rules, "Shell", "rm*"); got == nil || got.Effect != governance.Deny {
		t.Fatalf("project deny was lost: %#v", rules)
	}
	if got := findRule(rules, "Shell", "git push*"); got == nil || got.Effect != governance.Ask {
		t.Fatalf("project ask was lost: %#v", rules)
	}
	if r.OperatorWebSearch() != nil {
		t.Fatal("project websearch became operator configuration")
	}
	if log := logs.String(); !strings.Contains(log, "IGNORING project-tier websearch block") || strings.Contains(log, secret) {
		t.Fatalf("websearch warning missing or leaked a value: %s", log)
	}
}
