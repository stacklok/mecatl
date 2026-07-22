package cliconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// maxAuthFileBytes bounds auth.yaml before parsing (defense in depth, CWE-770,
// mirroring permconfig's maxConfigBytes): a credentials file is a handful of
// short strings and is never legitimately large.
const maxAuthFileBytes = 16 * 1024

// authFileRelPath is the SINGLE definition of where the conventional auth.yaml
// lives, resolved against xdgconfig.UserConfigDir — the credential-file sibling
// of permconfig's "mecatl/settings.yaml". It is deliberately its OWN file
// rather than a settings.yaml subtree or a fourth per-provider flag: keeping
// secrets out of settings.yaml means that file stays safe to share or check
// into a dotfiles repo, and this file has room to grow beyond a bare api_key
// (an OAuth token set — access/refresh token, expiry — is the anticipated next
// tenant of the same per-provider entry).
const authFileRelPath = "mecatl/auth.yaml"

// knownAuthProviders is the closed set of provider ids an auth.yaml entry may
// name — the same four ids ResolvedKeys carries. A name outside this set is
// almost certainly a typo (e.g. "anthropik"), so it is reported rather than
// silently ignored, while the entries that DO match still apply.
var knownAuthProviders = map[string]bool{
	"anthropic":  true,
	"openai":     true,
	"openrouter": true,
	"opencode":   true,
}

// AuthProviderEntry is one provider's entry in auth.yaml. APIKey is the only
// field today; it is already a struct (not a bare string) because an OAuth
// token set is the anticipated next field here, not a schema break.
type AuthProviderEntry struct {
	APIKey string `yaml:"api_key"`
}

// AuthFile is the parsed shape of auth.yaml: a settings.yaml companion that
// holds credentials. Operator-machine-local only — there is no project-tier
// equivalent (a project has no business supplying credentials).
type AuthFile struct {
	Providers map[string]AuthProviderEntry `yaml:"providers"`
}

// apiKey returns the api_key for name, or "" if af is nil or has no entry for
// name (an absent auth.yaml, or a provider it doesn't mention, degrades to the
// empty string exactly like an unset environment variable).
func (af *AuthFile) apiKey(name string) string {
	if af == nil {
		return ""
	}
	return af.Providers[name].APIKey
}

// DefaultAuthFilePath returns the conventional auth.yaml location:
// $XDG_CONFIG_HOME/mecatl/auth.yaml, falling back to ~/.config/mecatl/auth.yaml.
// Empty when neither can be resolved (matches xdgconfig.UserConfigDir).
func DefaultAuthFilePath(env xdgconfig.ResolveEnv) string {
	base := xdgconfig.UserConfigDir(env)
	if base == "" {
		return ""
	}
	return filepath.Join(base, authFileRelPath)
}

// loadAuthFile reads and strictly parses the auth.yaml at path, returning the
// parsed file (nil if there's nothing to merge) and a human-readable warning
// (empty if there's nothing to report). It is NEVER fatal — a caller always
// falls back to whatever ResolvedKeys already has from the environment — but
// a non-empty warning should be surfaced (cmd/ mains: slog.Warn) so a typo'd
// auth.yaml doesn't fail silently.
//
// explicit reports whether path came from an operator-supplied --auth-file
// (as opposed to the conventional default) and controls only whether a
// MISSING file is worth reporting: a missing conventional file is the
// common, unremarkable case (most operators still use an env var, or haven't
// created one yet), so it is silent; a missing EXPLICIT file is always
// reported, since the operator named that exact path. A file that DOES exist
// but fails to read or parse is always reported, regardless of explicit.
func loadAuthFile(path string, explicit bool, env xdgconfig.ResolveEnv) (*AuthFile, string) {
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
	if len(data) > maxAuthFileBytes {
		return nil, fmt.Sprintf("auth file %s: %d bytes exceeds the %d-byte cap", path, len(data), maxAuthFileBytes)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return &AuthFile{}, ""
	}

	// Strict (KnownFields) decode: an unrecognized key anywhere in the
	// document (a mistyped "provider:" instead of "providers:", or "apikey"
	// instead of "api_key" inside an entry) is a parse error rather than a
	// silently-ignored typo — a credentials file is exactly the place a
	// silent typo should not degrade to "key not found" at request time.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var af AuthFile
	if err := dec.Decode(&af); err != nil {
		return nil, fmt.Sprintf("auth file %s: parse: %v", path, err)
	}

	var unknown []string
	for name := range af.Providers {
		if !knownAuthProviders[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return &af, fmt.Sprintf(
			"auth file %s: unknown provider(s) %s (expected one of anthropic, openai, openrouter, opencode) — ignored",
			path, strings.Join(unknown, ", "),
		)
	}
	return &af, ""
}
