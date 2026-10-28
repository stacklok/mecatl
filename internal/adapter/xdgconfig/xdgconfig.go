// Package xdgconfig is the shared adapter-layer seam for resolving user-global
// configuration locations against the process environment. It exists to remove
// the resolveEnv/osEnv/userConfigDir triple that was independently duplicated in
// four adapters (permconfig, skills, agents, soul): each abstracted getenv /
// userHomeDir (and sometimes readFile) behind an injectable struct so the XDG
// path resolution was testable with a fake home. Centralising it keeps the XDG
// semantics identical across every adapter and gives one place to test them.
//
// LAYERING: this is an adapter-layer LEAF with only stdlib imports. Adapters MAY
// import it; no domain package (session, prompt, governance, tool) ever may.
package xdgconfig

import (
	"os"
	"path/filepath"
)

// ResolveEnv abstracts the process environment so a resolver is testable with a
// fake home / XDG and a fake file reader, without touching the real one. The
// composition root binds OSEnv (the real os funcs); tests pass a fake.
//
// ReadFile is included for the adapters that read a user-global FILE (permconfig,
// soul). Adapters that only resolve DIRECTORIES (skills, agents) simply leave it
// bound via OSEnv and never call it — an unused field, not a behavioural cost.
type ResolveEnv struct {
	Getenv      func(string) string
	Environ     func() []string
	UserHomeDir func() (string, error)
	ReadFile    func(string) ([]byte, error)
}

// OSEnv binds a resolver to the real process environment + filesystem.
var OSEnv = ResolveEnv{Getenv: os.Getenv, Environ: os.Environ, UserHomeDir: os.UserHomeDir, ReadFile: os.ReadFile}

// UserConfigDir returns the XDG config base for a user-level config location: the
// value of $XDG_CONFIG_HOME when set, else ~/.config. It returns "" when neither
// can be resolved (the caller then skips the user-level source). This preserves
// the exact semantics the four adapters shared before extraction.
func UserConfigDir(env ResolveEnv) string {
	if base := env.Getenv("XDG_CONFIG_HOME"); base != "" {
		return base
	}
	if home, err := env.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config")
	}
	return ""
}

// UserStateDir returns the XDG STATE base for a user-level state location: the
// value of $XDG_STATE_HOME when set, else ~/.local/state. It returns "" when
// neither can be resolved (the caller then skips persistence). It is the
// state-base twin of UserConfigDir, for MACHINE-WRITTEN runtime state (last-used
// selections, registries) — the settings-vs-state split the trust feature
// established (human config under XDG_CONFIG_HOME, machine state under
// XDG_STATE_HOME).
func UserStateDir(env ResolveEnv) string {
	if base := env.Getenv("XDG_STATE_HOME"); base != "" {
		return base
	}
	if home, err := env.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state")
	}
	return ""
}
