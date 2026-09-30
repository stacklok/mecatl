package permconfig

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

func TestADR_0369_Scenario1_AliasGrammar(t *testing.T) {
	cfg, err := parseYAML([]byte(`models:
  aliases:
    scalar: opaque/model:id
    pair:
      provider: anthropic
      model: opaque model id
`))
	if err != nil {
		t.Fatalf("parse valid aliases: %v", err)
	}
	if got := cfg.Models.Aliases["scalar"]; got.Provider != "" || got.Model != "opaque/model:id" {
		t.Fatalf("scalar alias = %+v", got)
	}
	if got := cfg.Models.Aliases["pair"]; got.Provider != "anthropic" || got.Model != "opaque model id" {
		t.Fatalf("pair alias = %+v", got)
	}

	for _, body := range []string{
		"pair: {}",
		"pair: {provider: anthropic}",
		"pair: {model: opaque}",
		"pair: {provider: '', model: opaque}",
		"pair: {provider: anthropic, model: ''}",
		"pair: {provider: anthropic, model: opaque, extra: nope}",
		"pair: [anthropic, opaque]",
	} {
		t.Run(body, func(t *testing.T) {
			if _, err := parseYAML([]byte("models:\n  aliases:\n    " + body + "\n")); err == nil {
				t.Fatal("malformed alias accepted")
			}
		})
	}
}

func TestADR_0369_Scenario1_ProjectModelsDisabled(t *testing.T) {
	for _, trust := range []bool{false, true} {
		t.Run(map[bool]string{false: "untrusted", true: "trusted"}[trust], func(t *testing.T) {
			var logs bytes.Buffer
			r := newWithEnv(Options{Conventional: true, TrustProject: trust, Diagnostics: slogdiag.New(&logs, false, port.LevelDebug)}, fakeEnv())
			ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
			const opaque = `models:
  this is not a known models key:
    malformed: [still, ignored]
permissions:
  deny: [Shell]
`
			ws.seed(t, projectFileMecatl, opaque)
			rules := r.Resolve(context.Background(), ws)
			if len(rules) == 0 {
				t.Fatal("whole-document decoding lost valid sibling permissions")
			}
			if got := r.ProjectModelBindings(ws); got != nil {
				t.Fatalf("project models affected configuration: %+v", got)
			}
			log := logs.String()
			if got := strings.Count(log, "IGNORING project-tier models block"); got != 1 {
				t.Fatalf("models warning count = %d, log=%s", got, log)
			}
			if strings.Contains(log, "this is not a known models key") || strings.Contains(log, "malformed") {
				t.Fatalf("warning leaked ignored model values: %s", log)
			}
		})
	}

	var logs bytes.Buffer
	r := newWithEnv(Options{ExplicitFiles: []string{"/operator.yaml"}, Diagnostics: slogdiag.New(&logs, false, port.LevelDebug)}, envWithExplicit("/operator.yaml", "models:\n  allowlist: [legacy]\n"))
	if r.OperatorModelPolicy() == nil {
		t.Fatal("operator allowlist no longer parses")
	}
	if got := strings.Count(logs.String(), "models.allowlist has no effect"); got != 1 {
		t.Fatalf("operator allowlist warning count = %d, log=%s", got, logs.String())
	}
}
