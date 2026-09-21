package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/parser"

	"github.com/stacklok/mecatl/cmd/mecatui/keymap"
	statusline "github.com/stacklok/mecatl/cmd/mecatui/statusline"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/adapter/yamldiag"
)

// clientSettings is the STRICT top-level schema of the client-owned settings
// file (~/.config/mecatui/settings.yaml). The client file is mecatui's own —
// unlike the shared server file it is decoded with KnownFields(true), so an
// unrecognized top-level key is a parse error, not a silently-ignored typo.
// It is designed to grow additive fields later (theme, UI behaviour, …).
type clientSettings struct {
	Keymap              map[string]string    `yaml:"keymap"`
	StatusCustomization *statusCustomization `yaml:"status_customization"`
	TerminalTitle       terminalTitleSettings
}

type terminalTitleSettings struct {
	Enabled  bool
	Template string
}

type terminalTitleSettingsYAML struct {
	Enabled  *bool  `yaml:"enabled"`
	Template string `yaml:"template"`
}

func shippedTerminalTitleSettings() terminalTitleSettings {
	return terminalTitleSettings{Enabled: true, Template: statusline.DefaultTitleTemplate()}
}

func newTitleRenderer(settings terminalTitleSettings) (*statusline.TitleRenderer, error) {
	return statusline.NewTitleRenderer(settings.Template)
}

func defaultClientSettings() clientSettings {
	return clientSettings{TerminalTitle: shippedTerminalTitleSettings()}
}

// clientSettingsYAML is the strict decode shape. A duration stays textual until
// after the strict YAML decode so the accepted duration syntax is explicit.
type clientSettingsYAML struct {
	Keymap              map[string]string          `yaml:"keymap"`
	StatusCustomization *statusCustomizationYAML   `yaml:"status_customization"`
	TerminalTitle       *terminalTitleSettingsYAML `yaml:"terminal_title"`
}

type statusCustomizationYAML struct {
	Templates *statusTemplates `yaml:"templates"`
	Command   *statusCommand   `yaml:"command"`
	Interval  string           `yaml:"interval"`
}

// statusCustomization is inert client configuration. A nil Templates and
// Command selects the shipped status template; rendering and command execution
// deliberately belong to later client layers.
type statusCustomization struct {
	Templates *statusTemplates
	Command   *statusCommand
	Interval  time.Duration
}

// statusTemplates supplies independent header and footer responsive variants.
type statusTemplates struct {
	Header *statusSurfaceTemplates `yaml:"header"`
	Footer *statusSurfaceTemplates `yaml:"footer"`
}

type statusSurfaceTemplates struct {
	Full    string `yaml:"full"`
	Compact string `yaml:"compact"`
	Minimal string `yaml:"minimal"`
}

// statusCommand is a trusted user-global direct executable selection. Its
// PassthroughEnv list is an explicit allowlist; the status source owns the
// remaining environment and working-directory policy.
type statusCommand struct {
	Path           string   `yaml:"executable"`
	Args           []string `yaml:"args"`
	PassthroughEnv []string `yaml:"passthrough_env"`
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

// readClientSettings reads the one strict client-owned settings document. An
// absent file (or unresolvable config base) yields the zero settings value.
func readClientSettings() (clientSettings, error) {
	path := clientSettingsPath(xdgconfig.OSEnv)
	if path == "" {
		return defaultClientSettings(), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultClientSettings(), nil
		}
		return clientSettings{}, fmt.Errorf("read %s: %w", path, err)
	}
	document, err := yamldiag.ParseSettingsDocument(b)
	if err != nil {
		if errors.Is(err, yamldiag.ErrMultipleDocuments) {
			return clientSettings{}, fmt.Errorf("parsing %s: multiple documents are not supported (the client settings schema is a single document)", path)
		}
		return clientSettings{}, clientKeymapSyntaxError(path, err)
	}
	var raw clientSettingsYAML
	if err := yaml.NewDecoder(bytes.NewReader(nil), yaml.DisallowUnknownField()).DecodeFromNode(document.Mapping(), &raw); err != nil {
		return clientSettings{}, clientSettingsSchemaError(path, err)
	}
	status, err := decodeStatusCustomization(raw.StatusCustomization)
	if err != nil {
		var passthroughErr *statusline.PassthroughEnvError
		if errors.As(err, &passthroughErr) {
			return clientSettings{}, fmt.Errorf("parsing %s: %w", path, err)
		}
		return clientSettings{}, fmt.Errorf("parsing %s: invalid status_customization configuration", path)
	}
	title, err := decodeTerminalTitle(raw.TerminalTitle)
	if err != nil {
		return clientSettings{}, fmt.Errorf("parsing %s: terminal_title.template: %w", path, err)
	}
	return clientSettings{Keymap: raw.Keymap, StatusCustomization: status, TerminalTitle: title}, nil
}

