// Package authfile is the adapter-layer leaf for mecatl's credentials file: a
// settings.yaml-sibling YAML file (conventionally $XDG_CONFIG_HOME/mecatl/auth.yaml)
// holding per-provider secrets: an api_key for the existing keyed providers or
// a manually supplied OAuth access-token snapshot for openai-codex.
//
// LAYERING: adapter-layer leaf — stdlib plus narrowly scoped root-internal
// provider-ID, XDG, and YAML-diagnostic helpers. internal/cliconfig and local
// operator setup consume it without pulling in CLI/model-alias machinery.
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

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/adapter/yamldiag"
)

// maxFileBytes bounds the credentials file before parsing (defense in depth,
// CWE-770, mirroring permconfig's maxConfigBytes): a credentials file is a
// handful of short strings and is never legitimately large.
const maxFileBytes = 16 * 1024

// relPath is the SINGLE definition of where the conventional auth.yaml lives,
// resolved against xdgconfig.UserConfigDir — the credential-file sibling of
// permconfig's "mecatl/settings.yaml".
const relPath = "mecatl/auth.yaml"

// OAuthEntry is the raw, immutable-at-runtime OAuth shape accepted at the file
// boundary. Parsing routing metadata and expiry is deliberately left to the
// provider adjunct; authfile keeps these fields as strings and never refreshes
// or writes them.
type OAuthEntry struct {
	AccessToken string `yaml:"access_token"`
	AccountID   string `yaml:"account_id"`
	ExpiresAt   string `yaml:"expires_at"`
}

// providerEntry is one provider's stored entry. The whole parsed map remains
// private; consumers receive only copied scalar values through accessors.
type providerEntry struct {
	APIKey   string `yaml:"api_key"`
	oauth    OAuthEntry
	hasOAuth bool
}

// File is the parsed shape of auth.yaml: a settings.yaml companion that holds
// credentials. Operator-machine-local only — there is no project-tier
// equivalent (a project has no business supplying credentials).
type File struct {
	providers   map[string]providerEntry
	path        string
	baseWarning string
}

type rawFile struct {
	Providers map[string]rawProviderEntry `yaml:"providers"`
}

type rawProviderEntry struct {
	APIKey strictString   `yaml:"api_key"`
	OAuth  *rawOAuthEntry `yaml:"oauth"`
}

type rawOAuthEntry struct {
	AccessToken strictString `yaml:"access_token"`
	AccountID   strictString `yaml:"account_id"`
	ExpiresAt   expiryString `yaml:"expires_at"`
}

type strictString string

func (s *strictString) UnmarshalYAML(node ast.Node) error {
	if tagged, ok := node.(*ast.TagNode); ok && tagged.Start != nil && tagged.Start.Value == "!!str" {
		if parserToken := tagged.Value.GetToken(); parserToken != nil {
			*s = strictString(parserToken.Value)
			return nil
		}
	}
	if node.Type() != ast.StringType {
		return errors.New("expected string")
	}
	*s = strictString(node.GetToken().Value)
	return nil
}

type expiryString string

func (s *expiryString) UnmarshalYAML(node ast.Node) error {
	if node.Type() != ast.StringType {
		return errors.New("expected timestamp string")
	}
	*s = expiryString(node.GetToken().Value)
	return nil
}

// APIKey returns the api_key for name, or "" if f is nil or has no entry for
// name (an absent file, or a provider it doesn't mention, degrades to the
// empty string exactly like an unset environment variable).
func (f *File) APIKey(name string) string {
	if f == nil {
		return ""
	}
	return f.providers[name].APIKey
}

// OAuth returns a copy of name's validated OAuth file entry. The zero value
// means there is no usable OAuth entry for name. Returning a value keeps
// callers from mutating the parsed credential snapshot through this accessor.
func (f *File) OAuth(name string) OAuthEntry {
	if f == nil {
		return OAuthEntry{}
	}
	entry := f.providers[name]
	if !entry.hasOAuth {
		return OAuthEntry{}
	}
	return entry.oauth
}

