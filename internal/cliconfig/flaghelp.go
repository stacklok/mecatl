package cliconfig

import (
	"flag"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// helpWrapWidth is the fixed column budget every help renderer wraps long flag
// usage strings to. 84 = a desktop terminal's usable width after the 2-cell
// header indent and the stdlib's "    \t" continuation indent (4 bytes,
// expanded to a tab stop) are subtracted from an 80-col line — a fixed width
// that keeps output deterministic across CI test and capture renderers while
// still letting each wide usage body occupy most of an 80-col line. Values
// above 84 reclaim the indentation depth; below 60 just makes lines short.
const helpWrapWidth = 84

// PrintDefaultsExcluding writes a flag.FlagSet's defaults in the same format as
// (*flag.FlagSet).PrintDefaults, skipping any flag whose name is in exclude.
// It is the ONE filtered-defaults formatter shared by the cmd mains' progressive
// help renderers, so there is no second hand-rolled PrintDefaults.
//
// It does NOT mutate, rebind, or copy the source FlagSet: it iterates the real
// flags via VisitAll and formats each in place, so original flag.Value defaults
// are untouched. A nil exclude set (or an empty map) renders every flag.
//
// The per-flag formatting mirrors flag.PrintDefaults: the back-quoted/type name
// from flag.UnquoteUsage, the four-space+tab indent, the embedded-newline
// rewrite, and the default-value annotation (omitted for zero values, %q for
// string flags, %v otherwise) computed via the same reflect-based zero check
// the standard library uses. Long usage strings are word-wrapped to
// helpWrapWidth so narrow terminals never receive >80-col lines.
func PrintDefaultsExcluding(w io.Writer, fs *flag.FlagSet, exclude map[string]bool) {
	fs.VisitAll(func(f *flag.Flag) {
		if exclude != nil && exclude[f.Name] {
			return
		}
		_, _ = fmt.Fprint(w, formatFlagDefault(f), "\n")
	})
}

// PrintFlagDefault writes a single flag's usage block in the format of
// flag.PrintDefaults (possibly word-wrapped). It is the selected/predicate form
// grouped progressive-help renderers use (a grouped renderer sorts + selects
// flag names, then calls this per flag), keeping ONE formatter implementation
// behind both the exhaustive and the grouped paths.
func PrintFlagDefault(w io.Writer, f *flag.Flag) {
	_, _ = fmt.Fprint(w, formatFlagDefault(f), "\n")
}

// formatFlagDefault returns the single-flag usage string flag.PrintDefaults
// builds internally (without the trailing newline). It starts as a faithful
// reimplementation of the unexported per-flag body of (*FlagSet).PrintDefaults,
// then word-wraps the rendered block over helpWrapWidth via wrapUsageLines so
// the rendered block's longest line (header or continuation) stays within the
// fixed budget that keeps narrow terminals readable.
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
	return wrapUsageLines(b.String())
}

// continuationIndent is the four-space+tab indent flag.PrintDefaults (and
// formatFlagDefault's stdlib-mirroring layout) puts beneath a flag's header.
// wrapUsageLines re-applies it on every wrapped continuation line so the block
// keeps the stdlib's column alignment.
const continuationIndent = "    \t"

// wrapUsageLines word-wraps long usage lines to helpWrapWidth. The flag header
// line ("  -name …") is left alone; continuation lines (the indented usage
// body flag.PrintDefaults places beneath the header) are re-wrapped so every
// rendered continuation line stays within the width budget. Continuation
// lines are identified by their leading continuationIndent and re-indented
// after wrapping so the wrapped forms align under the header's continuation
// column. When the usage body plus indent fits within helpWrapWidth, the block
// is returned unchanged (byte-identical to the stdlib); multi-line usages
// keep the same word sequence but are split across re-wrapped continuation
// lines — the ONLY divergence from flag.PrintDefaults, introduced to keep
// --help readable at narrow widths.
func wrapUsageLines(block string) string {
	lines := strings.Split(block, "\n")
	if len(lines) < 2 {
		return block
	}
	header, rest := lines[0], lines[1:]
	// Every line after the header must be a continuation (leading
	// continuationIndent, e.g. "    \t<usage>"); any non-continuation line is
	// treated as the end of the block and left verbatim (forward-compatible
	// with an stdlib layout change).
	var bodyLines []string
	for _, line := range rest {
		if !strings.HasPrefix(line, continuationIndent) {
			continue
		}
		bodyLines = append(bodyLines, strings.TrimPrefix(line, continuationIndent))
	}
	if maxWidth(block) <= helpWrapWidth {
		return block
	}
	// widthBudget is the grapheme-cell budget each wrapped continuation line
	// must respect. ansi.Wrap works over grapheme cells (x/ansi treats
	// East-Asian-width correctly), so a tab multi-byte byte-length does not
	// count against the budget the way a plain len() would. Using
	// StringWidth here keeps a rendered continuation line's DISPLAY width —
	// not its byte length — within helpWrapWidth.
	widthBudget := helpWrapWidth - len(continuationIndent)
	body := ansi.Wrap(strings.Join(bodyLines, " "), widthBudget, "")
	wrappedLines := strings.Split(body, "\n")
	for _, line := range wrappedLines {
		if ansi.StringWidth(line) > widthBudget {
			// Grapheme-aware wrap produced an overlong line (e.g. an unbreakable
			// word longer than the budget). Hardwrap at the budget instead of
			// letting narrow terminals see >helpWrapWidth cols.
			body = ansi.Hardwrap(strings.Join(wrappedLines, " "), widthBudget, false)
			break
		}
	}
	return header + "\n" + continuationIndent + strings.ReplaceAll(body, "\n", "\n"+continuationIndent)
}

// maxWidth returns the DISPLAY width (grapheme cells, East-Asian-width-aware,
// matching ansi.Wrap's semantics) of the longest line in block. Numbers
// compare against helpWrapWidth, so a line whose bytes exceed the width but
// render as short cells (multi-byte spaces, wide CJK split graphemes) is
// correctly classified as within budget.
func maxWidth(block string) int {
	maxW := 0
	for _, line := range strings.Split(block, "\n") {
		if w := ansi.StringWidth(line); w > maxW {
			maxW = w
		}
	}
	return maxW
}

// wrapUsageLines continues above; maxWidth is the helper it shares.

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
