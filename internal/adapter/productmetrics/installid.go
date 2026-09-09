package productmetrics

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// installIDRelPath is the state-dir-relative path to the persisted anonymous
// install identifier — machine-written runtime state, not human config, so
// it lives under XDG_STATE_HOME (mirroring mecatui's
// $XDG_STATE_HOME/mecatl/mecatui.log precedent), not XDG_CONFIG_HOME.
const installIDRelPath = "mecatl/telemetry-id"

// LoadOrCreateInstallID reads the persisted install UUID, creating one if
// absent or unparseable. The id is a bare random v4 UUID: it carries no
// machine or user information, and is trivially reset by deleting the file
// (the next opt-in mints a new one). firstRun is true whenever a new id was
// just minted — the caller uses it to decide whether to print the one-time
// disclosure notice. readFile/writeFile/mkdirAll are injected for testing;
// LoadOrCreateInstallIDDefault binds the real filesystem.
func LoadOrCreateInstallID(
	env xdgconfig.ResolveEnv,
	readFile func(string) ([]byte, error),
	writeFile func(string, []byte, os.FileMode) error,
	mkdirAll func(string, os.FileMode) error,
) (id string, firstRun bool, err error) {
	base := xdgconfig.UserStateDir(env)
	if base == "" {
		return "", false, fmt.Errorf("productmetrics: cannot resolve a state directory (no XDG_STATE_HOME and no home dir)")
	}
	path := filepath.Join(base, installIDRelPath)

	if readFile != nil {
		if data, rerr := readFile(path); rerr == nil {
			if existing := strings.TrimSpace(string(data)); existing != "" {
				if _, perr := uuid.Parse(existing); perr == nil {
					return existing, false, nil
				}
				// Corrupt file: fall through and regenerate.
			}
		}
	}

	fresh := uuid.NewString()
	if mkdirAll != nil {
		if merr := mkdirAll(filepath.Dir(path), 0o700); merr != nil {
			return "", false, fmt.Errorf("productmetrics: create state dir: %w", merr)
		}
	}
	if writeFile != nil {
		if werr := writeFile(path, []byte(fresh), 0o600); werr != nil {
			return "", false, fmt.Errorf("productmetrics: write install id: %w", werr)
		}
	}
	return fresh, true, nil
}

// LoadOrCreateInstallIDDefault binds LoadOrCreateInstallID to the real
// process environment and filesystem.
func LoadOrCreateInstallIDDefault() (id string, firstRun bool, err error) {
	return LoadOrCreateInstallID(xdgconfig.OSEnv, os.ReadFile, os.WriteFile, os.MkdirAll)
}
