package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/stacklok/mecatl/cmd/mecatui/keymap"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// clientSettings is the STRICT top-level schema of the client-owned settings
// file (~/.config/mecatui/settings.yaml). The client file is mecatui's own —
// unlike the shared server file it is decoded with KnownFields(true), so an
// unrecognized top-level key is a parse error, not a silently-ignored typo.
// It is designed to grow additive fields later (theme, UI behaviour, …).
type clientSettings struct {
	Keymap map[string]string `yaml:"keymap"`
}

// legacySettings mirrors the keymap: key out of the SERVER-owned operator-tier
// settings file (~/.config/mecatl/settings.yaml). That file is shared with
// mecated and carries permissions:/guardrails:/models:/… sibling keys, so it
// is decoded LENIENTLY (plain Unmarshal) — the legacy reader must tolerate
// every server key, not reject them.
type legacySettings struct {
	Keymap map[string]string `yaml:"keymap"`
}

// clientSettingsPath is the single source of the client settings file
// location: <XDG config base>/mecatui/settings.yaml. It returns "" when the
// base is unresolvable (the caller then treats the file as absent).
func clientSettingsPath(env xdgconfig.ResolveEnv) string {
	base := xdgconfig.UserConfigDir(env)
	if base == "" {
		return ""
	}
	return filepath.Join(base, "mecatui", "settings.yaml")
}

// splitChords splits a comma-separated chord value into trimmed chords,
// skipping empties. Shared by the legacy and client keymap readers so both
// parse the byte-compatible `keymap: {Action: "ctrl+a, ctrl+f12"}` format
// identically.
func splitChords(val string) []string {
	parts := make([]string, 0, 1)
	for _, p := range strings.Split(val, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// splitKeymap applies splitChords to every action in a raw keymap map,
// dropping actions whose value splits to nothing.
func splitKeymap(raw map[string]string) map[string][]string {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string][]string, len(raw))
	for action, val := range raw {
		if parts := splitChords(val); len(parts) > 0 {
			out[action] = parts
		}
	}
	return out
}

// readClientKeymap reads the CLIENT-owned settings file
// (~/.config/mecatui/settings.yaml) and returns its keymap overrides as
// action -> []chords. An absent file (or unresolvable config base) returns
// (nil, false, nil). The bool reports "the client file contributed a keymap".
//
// The file is decoded STRICTLY (KnownFields(true)): clientSettings is the
// whole schema, so an unknown top-level key is an error naming the file (the
// daemonconfig strict-decode idiom — a *yaml.TypeError is the unknown-key /
// wrong-type signal; a plain decode error is a syntax problem whose message
// embeds the offending source line and is therefore NOT wrapped through,
// CWE-209). A multi-document file is rejected.
func readClientKeymap() (map[string][]string, bool, error) {
	path := clientSettingsPath(xdgconfig.OSEnv)
	if path == "" {
		return nil, false, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var s clientSettings
	if err := dec.Decode(&s); err != nil && !errors.Is(err, io.EOF) {
		var typeErr *yaml.TypeError
		if errors.As(err, &typeErr) {
			return nil, false, fmt.Errorf("parsing %s: does not match the expected schema (unknown key or type); the only recognized key is keymap", path)
		}
		return nil, false, fmt.Errorf("parsing %s: invalid YAML syntax (the document must be valid YAML matching the client settings schema)", path)
	}
	// Single-document schema: a second decode MUST hit io.EOF — a decoded
	// second document or trailing garbage means the file carries more than one
	// document, so refuse rather than silently drop the rest.
	if err := dec.Decode(&clientSettings{}); !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("parsing %s: multiple documents are not supported (the client settings schema is a single document)", path)
	}
	out := splitKeymap(s.Keymap)
	if out == nil {
		return nil, false, nil
	}
	return out, true, nil
}