// LoadStrict is the fail-closed credential-file entry point for callers whose
// provider set is configuration-derived. Unlike Load's legacy best-effort merge,
// any file warning rejects the entire snapshot without returning file content.
func LoadStrict(path string, explicit bool, env xdgconfig.ResolveEnv, knownProviders []string) (*File, error) {
	file, warning := Load(path, explicit, env, knownProviders)
	if warning != "" {
		return nil, errors.New("auth file validation failed")
	}
	return file, nil
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
	// Advisory-only permission check for reads. The targeted writer separately
	// rejects loose existing modes and never tightens them on the operator's behalf.
	// It is checked before parsing (a permission problem is worth knowing about even if the
	// content also turns out to be malformed), and ACCUMULATED with any later
	// content warning rather than clobbered by it — the permission finding is
	// the security-relevant one, and the hand-edited files most likely to trip
	// a content warning are exactly the ones whose loose mode needs reporting.
	permWarning := checkPermissions(path)

	if len(bytes.TrimSpace(data)) == 0 {
		return &File{}, permWarning
	}

	// Strict typed decode: a typo at any supported level rejects the credential
	// file instead of silently dropping a field. Parser and decoder errors remain
	// value-free because they are never returned to callers. Auth files retain the
	// parser's anchor and alias semantics; those restrictions belong only to
	// settings documents that an editor rewrites.
	file, err := parser.ParseBytes(data, parser.ParseComments)
	if err != nil {
		return nil, schemaWarning(path, err)
	}
	if len(file.Docs) != 1 || file.Docs[0] == nil || file.Docs[0].Body == nil || len(ast.Filter(ast.NullType, file.Docs[0].Body)) > 0 {
		return nil, schemaWarning(path, nil)
	}
	var raw rawFile
	if err := yaml.NodeToValue(file.Docs[0].Body, &raw, yaml.DisallowUnknownField()); err != nil || raw.Providers == nil {
		return nil, schemaWarning(path, nil)
	}
	// Count, never the names: an unknown provider name is an arbitrary YAML
	// key, and a key typed where the provider name belongs (inverted nesting)
	// would otherwise be echoed verbatim into the warning — the same CWE-532
	// class as the decode-error branch above. "Value-free" is a property of
	// the whole warning surface, not just that one branch.
	f := File{providers: make(map[string]providerEntry, len(raw.Providers)), path: path, baseWarning: permWarning}
	for name, rawEntry := range raw.Providers {
		entry := providerEntry{APIKey: string(rawEntry.APIKey)}
		if rawEntry.OAuth != nil {
			entry.oauth = OAuthEntry{
				AccessToken: string(rawEntry.OAuth.AccessToken),
				AccountID:   string(rawEntry.OAuth.AccountID),
				ExpiresAt:   string(rawEntry.OAuth.ExpiresAt),
			}
			entry.hasOAuth = true
		}
		f.providers[name] = entry
	}
	return &f, f.ValidateKnown(knownProviders)
}

// ValidateKnown applies the command root's final provider allowlist to an
// already parsed immutable snapshot. It is intentionally separate from Load so
// roots can first discover custom provider IDs, then validate auth.yaml without
// a second filesystem read.
func (f *File) ValidateKnown(knownProviders []string) string {
	if f == nil {
		return ""
	}
	unknown, invalidSemantics := 0, 0
	for name, entry := range f.providers {
		if len(knownProviders) > 0 && !slices.Contains(knownProviders, name) {
			unknown++
			continue
		}
		entry, invalid := validateProviderEntry(name, entry)
		if invalid {
			invalidSemantics++
		}
		f.providers[name] = entry
	}
	contentWarnings := make([]string, 0, 2)
	if unknown > 0 {
		contentWarnings = append(contentWarnings, fmt.Sprintf(
			"auth file %s: %d unknown provider(s) ignored (expected one of %s) — check provider names in the file",
			f.path, unknown, strings.Join(knownProviders, ", "),
		))
	}
	if invalidSemantics > 0 {
		contentWarnings = append(contentWarnings, fmt.Sprintf(
			"auth file %s: %d provider credential entry/entries contained ignored fields (OAuth is only valid for openai-codex with a non-empty access_token; API keys do not enable openai-codex)",
			f.path, invalidSemantics,
		))
	}
	return joinWarnings(append([]string{f.baseWarning}, contentWarnings...)...)
}

func schemaWarning(path string, parseErr error) string {
	location := yamldiag.Classify("parse auth file", parseErr)
	if location.HasLocation {
		return fmt.Sprintf(
			"auth file %s: does not match the expected schema at line %d, column %d (providers.<name>.api_key or providers.openai-codex.oauth) — check indentation and field names",
			path,
			location.Line,
			location.Column,
		)
	}
	return fmt.Sprintf(
		"auth file %s: does not match the expected schema (providers.<name>.api_key or providers.openai-codex.oauth) — check indentation and field names",
		path,
	)
}

func validateProviderEntry(name string, entry providerEntry) (providerEntry, bool) {
	invalid := false
	if name == "openai-codex" {
		if entry.APIKey != "" {
			entry.APIKey = ""
			invalid = true
		}
		if entry.hasOAuth && strings.TrimSpace(entry.oauth.AccessToken) == "" {
			entry.oauth = OAuthEntry{}
			entry.hasOAuth = false
			invalid = true
		}
		return entry, invalid
	}
	if entry.hasOAuth {
		entry.oauth = OAuthEntry{}
		entry.hasOAuth = false
		invalid = true
	}
	return entry, invalid
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
