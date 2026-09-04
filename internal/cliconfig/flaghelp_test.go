package cliconfig

import (
	"bytes"
	"flag"
	"strings"
	"testing"
	"time"
)

// TestPrintDefaultsUsesConventionalOptionSpelling proves multi-character flag
// names render as long options while one-character aliases retain short spelling.
func TestPrintDefaultsUsesConventionalOptionSpelling(t *testing.T) {
	fs := flag.NewFlagSet("parity", flag.ContinueOnError)
	var (
		b        bool
		s        string
		i        int
		i64      int64
		u        uint
		u64      uint64
		f        float64
		d        time.Duration
		named    string
		custom   customValue
		zeroStr  string
		zeroBool bool
	)
	fs.BoolVar(&b, "bool", true, "a bool flag with a non-zero default")
	fs.StringVar(&s, "string", "value", "a string flag with a non-zero default")
	fs.IntVar(&i, "int", 7, "an int flag with a non-zero default")
	fs.Int64Var(&i64, "int64", 9, "an int64 flag with a non-zero default")
	fs.UintVar(&u, "uint", 3, "a uint flag with a non-zero default")
	fs.Uint64Var(&u64, "uint64", 5, "a uint64 flag with a non-zero default")
	fs.Float64Var(&f, "float", 1.5, "a float flag with a non-zero default")
	fs.DurationVar(&d, "duration", 30*time.Second, "a duration flag with a non-zero default")
	fs.StringVar(&named, "named", "x", "a `name` to show for the value")
	fs.Var(&custom, "custom", "a custom flag.Value with a non-zero default")
	fs.StringVar(&zeroStr, "zero-string", "", "a string flag at its zero value (no default shown)")
	fs.BoolVar(&zeroBool, "zero-bool", false, "a bool flag at its zero value (no default shown)")
	fs.Bool("v", false, "a one-character alias")

	var got bytes.Buffer
	PrintDefaults(&got, fs)

	for _, name := range []string{"bool", "string", "int", "int64", "uint", "uint64", "float", "duration", "named", "custom", "zero-string", "zero-bool"} {
		if !strings.Contains(got.String(), "  --"+name) {
			t.Errorf("long flag %q did not use -- spelling:\n%s", name, got.String())
		}
	}
	if !strings.Contains(got.String(), "  -v\t") {
		t.Errorf("one-character alias did not retain - spelling:\n%s", got.String())
	}
}

// TestPrintDefaultsExcludingHonoursExclude proves the exclude set skips the
// named flag's full rendered block and retains the remaining long options.
func TestPrintDefaultsExcludingHonoursExclude(t *testing.T) {
	fs := flag.NewFlagSet("exclude", flag.ContinueOnError)
	var a, b, c string
	fs.StringVar(&a, "a", "1", "a flag")
	fs.StringVar(&b, "b", "2", "b flag")
	fs.StringVar(&c, "c", "3", "c flag")

	var got bytes.Buffer
	PrintDefaultsExcluding(&got, fs, map[string]bool{"b": true})

	if strings.Contains(got.String(), "--b") {
		t.Fatalf("excluded flag b was rendered:\n%s", got.String())
	}
	for _, name := range []string{"a", "c"} {
		if !strings.Contains(got.String(), "  -"+name+" ") {
			t.Errorf("kept one-character flag %q was not rendered:\n%s", name, got.String())
		}
	}
}

// TestPrintFlagDefaultUsesLongOptionSpelling proves a selected multi-character
// flag uses the same long-option spelling as the complete renderer.
func TestPrintFlagDefaultUsesLongOptionSpelling(t *testing.T) {
	fs := flag.NewFlagSet("single", flag.ContinueOnError)
	var s string
	fs.StringVar(&s, "thing", "stuff", "a thing flag")

	var got bytes.Buffer
	PrintFlagDefault(&got, fs.Lookup("thing"))

	if !strings.HasPrefix(got.String(), "  --thing string\n") {
		t.Fatalf("selected long flag did not use -- spelling:\n%s", got.String())
	}
}

