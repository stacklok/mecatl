// Package authfile is the adapter-layer leaf for mecatl's credentials file: a
// settings.yaml-sibling YAML file (conventionally $XDG_CONFIG_HOME/mecatl/auth.yaml)
// holding per-provider secrets: an api_key for the existing keyed providers or
// a manually supplied OAuth access-token snapshot for openai-codex.
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
	"io"
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

// OAuthEntry is the raw, immutable-at-runtime OAuth shape accepted at the file
// boundary. Parsing routing metadata and expiry is deliberately left to the
// provider adjunct; authfile keeps these fields as strings and never refreshes
// or writes them.
type OAuthEntry struct {
	AccessToken string `yaml:"access_token"`
	AccountID   string `yaml:"account_id"`
	ExpiresAt   string `yaml:"expires_at"`
}

// ProviderEntry is one provider's stored entry. OAuth state remains
// unexported: File.Providers is public for API-key compatibility, so putting a
// pointer there would let a caller mutate the supposedly immutable credential
// snapshot without going through the copy-returning accessor.
type ProviderEntry struct {
	APIKey   string `yaml:"api_key"`
	oauth    OAuthEntry
	hasOAuth bool
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

// OAuth returns a copy of name's validated OAuth file entry. The zero value
// means there is no usable OAuth entry for name. Returning a value keeps
// callers from mutating the parsed credential snapshot through this accessor.
func (f *File) OAuth(name string) OAuthEntry {
	if f == nil {
		return OAuthEntry{}
	}
	entry := f.Providers[name]
	if !entry.hasOAuth {
		return OAuthEntry{}
	}
	return entry.oauth
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

	// Decode the whole document as nodes so tags and exact mapping shapes remain
	// visible for validation. A typed top-level decode would discard custom tags
	// on the root/providers mappings before the entry-local checks could see them.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := dec.Decode(&document); err != nil {
		// Deliberately do NOT include err (or %v of it) here. A structural
		// type mismatch — a scalar where providers.<name> expects a mapping,
		// e.g. an operator forgetting the "api_key:" nesting and writing the
		// key directly — makes go.yaml.in/yaml/v3's TypeError echo the raw
		// source text of the offending node into its error string. For a file
		// whose entire purpose is holding secrets, that is a verified,
		// reproducible way for a fragment of a real key to end up in this
		// warning and then in a log line. Report only the path and a generic
		// shape complaint; never the library's rendered error.
		return nil, schemaWarning(path)
	}
	// Exactly one YAML document is accepted. A second document is ambiguous
	// credential input even when it is empty or null, so fail closed with the
	// same generic, value-free warning used for other whole-file shape errors.
	var trailing yaml.Node
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, schemaWarning(path)
	}
	rawProviders, ok := decodeAuthDocument(document)
	if !ok {
		return nil, schemaWarning(path)
	}

	// Count, never the names: an unknown provider name is an arbitrary YAML
	// key, and a key typed where the provider name belongs (inverted nesting)
	// would otherwise be echoed verbatim into the warning — the same CWE-532
	// class as the decode-error branch above. "Value-free" is a property of
	// the whole warning surface, not just that one branch.
	f := File{Providers: make(map[string]ProviderEntry, len(rawProviders))}
	unknown := 0
	invalidSchema := 0
	invalidSemantics := 0
	for name, node := range rawProviders {
		if !slices.Contains(knownProviders, name) {
			unknown++
			continue
		}
		entry, ok := decodeProviderEntry(node)
		if !ok {
			invalidSchema++
			continue
		}
		entry, invalid := validateProviderEntry(name, entry)
		if invalid {
			invalidSemantics++
		}
		f.Providers[name] = entry
	}
	contentWarnings := make([]string, 0, 3)
	if unknown > 0 {
		contentWarnings = append(contentWarnings, fmt.Sprintf(
			"auth file %s: %d unknown provider(s) ignored (expected one of %s) — check provider names in the file",
			path, unknown, strings.Join(knownProviders, ", "),
		))
	}
	if invalidSchema > 0 {
		contentWarnings = append(contentWarnings, fmt.Sprintf(
			"auth file %s: %d provider credential entry/entries ignored because they do not match the expected schema (api_key or oauth.access_token/account_id/expires_at)",
			path, invalidSchema,
		))
	}
	if invalidSemantics > 0 {
		contentWarnings = append(contentWarnings, fmt.Sprintf(
			"auth file %s: %d provider credential entry/entries contained ignored fields (OAuth is only valid for openai-codex with a non-empty access_token; API keys do not enable openai-codex)",
			path, invalidSemantics,
		))
	}
	warning := joinWarnings(append([]string{permWarning}, contentWarnings...)...)
	if invalidSchema > 0 && len(f.Providers) == 0 {
		return nil, warning
	}
	return &f, warning
}