func decodeTerminalTitle(raw *terminalTitleSettingsYAML) (terminalTitleSettings, error) {
	if raw == nil {
		return shippedTerminalTitleSettings(), nil
	}
	out := shippedTerminalTitleSettings()
	if raw.Enabled != nil {
		out.Enabled = *raw.Enabled
	}
	if raw.Template != "" {
		out.Template = raw.Template
	}
	if _, err := newTitleRenderer(out); err != nil {
		return terminalTitleSettings{}, err
	}
	return out, nil
}

func clientSettingsSchemaError(path string, err error) error {
	const guidance = "this is the client-owned settings file; server configuration such as models: belongs in ~/.config/mecatl/settings.yaml"

	diagnostic := yamldiag.Classify("parse client settings", err)
	if diagnostic.HasLocation {
		return fmt.Errorf("parsing %s: does not match the expected client settings schema at line %d, column %d (unknown key or type, including terminal_title; %s)", path, diagnostic.Line, diagnostic.Column, guidance)
	}
	return fmt.Errorf("parsing %s: does not match the expected client settings schema (unknown key or type, including terminal_title; %s)", path, guidance)
}
func clientKeymapSyntaxError(path string, err error) error {
	var documentError *yamldiag.DocumentError
	if errors.As(err, &documentError) && documentError.Location.HasLocation {
		return fmt.Errorf("parsing %s: invalid YAML syntax at line %d, column %d (the document must be valid YAML matching the client settings schema, including terminal_title)", path, documentError.Location.Line, documentError.Location.Column)
	}
	return fmt.Errorf("parsing %s: invalid YAML syntax (the document must be valid YAML matching the client settings schema, including terminal_title)", path)
}

// readClientKeymap reads the CLIENT-owned settings file
// (~/.config/mecatui/settings.yaml) and returns its keymap overrides as
// action -> []chords. An absent file (or unresolvable config base) returns
// (nil, false, nil). The bool reports "the client file contributed a keymap".
func readClientKeymap() (map[string][]string, bool, error) {
	s, err := readClientSettings()
	if err != nil {
		return nil, false, err
	}
	out := splitKeymap(s.Keymap)
	if out == nil {
		return nil, false, nil
	}
	return out, true, nil
}

// shippedStatusCustomization is the inert default selector. Template literals
// live with the renderer, not the settings parser.
func shippedStatusCustomization() statusCustomization { return statusCustomization{} }

// newSource adapts validated settings into the source.
func newSource(customization statusCustomization) statusline.Source {
	if customization.Command != nil {
		launchDir, _ := os.Getwd()
		return statusline.NewCommandSource(statusline.Command{
			Path: customization.Command.Path, Args: customization.Command.Args, PassthroughEnv: customization.Command.PassthroughEnv, LaunchDir: launchDir, RefreshInterval: customization.Interval,
		})
	}
	if customization.Templates == nil {
		return statusline.NewDefaultSource(customization.Interval)
	}
	return statusline.NewTemplateSource(statusline.TemplateSet{
		Header: toSurfaceTemplates(customization.Templates.Header),
		Footer: toSurfaceTemplates(customization.Templates.Footer),
	}, customization.Interval)
}

