package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// fakeSoulEnv installs a faked XDG/home path-resolution env for the duration of a
// test (restored on cleanup), so the conventional user-scoped soul
// (<xdg>/mecatl/soul.md) resolves against a temp dir — never the developer's real
// ~/.config. ReadFile is left nil: the soul.Store reads through the REAL bounded
// os.Open seam, so the body must exist as a real file under xdg.
//
// WARNING: this mutates the package-global soulEnv (swap + Cleanup restore). Any test
// that calls fakeSoulEnv MUST NOT call t.Parallel() — concurrent swaps of the global
// would race and cross-contaminate. Keep these tests serial.
func fakeSoulEnv(t *testing.T, xdg string) {
	t.Helper()
	prev := soulEnv
	soulEnv = xdgconfig.ResolveEnv{
		Getenv: func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return xdg
			}
			return ""
		},
		UserHomeDir: func() (string, error) { return "", errors.New("no home in test") },
	}
	t.Cleanup(func() { soulEnv = prev })
}

// writeUserSoul seeds a real conventional user soul at <xdg>/mecatl/soul.md.
func writeUserSoul(t *testing.T, xdg, body string) {
	t.Helper()
	dir := filepath.Join(xdg, "mecatl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir user soul dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "soul.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("seed user soul: %v", err)
	}
}

// writeProjectSoul seeds a real project soul at <workspace>/.mecatl/soul.md.
func writeProjectSoul(t *testing.T, workspace, body string) {
	t.Helper()
	dir := filepath.Join(workspace, ".mecatl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir project soul dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "soul.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("seed project soul: %v", err)
	}
}

// TestBuildSoulSourceDefaultOnAndDisable proves the REAL composition wiring honours
// the product decisions: soul is ON BY DEFAULT (a non-nil prompt.SoulSource WHEN a
// soul file is present), an explicit --soul-file path is honoured, and --no-soul
// (NoSoul) turns it OFF — returning an UNTYPED nil so buildInstructionAssembler's nil
// guard holds (the typed-nil gotcha). After Item 2, "on by default" means the soul
// is SELECTED when present; an absent file yields nil (no fragment), which is the
// same fail-soft outcome as before (the SoulAssembler's Load would return "").
func TestBuildSoulSourceDefaultOnAndDisable(t *testing.T) {
	t.Run("default on, user soul present", func(t *testing.T) {
		xdg := t.TempDir()
		fakeSoulEnv(t, xdg)
		writeUserSoul(t, xdg, "You are terse and kind.")
		if src := buildSoulSource(Config{}); src == nil {
			t.Fatal("buildSoulSource(Config{}) returned nil with a user soul present; soul must be ON by default")
		}
	})

	t.Run("default on, no soul present yields nil", func(t *testing.T) {
		xdg := t.TempDir() // empty: no user soul on disk
		fakeSoulEnv(t, xdg)
		if src := buildSoulSource(Config{}); src != nil {
			t.Fatalf("buildSoulSource(Config{}) with no soul file = %v, want nil (fail-soft, no fragment)", src)
		}
	})

	t.Run("explicit path on", func(t *testing.T) {
		dir := t.TempDir()
		soulPath := filepath.Join(dir, "soul.md")
		if err := writeTestFile(soulPath, "You are an explicit persona."); err != nil {
			t.Fatalf("seed explicit soul: %v", err)
		}
		if src := buildSoulSource(Config{SoulPath: soulPath}); src == nil {
			t.Fatal("buildSoulSource with SoulPath returned nil; the override path must be honoured")
		}
	})

	t.Run("no-soul off", func(t *testing.T) {
		// Config{NoSoul:true} → nil source, AND buildInstructionAssembler with that nil
		// source (and nil memStore) returns a BARE RootAssembler, not a MultiAssembler —
		// exercising the typed-nil guard end to end.
		src := buildSoulSource(Config{NoSoul: true})
		if src != nil {
			t.Fatalf("buildSoulSource(Config{NoSoul:true}) = %v, want nil (soul disabled)", src)
		}
		asm := buildInstructionAssembler(nil, src, nil, nil, false)
		if _, ok := asm.(prompt.RootAssembler); !ok {
			t.Fatalf("with nil soul + nil memStore, buildInstructionAssembler returned %T, want a bare prompt.RootAssembler (typed-nil guard)", asm)
		}
	})
}