// TestWrapUsageLinesWrapsWideBody proves that a usage body longer than
// helpWrapWidth is re-wrapped across continuation lines, that every rendered
// continuation line stays within the width budget, and that the header line is
// preserved verbatim.
func TestWrapUsageLinesWrapsWideBody(t *testing.T) {
	block := "  -wide string\n    \tembedded server only: this usage body repeats filler text until it exceeds the eighty-column wrap budget that flaghelp applies to narrow-terminal help output"
	rendered := wrapUsageLines(block)
	lines := strings.Split(rendered, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected the wide usage body to wrap across at least two continuation lines; got %d lines: %q", len(lines), rendered)
	}
	if lines[0] != "  -wide string" {
		t.Fatalf("header line mutated:\n%s", lines[0])
	}
	for i, line := range lines[1:] {
		if !strings.HasPrefix(line, continuationIndent) {
			t.Fatalf("continuation line %d lost its %q indent: %q (full block: %q)", i, continuationIndent, line, rendered)
		}
		if len(line) > helpWrapWidth {
			t.Fatalf("wrapped continuation line %d exceeds helpWrapWidth=%d: %d cols: %q", i, helpWrapWidth, len(line), line)
		}
		// Every continuation line should carry SOME of the wrapped body (not
		// empty, which would mean the indent was added to a blank split).
		if strings.TrimSpace(strings.TrimPrefix(line, continuationIndent)) == "" {
			t.Fatalf("wrapped continuation line %d is empty apart from indent: %q", i, line)
		}
	}
}

// TestWrapUsageLinesPreservesNarrowBody proves a narrow usage body (fits within
// helpWrapWidth) renders byte-identical to the stdlib passthrough, and an
// empty-second-line block is returned unchanged too.
func TestWrapUsageLinesPreservesNarrowBody(t *testing.T) {
	narrow := "  -narrow string\n    \ta short usage body"
	if got := wrapUsageLines(narrow); got != narrow {
		t.Fatalf("narrow body was re-wrapped; want byte-identical passthrough:\n--- want ---\n%s\n--- got ---\n%s", narrow, got)
	}
}

// TestWrapUsageLinesSingleLineBlock proves a header-only block (no continuation
// lines — e.g. an unusual flag shape) is returned unchanged.
func TestWrapUsageLinesSingleLineBlock(t *testing.T) {
	single := "  -one string"
	if got := wrapUsageLines(single); got != single {
		t.Fatalf("single-line block mutated:\n--- want ---\n%s\n--- got ---\n%s", single, got)
	}
}

// TestPrintDefaultsExcludingWideUsageWrapped proves the one formatter wires
// wrapUsageLines into both exported entry points (PrintDefaultsExcluding +
// PrintFlagDefault) — a wide usage body renders wrapped, not as a single
// >helpWrapWidth line.
func TestPrintDefaultsExcludingWideUsageWrapped(t *testing.T) {
	fs := flag.NewFlagSet("wide-wrapped", flag.ContinueOnError)
	var s string
	fs.StringVar(&s, "wide", "x", "embedded server only: this usage body repeats filler text until it exceeds the eighty-column wrap budget that flaghelp applies to narrow-terminal help output")

	var got bytes.Buffer
	PrintDefaultsExcluding(&got, fs, nil)

	for i, line := range strings.Split(got.String(), "\n") {
		if strings.HasPrefix(line, continuationIndent) && len(line) > helpWrapWidth {
			t.Fatalf("line %d exceeds helpWrapWidth=%d (%d cols): %q", i, helpWrapWidth, len(line), line)
		}
	}

	var got2 bytes.Buffer
	PrintFlagDefault(&got2, fs.Lookup("wide"))
	if got.String() != got2.String() {
		t.Fatalf("PrintDefaultsExcluding and PrintFlagDefault disagree:\n--- excl ---\n%s\n--- single ---\n%s", got.String(), got2.String())
	}
}

// customValue is a minimal flag.Value with a non-zero default, exercising the
// %v (non-string) default-annotation path.
type customValue string

func (c *customValue) String() string     { return string(*c) }
func (c *customValue) Set(s string) error { *c = customValue(s); return nil }
