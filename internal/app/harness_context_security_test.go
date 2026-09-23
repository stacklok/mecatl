package app

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
)

func TestHarnessContextRejectsMalformedOperatorPolicyBeforeBinding(t *testing.T) {
	for _, body := range []string{
		"harness_context: {typo: true}\n",
		"harness_context: {enabled_sources: [source], kinds: {instructions: {sources: [source], mode: combine}}}\n",
		"harness_context: null\n",
	} {
		t.Run(body, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			cfg := Config{Workspace: t.TempDir(), UseMock: true, UserModelDir: t.TempDir(), PermissionConfigs: []string{path}, HarnessInstructionSources: []HarnessSourceRegistration[prompt.InstructionAssembler]{{ID: "source", Provenance: HarnessProvenancePolicy{Fixed: "project"}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
				calls.Add(1)
				return hcAssembler("unadmitted"), nil, nil
			}}}}
			built, err := buildIsolated(t, t.Context(), cfg)
			if built != nil {
				built.Close()
			}
			if err == nil {
				t.Error("malformed explicit operator policy silently accepted")
			}
			if calls.Load() != 0 {
				t.Errorf("called source before policy validation: %d", calls.Load())
			}
		})
	}
}

func TestHarnessContextProjectRegistrationCannotBypassAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	policy := `harness_context:
  enabled_sources: [source]
  kinds:
    instructions: {sources: [source], mode: combine}
    commands: {sources: [], mode: combine}
    rules: {sources: [], mode: combine}
    skills: {sources: [], mode: combine}
    agent_defs: {sources: [], mode: combine}
`
	if err := os.WriteFile(path, []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	cfg := Config{Workspace: t.TempDir(), UseMock: true, Headless: true, TrustProject: false, UserModelDir: t.TempDir(), PermissionConfigs: []string{path}, HarnessInstructionSources: []HarnessSourceRegistration[prompt.InstructionAssembler]{{ID: "source", Provenance: HarnessProvenancePolicy{Fixed: "project"}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
		calls.Add(1)
		return hcAssembler("unadmitted"), nil, nil
	}}}}
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	if calls.Load() != 0 {
		t.Errorf("untrusted project registration bound %d times", calls.Load())
	}
	if _, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{}); err != nil {
		t.Fatal(err)
	}
}