func schemaWarning(path string) string {
	return fmt.Sprintf(
		"auth file %s: does not match the expected schema (providers.<name>.api_key or providers.openai-codex.oauth) — check indentation and field names",
		path,
	)
}

func decodeAuthDocument(document yaml.Node) (map[string]yaml.Node, bool) {
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, false
	}
	root := document.Content[0]
	if !canonicalMapping(root) {
		return nil, false
	}
	rootKeys := make(map[string]struct{}, len(root.Content)/2)
	providers := make(map[string]yaml.Node)
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if !recordMappingKey(key, rootKeys) || key.Value != "providers" || !canonicalMapping(value) {
			return nil, false
		}
		providerKeys := make(map[string]struct{}, len(value.Content)/2)
		for j := 0; j < len(value.Content); j += 2 {
			providerKey, providerValue := value.Content[j], value.Content[j+1]
			if !recordMappingKey(providerKey, providerKeys) {
				return nil, false
			}
			providers[providerKey.Value] = *providerValue
		}
	}
	return providers, true
}

// decodeProviderEntry performs strict, value-free validation without asking
// the YAML library to render an error. Its boolean is the entire error surface:
// Load reports only a count, never a node, key, or scalar from the file.
func decodeProviderEntry(node yaml.Node) (ProviderEntry, bool) {
	if !canonicalMapping(&node) {
		return ProviderEntry{}, false
	}
	var entry ProviderEntry
	seen := make(map[string]struct{}, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if !recordMappingKey(key, seen) {
			return ProviderEntry{}, false
		}
		switch key.Value {
		case "api_key":
			apiKey, ok := strictYAMLString(value)
			if !ok {
				return ProviderEntry{}, false
			}
			entry.APIKey = apiKey
		case "oauth":
			oauth, ok := decodeOAuthEntry(value)
			if !ok {
				return ProviderEntry{}, false
			}
			entry.oauth = oauth
			entry.hasOAuth = true
		default:
			return ProviderEntry{}, false
		}
	}
	return entry, true
}

func decodeOAuthEntry(node *yaml.Node) (OAuthEntry, bool) {
	if !canonicalMapping(node) {
		return OAuthEntry{}, false
	}
	var entry OAuthEntry
	seen := make(map[string]struct{}, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if !recordMappingKey(key, seen) {
			return OAuthEntry{}, false
		}
		switch key.Value {
		case "access_token":
			decoded, ok := strictYAMLString(value)
			if !ok {
				return OAuthEntry{}, false
			}
			entry.AccessToken = decoded
		case "account_id":
			decoded, ok := strictYAMLString(value)
			if !ok {
				return OAuthEntry{}, false
			}
			entry.AccountID = decoded
		case "expires_at":
			decoded, ok := strictExpiryString(value)
			if !ok {
				return OAuthEntry{}, false
			}
			entry.ExpiresAt = decoded
		default:
			return OAuthEntry{}, false
		}
	}
	return entry, true
}

func canonicalMapping(node *yaml.Node) bool {
	return node.Kind == yaml.MappingNode && node.Tag == "!!map" && len(node.Content)%2 == 0
}

func recordMappingKey(key *yaml.Node, seen map[string]struct{}) bool {
	if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
		return false
	}
	if _, duplicate := seen[key.Value]; duplicate {
		return false
	}
	seen[key.Value] = struct{}{}
	return true
}

func strictYAMLString(node *yaml.Node) (string, bool) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", false
	}
	return node.Value, true
}

func strictExpiryString(node *yaml.Node) (string, bool) {
	// YAML resolves the plan's unquoted RFC3339 example as !!timestamp. It is
	// still retained byte-for-byte as a raw string at this boundary; all other
	// implicit scalar types and every custom tag remain rejected.
	if node.Kind != yaml.ScalarNode || (node.Tag != "!!str" && node.Tag != "!!timestamp") {
		return "", false
	}
	return node.Value, true
}

func validateProviderEntry(name string, entry ProviderEntry) (ProviderEntry, bool) {
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
