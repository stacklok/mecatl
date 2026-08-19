package cliconfig

import (
	"bytes"
	"flag"
	"strings"
	"testing"
	"time"
)

// TestPrintDefaultsExcludingParity proves PrintDefaultsExcluding with no
// exclusions is BYTE-IDENTICAL to (*flag.FlagSet).PrintDefaults across a
// representative FlagSet exercising every stdlib flag shape (bool, string, int,
// int64, uint, uint64, float64, duration, a back-quoted-name string, and a
// custom flag.Value). Every usage string here stays within helpWrapWidth, so
// this pins the ONE formatter to the standard library's output on short usage
// bodies; TestWrapUsageLines* covers the wrapping divergence on long ones.
func TestPrintDefaultsExcludingParity(t *testing.T) {
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

	var want bytes.Buffer
	fs.SetOutput(&want)
	fs.PrintDefaults()

	var got bytes.Buffer
	PrintDefaultsExcluding(&got, fs, nil)

	if got.String() != want.String() {
		t.Fatalf("PrintDefaultsExcluding diverged from flag.PrintDefaults:\n--- want ---\n%s\n--- got ---\n%s", want.String(), got.String())
	}
}

// TestPrintDefaultsExcludingHonoursExclude proves the exclude set skips the
// named flag's full rendered block and leaves the rest byte-identical to the
// stdlib for the kept flags.
func TestPrintDefaultsExcludingHonoursExclude(t *testing.T) {
	fs := flag.NewFlagSet("exclude", flag.ContinueOnError)
	var a, b, c string
	fs.StringVar(&a, "a", "1", "a flag")
	fs.StringVar(&b, "b", "2", "b flag")
	fs.StringVar(&c, "c", "3", "c flag")

	var want bytes.Buffer
	fs.SetOutput(&want)
	fs.PrintDefaults()
	wantKept := removeFlagBlock(want.String(), "b")

	var got bytes.Buffer
	PrintDefaultsExcluding(&got, fs, map[string]bool{"b": true})

	if got.String() != wantKept {
		t.Fatalf("exclude did not match the stdlib output minus b:\n--- want ---\n%s\n--- got ---\n%s", wantKept, got.String())
	}
}

// TestPrintFlagDefaultMatchesStdlib proves the per-flag selected form matches the
// corresponding line(s) from flag.PrintDefaults when the usage body fits within
// the wrap width (short-name flags with short usage bodies are byte-identical).
func TestPrintFlagDefaultMatchesStdlib(t *testing.T) {
	fs := flag.NewFlagSet("single", flag.ContinueOnError)
	var s string
	fs.StringVar(&s, "thing", "stuff", "a thing flag")

	var want bytes.Buffer
	fs.SetOutput(&want)
	fs.PrintDefaults()

	var got bytes.Buffer
	PrintFlagDefault(&got, fs.Lookup("thing"))

	if got.String() != want.String() {
		t.Fatalf("PrintFlagDefault diverged from flag.PrintDefaults:\n--- want ---\n%s\n--- got ---\n%s", want.String(), got.String())
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

// removeFlagBlock removes the full rendered block for the named flag from s —
// the header line ("  -<name>") plus every immediately following indented usage
// continuation line (those starting with "    \t"). flag.PrintDefaults emits a
// flag's block as the header line followed by one or more indented lines.
func removeFlagBlock(s, name string) string {
	header := "  -" + name + " "
	var out strings.Builder
	lines := strings.SplitAfter(s, "\n")
	skipping := false
	for _, line := range lines {
		if skipping {
			if strings.HasPrefix(line, "    \t") {
				continue
			}
			skipping = false
		}
		if strings.HasPrefix(line, header) {
			skipping = true
			continue
		}
		out.WriteString(line)
	}
	return out.String()
}
