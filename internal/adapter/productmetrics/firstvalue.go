package productmetrics

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// firstValueMarkerRelPath is the state-dir-relative path to a bare marker file
// recording whether this install's first meaningful-and-successful run has
// already been observed — so mecatl.product.time_to_first_value is recorded at
// most once per install, ever. It lives beside the install-id file under
// XDG_STATE_HOME (machine-written runtime state, not human config) and mirrors
// installid.go's injected-filesystem shape exactly.
const firstValueMarkerRelPath = "mecatl/first-value-recorded"

// firstValueMarkerPath resolves the marker's absolute path, failing closed when
// no state directory can be resolved at all.
func firstValueMarkerPath(env xdgconfig.ResolveEnv) (string, error) {
	base := xdgconfig.UserStateDir(env)
	if base == "" {
		return "", fmt.Errorf("productmetrics: cannot resolve a state directory (no XDG_STATE_HOME and no home dir)")
	}
	return filepath.Join(base, firstValueMarkerRelPath), nil
}

// FirstValueRecorded reports whether this install's time_to_first_value sample
// was already recorded by some EARLIER process. It is a pure READ: it never
// creates the marker, because the marker means "the qualifying run happened",
// and process startup is not that moment (a process may exit without ever
// having one). The marker is written later, by LoadOrCreateFirstValueMarker,
// at the instant the qualifying run is observed.
//
// readFile is injected for testing; FirstValueRecordedDefault binds the real
// filesystem.
func FirstValueRecorded(env xdgconfig.ResolveEnv, readFile func(string) ([]byte, error)) (bool, error) {
	path, err := firstValueMarkerPath(env)
	if err != nil {
		return false, err
	}
	if readFile == nil {
		return false, nil
	}
	if _, rerr := readFile(path); rerr != nil {
		return false, nil
	}
	return true, nil
}

// FirstValueRecordedDefault binds FirstValueRecorded to the real process
// environment and filesystem.
func FirstValueRecordedDefault() (bool, error) {
	return FirstValueRecorded(xdgconfig.OSEnv, os.ReadFile)
}

// LoadOrCreateFirstValueMarker reports whether this install's first-value
// moment was already marked (already == true), creating the marker (and
// returning already == false) the first time it is called. It is the WRITE
// half, called at the moment a qualifying run is observed — not at startup.
// Once created, the marker is never removed automatically; deleting it (like
// the install-id file) resets the install and lets time_to_first_value fire
// once more.
//
// readFile/writeFile/mkdirAll are injected for testing;
// LoadOrCreateFirstValueMarkerDefault binds the real filesystem.
func LoadOrCreateFirstValueMarker(
	env xdgconfig.ResolveEnv,
	readFile func(string) ([]byte, error),
	writeFile func(string, []byte, os.FileMode) error,
	mkdirAll func(string, os.FileMode) error,
) (already bool, err error) {
	path, err := firstValueMarkerPath(env)
	if err != nil {
		return false, err
	}

	if readFile != nil {
		if _, rerr := readFile(path); rerr == nil {
			return true, nil
		}
	}
	if mkdirAll != nil {
		if merr := mkdirAll(filepath.Dir(path), 0o700); merr != nil {
			return false, fmt.Errorf("productmetrics: create state dir: %w", merr)
		}
	}
	if writeFile != nil {
		if werr := writeFile(path, []byte("1"), 0o600); werr != nil {
			return false, fmt.Errorf("productmetrics: write first-value marker: %w", werr)
		}
	}
	return false, nil
}

// LoadOrCreateFirstValueMarkerDefault binds LoadOrCreateFirstValueMarker to the
// real process environment and filesystem.
func LoadOrCreateFirstValueMarkerDefault() (already bool, err error) {
	return LoadOrCreateFirstValueMarker(xdgconfig.OSEnv, os.ReadFile, os.WriteFile, os.MkdirAll)
}
