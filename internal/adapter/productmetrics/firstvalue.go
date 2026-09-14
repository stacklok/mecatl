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
// The create is an ATOMIC cross-process claim, not a read-then-write check:
// createExclusive must fail when the marker already exists (os.IsExist),
// e.g. via O_CREATE|O_EXCL. Two processes racing this call therefore never
// both observe already==false — exactly one create wins, and the loser
// reliably reports already==true, even when the two calls are strictly
// sequential rather than concurrent (the marker created by an earlier
// process's call is still there when a later process's call runs).
// firstValueTracker.claim() depends on this: it treats a false "won" from
// its recordFn as "another process already has this install's one sample"
// and skips recording, so a non-atomic check-then-act here would silently
// let two processes each record a sample.
//
// mkdirAll/createExclusive are injected for testing;
// LoadOrCreateFirstValueMarkerDefault binds the real filesystem.
func LoadOrCreateFirstValueMarker(
	env xdgconfig.ResolveEnv,
	mkdirAll func(string, os.FileMode) error,
	createExclusive func(string, []byte, os.FileMode) error,
) (already bool, err error) {
	path, err := firstValueMarkerPath(env)
	if err != nil {
		return false, err
	}

	if mkdirAll != nil {
		if merr := mkdirAll(filepath.Dir(path), 0o700); merr != nil {
			return false, fmt.Errorf("productmetrics: create state dir: %w", merr)
		}
	}
	if createExclusive == nil {
		return false, nil
	}
	switch cerr := createExclusive(path, []byte("1"), 0o600); {
	case cerr == nil:
		return false, nil
	case os.IsExist(cerr):
		return true, nil
	default:
		return false, fmt.Errorf("productmetrics: write first-value marker: %w", cerr)
	}
}

// createFileExclusive creates path only if it does not already exist,
// returning an os.IsExist-satisfying error otherwise (O_CREATE|O_EXCL) — the
// atomic cross-process claim LoadOrCreateFirstValueMarker's contract
// depends on; a plain os.WriteFile (create-or-truncate) would let two
// concurrent callers both "win".
func createFileExclusive(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// LoadOrCreateFirstValueMarkerDefault binds LoadOrCreateFirstValueMarker to the
// real process environment and filesystem.
func LoadOrCreateFirstValueMarkerDefault() (already bool, err error) {
	return LoadOrCreateFirstValueMarker(xdgconfig.OSEnv, os.MkdirAll, createFileExclusive)
}
