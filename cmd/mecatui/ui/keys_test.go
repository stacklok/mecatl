package ui

import (
	"reflect"
	"testing"
)

// keyMapFieldNames returns the exported field names of the keyMap struct via
// reflection — the canonical set of rebindable action names.
func keyMapFieldNames() []string {
	tp := reflect.TypeOf(keyMap{})
	names := make([]string, 0, tp.NumField())
	for i := 0; i < tp.NumField(); i++ {
		names = append(names, tp.Field(i).Name)
	}
	return names
}

// TestDefaultRawArgsBinding pins the RawArgs default: a bare "r" (consulted
// ONLY inside the full-screen ask-args view) with the "raw args" help text.
func TestDefaultRawArgsBinding(t *testing.T) {
	b := defaultKeys().RawArgs
	keys := b.Keys()
	if len(keys) != 1 || keys[0] != "r" {
		t.Errorf("RawArgs default keys = %v, want [r]", keys)
	}
	if h := b.Help(); h.Key != "r" || h.Desc != "raw args" {
		t.Errorf("RawArgs default help = %v, want {r raw args}", h)
	}
}

// TestDefaultModeSwitchBinding pins the conventional Shift+Tab default and its
// matching help label; Alt+M remains available only through a user override.
func TestDefaultModeSwitchBinding(t *testing.T) {
	b := defaultKeys().ModeSwitch
	if keys := b.Keys(); len(keys) != 1 || keys[0] != "shift+tab" {
		t.Errorf("ModeSwitch default keys = %v, want [shift+tab]", keys)
	}
	if h := b.Help(); h.Key != "shift+tab" || h.Desc != "switch mode" {
		t.Errorf("ModeSwitch default help = %v, want {shift+tab switch mode}", h)
	}
}

// TestRawArgsKeyMarking pins the helpKeyMarkings seam: the rawArgs marking reads
// the LIVE binding's first chord, falling back to "r" for an empty binding.
func TestRawArgsKeyMarking(t *testing.T) {
	if got := keyMarkings(defaultKeys()).rawArgs; got != "r" {
		t.Errorf("default rawArgs marking = %q, want r", got)
	}
	km := applyKeyOverrides(defaultKeys(), map[string][]string{"RawArgs": {"ctrl+f20"}})
	if got := keyMarkings(km).rawArgs; got != "ctrl+f20" {
		t.Errorf("overridden rawArgs marking = %q, want ctrl+f20", got)
	}
}

// TestEveryKeyHasOverrideSetter pins the rebindable-key parity invariant (issue #228
// added EditBack): EVERY keyMap field must have an applyKeyOverrides setter, or that
// key silently becomes non-rebindable. We probe the setter by overriding each action
// to a distinct sentinel chord and asserting applyKeyOverrides actually rebound it.
func TestEveryKeyHasOverrideSetter(t *testing.T) {
	base := defaultKeys()
	for _, name := range keyMapFieldNames() {
		name := name
		t.Run(name, func(t *testing.T) {
			const sentinel = "ctrl+f19" // a chord no default binding uses
			got := applyKeyOverrides(base, map[string][]string{name: {sentinel}})
			binding := reflect.ValueOf(got).FieldByName(name)
			// key.Binding.Keys() is the current chord set; assert it now contains the
			// sentinel — i.e. the setter fired for this field.
			out := binding.MethodByName("Keys").Call(nil)
			keys, _ := out[0].Interface().([]string)
			found := false
			for _, k := range keys {
				if k == sentinel {
					found = true
				}
			}
			if !found {
				t.Fatalf("keyMap field %q has no applyKeyOverrides setter (override did not take): keys=%v", name, keys)
			}
		})
	}
}