func toSurfaceTemplates(value *statusSurfaceTemplates) statusline.SurfaceTemplates {
	if value == nil {
		return statusline.SurfaceTemplates{}
	}
	return statusline.SurfaceTemplates{Full: value.Full, Compact: value.Compact, Minimal: value.Minimal}
}

// readStatusCustomization reads only the strict mecatui client settings file.
// A missing status_customization key selects the renderer's shipped default.
func readStatusCustomization() (statusCustomization, error) {
	s, err := readClientSettings()
	if err != nil {
		return statusCustomization{}, err
	}
	if s.StatusCustomization == nil {
		return shippedStatusCustomization(), nil
	}
	return *s.StatusCustomization, nil
}

func decodeStatusCustomization(raw *statusCustomizationYAML) (*statusCustomization, error) {
	if raw == nil {
		return nil, nil
	}
	if (raw.Templates == nil) == (raw.Command == nil) {
		return nil, errors.New("choose exactly one status source")
	}
	if raw.Templates != nil && raw.Templates.Header == nil && raw.Templates.Footer == nil {
		return nil, errors.New("template source has no surface")
	}
	if raw.Command != nil {
		command := statusline.Command{
			Path: raw.Command.Path, Args: raw.Command.Args, PassthroughEnv: raw.Command.PassthroughEnv,
		}
		if err := command.ValidatePassthroughEnv(); err != nil {
			return nil, err
		}
		if !command.Valid() {
			return nil, errors.New("invalid command")
		}
	}
	if raw.Templates != nil && (!validStatusSurfaceTemplates(raw.Templates.Header) || !validStatusSurfaceTemplates(raw.Templates.Footer)) {
		return nil, errors.New("template source has no variant")
	}
	out := &statusCustomization{Templates: raw.Templates, Command: raw.Command}
	if raw.Interval != "" {
		interval, err := time.ParseDuration(raw.Interval)
		if err != nil || interval < time.Second {
			return nil, errors.New("invalid interval")
		}
		out.Interval = interval
	}
	return out, nil
}

func validStatusSurfaceTemplates(value *statusSurfaceTemplates) bool {
	if value == nil {
		return true
	}
	return strings.TrimSpace(value.Full) != "" && strings.TrimSpace(value.Compact) != "" && strings.TrimSpace(value.Minimal) != ""
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
		// Parser failures and decode failures remain opaque: parser-rendered
		// messages can contain YAML-derived content. A successful parse proves
		// this is the historical wrong-keymap-shape path; otherwise it is syntax.
		if _, parseErr := parser.ParseBytes(b, 0); parseErr != nil {
			return nil, false, fmt.Errorf("parse %s: invalid YAML syntax (check the file's line structure)", path)
		}
		return nil, false, fmt.Errorf("parse %s: the keymap: key must be a mapping of action -> chord(s) (wrong type under keymap)", path)
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
func applyKeyOverridesToDeps(cfg config, settings clientSettings, deps *ui.Deps) error {
	legacyMap, legacySet, err := readLegacyKeymap()
	if err != nil {
		return err
	}
	clientMap := splitKeymap(settings.Keymap)
	cliMap := keyOverridesFromConfig(cfg)
	merged := mergeKeymaps(mergeKeymaps(legacyMap, clientMap), cliMap)
	if legacySet {
		// Deprecation WARN: a direct stderr line, NOT slog — the baseline-slog
		// redirect (main.go installBaselineSlog) discards slog, and this runs
		// BEFORE tea.NewProgram so the line lands in scrollback ahead of the
		// alt-screen. XDG-relative paths, not a possibly-wrong absolute path.
		fmt.Fprintln(os.Stderr, "mecatui: WARNING: the keymap: key in ~/.config/mecatl/settings.yaml is deprecated; move it to ~/.config/mecatui/settings.yaml (the client settings file). The legacy key still works but will be removed in a future release.")
	}
	if cfg.debugKeymap {
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
