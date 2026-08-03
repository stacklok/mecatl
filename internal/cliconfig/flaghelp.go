package cliconfig

import (
	"flag"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// PrintDefaultsExcluding writes a flag.FlagSet's defaults in the EXACT format
// of (*flag.FlagSet).PrintDefaults, skipping any flag whose name is in exclude.
// It is the ONE filtered-defaults formatter shared by the cmd mains' progressive
// help renderers, so there is no second hand-rolled PrintDefaults whose per-line
// format could drift from the standard library's (the review found exactly that
// drift: a bespoke formatter that always emitted "(default X)" and never the
// back-quoted/type name, diverging from flag.PrintDefaults).
//
// It does NOT mutate, rebind, or copy the source FlagSet: it iterates the real
// flags via VisitAll and formats each in place, so original flag.Value defaults
// are untouched. A nil exclude set (or an empty map) renders every flag, matching
// PrintDefaults byte-for-byte (see TestPrintDefaultsExcludingParity).
//
// The per-flag formatting mirrors flag.PrintDefaults: the back-quoted/type name
// from flag.UnquoteUsage, the four-space+tab indent, the embedded-newline
// rewrite, and the default-value annotation (omitted for zero values, %q for
// string flags, %v otherwise) computed via the same reflect-based zero check
// the standard library uses.
func PrintDefaultsExcluding(w io.Writer, fs *flag.FlagSet, exclude map[string]bool) {
	fs.VisitAll(func(f *flag.Flag) {
		if exclude != nil && exclude[f.Name] {
			return
		}
		_, _ = fmt.Fprint(w, formatFlagDefault(f), "\n")
	})
}

// PrintFlagDefault writes a single flag's usage line in the EXACT format of
// flag.PrintDefaults. It is the selected/predicate form grouped progressive-help
// renderers use (a grouped renderer sorts + selects flag names, then calls this
// per flag), keeping ONE formatter implementation behind both the exhaustive and
// the grouped paths.
func PrintFlagDefault(w io.Writer, f *flag.Flag) {
	_, _ = fmt.Fprint(w, formatFlagDefault(f), "\n")
}

// formatFlagDefault returns the single-flag usage string flag.PrintDefaults
// builds internally (without the trailing newline). It is a faithful
// reimplementation of the unexported per-flag body of (*FlagSet).PrintDefaults.
func formatFlagDefault(f *flag.Flag) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  -%s", f.Name)
	name, usage := flag.UnquoteUsage(f)
	if len(name) > 0 {
		b.WriteString(" ")
		b.WriteString(name)
	}
	if b.Len() <= 4 { // space, space, '-', 'x' — one-byte flag on same line.
		b.WriteString("\t")
	} else {
		b.WriteString("\n    \t")
	}
	b.WriteString(strings.ReplaceAll(usage, "\n", "\n    \t"))
	if isZero, ok := isZeroFlagValue(f); ok && !isZero {
		// flag.PrintDefaults quotes the default ONLY for the standard library's
		// *stringValue. flag.UnquoteUsage's returned name is NOT a reliable
		// discriminator (a back-quoted name overrides it), so detect *stringValue
		// by the reflected element type's package + name, which is the exact
		// check the stdlib's type switch performs.
		if isStdStringValue(f.Value) {
			fmt.Fprintf(&b, " (default %q)", f.DefValue)
		} else {
			fmt.Fprintf(&b, " (default %v)", f.DefValue)
		}
	}
	return b.String()
}

// isStdStringValue reports whether v is the standard library's *flag.stringValue
// — the type flag.PrintDefaults narrows on for %q-quoting the default. The
// unexported *stringValue cannot be type-asserted, so this inspects the reflected
// element type's package path ("flag") and name ("stringValue"), which together
// identify it exactly (a consumer's own string-backed Value type has a different
// PkgPath). It fail-safes to false on any reflect panic (then the default is
// rendered %v, never silently dropped).
func isStdStringValue(v flag.Value) bool {
	defer func() { _ = recover() }()
	t := reflect.TypeOf(v)
	if t == nil || t.Kind() != reflect.Pointer {
		return false
	}
	e := t.Elem()
	return e.PkgPath() == "flag" && e.Name() == "stringValue"
}

// isZeroFlagValue reports whether f.DefValue equals the String() of a zero value
// of f's flag.Value type — the same check the standard library's isZeroValue
// performs. The bool result is false (meaning "treat as non-zero, show default")
// when the reflect-based zero construction panics, matching the stdlib's
// fail-safe behaviour of still printing the default. The stdlib additionally
// collects the panic errors for a trailing notice; progressive help never needs
// that notice (the cmd mains' flag.Value types are all safe), so it is omitted.
func isZeroFlagValue(f *flag.Flag) (isZero bool, ok bool) {
	defer func() { _ = recover() }()
	typ := reflect.TypeOf(f.Value)
	var z reflect.Value
	if typ.Kind() == reflect.Pointer {
		z = reflect.New(typ.Elem())
	} else {
		z = reflect.Zero(typ)
	}
	return f.DefValue == z.Interface().(flag.Value).String(), true
}
