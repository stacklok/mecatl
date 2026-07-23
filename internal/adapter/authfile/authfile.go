// Package authfile is the adapter-layer leaf for mecatl's credentials file: a
// settings.yaml-sibling YAML file (conventionally $XDG_CONFIG_HOME/mecatl/auth.yaml)
// holding per-provider secrets — today an api_key, with room to grow into an OAuth
// token set (access/refresh token, expiry) without a schema break.
//
// LAYERING: adapter-layer LEAF — stdlib + internal/adapter/xdgconfig only (mirrors
// xdgconfig's own leaf shape: "Adapters MAY import it; no domain package ever may").
// internal/cliconfig is its cmd-layer wiring consumer today; a future credential-
// writing subcommand (e.g. `mecated auth login`) can depend on this package directly
// without pulling in cliconfig's flag/model-alias machinery.
package authfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// maxFileBytes bounds the credentials file before parsing (defense in depth,
// CWE-770, mirroring permconfig's maxConfigBytes): a credentials file is a
// handful of short strings and is never legitimately large.
const maxFileBytes = 16 * 1024

// relPath is the SINGLE definition of where the conventional auth.yaml lives,
// resolved against xdgconfig.UserConfigDir — the credential-file sibling of
// permconfig's "mecatl/settings.yaml".
const relPath = "mecatl/auth.yaml"

// ProviderEntry is one provider's entry in the file. APIKey is the only field
// today; it is already a struct (not a bare string) because an OAuth token set
// is the anticipated next field here, not a schema break.
type ProviderEntry struct {
	APIKey string `yaml:"api_key"`
}

// File is the parsed shape of auth.yaml: a settings.yaml companion that holds
// credentials. Operator-machine-local only — there is no project-tier
// equivalent (a project has no business supplying credentials).
type File struct {
	Providers map[string]ProviderEntry `yaml:"providers"`
}

// APIKey returns the api_key for name, or "" if f is nil or has no entry for
// name (an absent file, or a provider it doesn't mention, degrades to the
// empty string exactly like an unset environment variable).
func (f *File) APIKey(name string) string {
	if f == nil {
		return ""
	}
	return f.Providers[name].APIKey
}

// DefaultPath returns the conventional auth.yaml location:
// $XDG_CONFIG_HOME/mecatl/auth.yaml, falling back to ~/.config/mecatl/auth.yaml.
// Empty when neither can be resolved (matches xdgconfig.UserConfigDir).
func DefaultPath(env xdgconfig.ResolveEnv) string {
	base := xdgconfig.UserConfigDir(env)
	if base == "" {
		return ""
	}
	return filepath.Join(base, relPath)
}