// readLegacyKeymap reads the keymap: key out of the SERVER-owned operator-tier
// settings file (~/.config/mecatl/settings.yaml) — the DEPRECATED location.
// The file is shared with mecated, so the decode is LENIENT (plain Unmarshal):
// permissions:/guardrails:/models:/… sibling keys are tolerated and ignored.
// An absent file (or unresolvable config base, or no keymap: key) returns
// (nil, false, nil). The bool reports "the legacy file contributed a keymap"
// and drives the deprecation WARN in applyKeyOverridesToDeps.
func readLegacyKeymap() (map[string][]string, bool, error) {
	cfgBase := xdgconfig.UserConfigDir(xdgconfig.OSEnv)
	if cfgBase == "" {
		return nil, false, nil
	}
	path := filepath.Join(cfgBase, "mecatl", "settings.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	var s legacySettings
	if err := yaml.Unmarshal(b, &s); err != nil {
		// Same CWE-209 discipline as the strict client reader: a *yaml.TypeError
		// (here always "keymap: isn't a mapping of action -> chord(s)") embeds a
		// truncated slice of the offending VALUE, so it is NOT wrapped through —
		// the operator needs the line, not the value. A syntax (scanner) error
		// carries only a line number, no source text, but is kept generic for
		// symmetry with the client reader.
		var typeErr *yaml.TypeError
		if errors.As(err, &typeErr) {
			return nil, false, fmt.Errorf("parse %s: the keymap: key must be a mapping of action -> chord(s) (wrong type under keymap)", path)
		}
		return nil, false, fmt.Errorf("parse %s: invalid YAML syntax (check the file's line structure)", path)
	}
	out := splitKeymap(s.Keymap)
	if out == nil {
		return nil, false, nil
	}
	return out, true, nil
}

// mergeKeymaps returns a new map with b overlaying a (b wins on conflicts).
func mergeKeymaps(a, b map[string][]string) map[string][]string {
	if a == nil && b == nil {
		return nil
	}
	out := make(map[string][]string)
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// applyKeyOverridesToDeps parses and validates CLI/YAML keymap overrides and applies them to deps.
// Lives in package main to avoid adding imports to main.go; this file imports keymap.
//
// THREE layers merge PER ACTION (a higher layer rebinds only the actions it
// names), lowest to highest precedence:
//
//	legacy server file (~/.config/mecatl/settings.yaml, DEPRECATED — still
//	    honoured, but a keymap: there fires a stderr deprecation WARN each
//	    startup until the operator moves it)
//	  < client file (~/.config/mecatui/settings.yaml, the client-owned home)
//	  < CLI --keymap flags (highest).
//
// The merged map then goes through keymap.Parse + keymap.Validate unchanged:
// an invalid override still fails startup.
func applyKeyOverridesToDeps(cfg config, deps *ui.Deps) error {
	legacyMap, legacySet, err := readLegacyKeymap()
	if err != nil {
		return err
	}
	clientMap, _, err := readClientKeymap()
	if err != nil {
		return err
	}
	cliMap := keyOverridesFromConfig(cfg)
	merged := mergeKeymaps(mergeKeymaps(legacyMap, clientMap), cliMap)
	if legacySet {
		// Deprecation WARN: a direct stderr line, NOT slog — the baseline-slog
		// redirect (main.go installBaselineSlog) discards slog, and this runs
		// BEFORE tea.NewProgram so the line lands in scrollback ahead of the
		// alt-screen. XDG-relative paths, not a possibly-wrong absolute path.
		fmt.Fprintln(os.Stderr, "mecatui: WARNING: the keymap: key in ~/.config/mecatl/settings.yaml is deprecated; move it to ~/.config/mecatui/settings.yaml (the client settings file). The legacy key still works but will be removed in a future release.")
	}
	if os.Getenv("MECATUI_DEBUG_KEYMAP") == "1" {
		fmt.Fprintf(os.Stderr, "mecatui keymap (legacy YAML): %v\n", legacyMap)
		fmt.Fprintf(os.Stderr, "mecatui keymap (client YAML): %v\n", clientMap)
		fmt.Fprintf(os.Stderr, "mecatui keymap (CLI): %v\n", cliMap)
		fmt.Fprintf(os.Stderr, "mecatui keymap (merged): %v\n", merged)
	}
	if merged == nil {
		return nil
	}
	res, err := keymap.Parse(merged)
	if err != nil {
		return fmt.Errorf("keymap: %w", err)
	}
	if err := keymap.Validate(res); err != nil {
		return fmt.Errorf("keymap: %w", err)
	}
	deps.KeyOverrides = res.ByAction
	return nil
}
