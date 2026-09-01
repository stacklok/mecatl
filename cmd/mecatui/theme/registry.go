package theme

import (
	"slices"
	"strings"
)

// defaultName is the theme selected when no --theme flag or config key is given.
const defaultName = "aztec"

// builtins holds the compiled built-in themes, keyed by lowercase name. It is
// the lowest layer of the resolution order; user JSON themes (load.go) override
// or extend it.
var builtins = map[string]Theme{
	"aztec": New("aztec", aztecPalette),
	"mono":  New("mono", monoPalette),
	"solar": New("solar", solarPalette),
}

// Registry holds a set of themes by name and answers Get/List/Default. main.go
// builds one (built-ins + any loaded user themes) and hands the resolved Theme
// to the ui. Keeping registration in an instance (rather than a global) keeps
// tests isolated and the package free of init-order surprises.
type Registry struct {
	themes map[string]Theme
}

// NewRegistry returns a registry seeded with the built-in themes.
func NewRegistry() *Registry {
	r := &Registry{themes: make(map[string]Theme, len(builtins))}
	for name, t := range builtins {
		r.themes[name] = t
	}
	return r
}

// Register adds or replaces a theme by its (lowercased-by-caller) name. User
// JSON themes are registered here, overriding a built-in of the same name.
func (r *Registry) Register(t Theme) {
	r.themes[t.Name] = t
}

// Solar returns the built-in "solar" theme — the light-leaning variant used as
// the automatic fallback when the terminal reports a light background (ADR
// 0280) and no explicit theme was requested. It always returns the built-in,
// never a user override registered under the same name, so the auto-detect
// outcome is predictable regardless of --theme-dir contents.
func Solar() Theme { return builtins["solar"] }

// Get returns the theme for name and whether it was found. Lookup is
// case-insensitive (keys are stored lowercase), so "--theme Aztec" resolves.
func (r *Registry) Get(name string) (Theme, bool) {
	t, ok := r.themes[strings.ToLower(name)]
	return t, ok
}

// Default returns the Aztec theme — the guaranteed-present fallback.
func (r *Registry) Default() Theme {
	return r.themes[defaultName]
}

// Resolve returns the named theme, or the default if name is empty/unknown,
// plus whether the requested name was actually found. This is the one call
// main.go needs: "give me what the user asked for, but never fail".
func (r *Registry) Resolve(name string) (Theme, bool) {
	if name == "" {
		return r.Default(), true
	}
	if t, ok := r.themes[strings.ToLower(name)]; ok {
		return t, true
	}
	return r.Default(), false
}

// List returns the sorted names of all registered themes (for a --list-themes
// affordance or a runtime /theme picker).
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.themes))
	for name := range r.themes {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