// Load reads and strictly parses the auth.yaml at path, returning the parsed
// file (nil if there's nothing to merge) and a human-readable warning (empty
// if there's nothing to report). It is NEVER fatal — a caller always falls
// back to whatever it already has — but a non-empty warning should be
// surfaced (cmd/ mains: slog.Warn) so a typo'd or loosely-permissioned
// auth.yaml doesn't go unnoticed.
//
// The warning is deliberately VALUE-FREE: it names the path, a provider count,
// or a permission mode, but never echoes file content back — and a mistyped
// provider NAME is file content (a key typed where the name belongs is a
// verified leak shape). A caller may log it unconditionally without risking a
// secret leak — see the decode-error branch below for why this is asserted
// rather than merely intended.
//
// knownProviders is the closed set of provider names an entry may use (e.g.
// "anthropic"); a name outside it is reported rather than silently ignored,
// while entries that DO match still apply — a typo in one entry shouldn't
// cost you the rest of the file.
//
// explicit reports whether path came from an operator-supplied override (as
// opposed to a conventional default) and controls only whether a MISSING file
// is worth reporting: a missing conventional file is the common,
// unremarkable case (most operators still use an env var, or haven't created
// one yet); a missing EXPLICIT path is always reported, since the caller
// named that exact path. A file that DOES exist but fails to read or parse is
// always reported, regardless of explicit.
func Load(path string, explicit bool, env xdgconfig.ResolveEnv, knownProviders []string) (*File, string) {
	if path == "" {
		return nil, ""
	}
	data, err := env.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !explicit {
			return nil, ""
		}
		return nil, fmt.Sprintf("auth file %s: %v", path, err)
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Sprintf("auth file %s: %d bytes exceeds the %d-byte cap", path, len(data), maxFileBytes)
	}
	// Advisory-only permission check: mecatl never WRITES this file, so a loose
	// mode can only be reported, not fixed on the operator's behalf. Checked
	// before parsing (a permission problem is worth knowing about even if the
	// content also turns out to be malformed), and ACCUMULATED with any later
	// content warning rather than clobbered by it — the permission finding is
	// the security-relevant one, and the hand-edited files most likely to trip
	// a content warning are exactly the ones whose loose mode needs reporting.
	permWarning := checkPermissions(path)

	if len(bytes.TrimSpace(data)) == 0 {
		return &File{}, permWarning
	}

	// Strict (KnownFields) decode: an unrecognized key anywhere in the
	// document (a mistyped "provider:" instead of "providers:", or "apikey"
	// instead of "api_key" inside an entry) is a parse error rather than a
	// silently-ignored typo — a credentials file is exactly the place a
	// silent typo should not degrade to "key not found" at request time.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		// Deliberately do NOT include err (or %v of it) here. A structural
		// type mismatch — a scalar where providers.<name> expects a mapping,
		// e.g. an operator forgetting the "api_key:" nesting and writing the
		// key directly — makes go.yaml.in/yaml/v3's TypeError echo the raw
		// source text of the offending node into its error string. For a file
		// whose entire purpose is holding secrets, that is a verified,
		// reproducible way for a fragment of a real key to end up in this
		// warning and then in a log line. Report only the path and a generic
		// shape complaint; never the library's rendered error.
		return nil, fmt.Sprintf(
			"auth file %s: does not match the expected schema (providers.<name>.api_key) — check indentation and field names",
			path,
		)
	}

	// Count, never the names: an unknown provider name is an arbitrary YAML
	// key, and a key typed where the provider name belongs (inverted nesting)
	// would otherwise be echoed verbatim into the warning — the same CWE-532
	// class as the decode-error branch above. "Value-free" is a property of
	// the whole warning surface, not just that one branch.
	unknown := 0
	for name := range f.Providers {
		if !slices.Contains(knownProviders, name) {
			unknown++
		}
	}
	if unknown > 0 {
		return &f, joinWarnings(permWarning, fmt.Sprintf(
			"auth file %s: %d unknown provider(s) ignored (expected one of %s) — check provider names in the file",
			path, unknown, strings.Join(knownProviders, ", "),
		))
	}
	return &f, permWarning
}

// joinWarnings accumulates the non-empty warnings into the single return
// string (cheapest way to keep Load's (*File, string) signature while no
// longer letting a later content warning clobber the permission finding —
// both land in the one AuthFileWarning the cmd/ mains already log).
func joinWarnings(warnings ...string) string {
	return strings.Join(slices.DeleteFunc(slices.Clone(warnings), func(w string) bool {
		return w == ""
	}), "\n")
}

// checkPermissions warns (never fails) when path's file mode grants group or
// other access. Unix-only: Windows has no equivalent POSIX mode bits, so the
// check is skipped there rather than producing a meaningless warning.
func checkPermissions(path string) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		// Load already reported (or deliberately ignored) the read error via
		// env.ReadFile above; don't double-report a stat failure here.
		return ""
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Sprintf(
			"auth file %s: mode %04o allows group/other access — this file holds secrets in plaintext, consider chmod 600",
			path, perm,
		)
	}
	return ""
}
