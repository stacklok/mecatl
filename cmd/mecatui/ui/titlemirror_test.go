package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// mirrorSink is the term.File the mirror wraps in tests: writes are captured,
// Fd reports a sentinel so forwarding is observable.
type mirrorSink struct {
	bytes.Buffer
	closed bool
}

func (*mirrorSink) Read(_ []byte) (int, error) { return 0, nil }
func (s *mirrorSink) Close() error             { s.closed = true; return nil }
func (*mirrorSink) Fd() uintptr                { return 42 }

// TestTitleMirrorInjectsOSC1 pins the issue-#1460 contract at the byte level:
// a window-title sequence (OSC 2) passing through the wrapped output is
// followed by an icon-name twin (OSC 1) with the byte-identical payload —
// which is what iTerm2 labels tabs from — and all original bytes survive
// verbatim in order.
func TestTitleMirrorInjectsOSC1(t *testing.T) {
	t.Parallel()

	sink := &mirrorSink{}
	w := NewTitleMirror(sink)

	frame := "before" + ansi.SetWindowTitle("fix the login bug — Working mecatui") + "after"
	if _, err := w.Write([]byte(frame)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := "before" + ansi.SetWindowTitle("fix the login bug — Working mecatui") +
		ansi.SetIconName("fix the login bug — Working mecatui") + "after"
	if got := sink.String(); got != want {
		t.Errorf("mirrored output = %q, want %q", got, want)
	}
}

// TestTitleMirrorHandlesSplitWrites pins that scanner state survives Write
// boundaries: nothing guarantees the renderer's title sequence arrives in one
// Write, so the mirror must reassemble a sequence split at every possible
// byte position and still inject exactly one twin.
func TestTitleMirrorHandlesSplitWrites(t *testing.T) {
	t.Parallel()

	full := "x" + ansi.SetWindowTitle("split me") + "y"
	want := "x" + ansi.SetWindowTitle("split me") + ansi.SetIconName("split me") + "y"
	for cut := 1; cut < len(full); cut++ {
		sink := &mirrorSink{}
		w := NewTitleMirror(sink)
		if _, err := w.Write([]byte(full[:cut])); err != nil {
			t.Fatalf("cut %d first Write: %v", cut, err)
		}
		if _, err := w.Write([]byte(full[cut:])); err != nil {
			t.Fatalf("cut %d second Write: %v", cut, err)
		}
		if got := sink.String(); got != want {
			t.Errorf("cut %d: output = %q, want %q", cut, got, want)
		}
	}
}

// TestTitleMirrorSTTerminator pins that the ST-terminated form ("\x1b]2;…\x1b\\")
// is mirrored too: ansi.SetWindowTitle happens to use BEL today, but OSC
// defines both terminators and the mirror must not couple to the current
// encoding choice of a dependency.
func TestTitleMirrorSTTerminator(t *testing.T) {
	t.Parallel()

	sink := &mirrorSink{}
	w := NewTitleMirror(sink)
	seq := "\x1b]2;st title\x1b\\"
	if _, err := w.Write([]byte(seq)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got, want := sink.String(), seq+ansi.SetIconName("st title"); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// TestTitleMirrorEmptyTitleClearsIconName pins exit cleanup: the renderer
// clears the window title with an empty OSC 2 when the program quits, and the
// mirror must clear the icon name with it so a closed mecatui does not leave
// a stale conversation title on the tab.
func TestTitleMirrorEmptyTitleClearsIconName(t *testing.T) {
	t.Parallel()

	sink := &mirrorSink{}
	w := NewTitleMirror(sink)
	if _, err := w.Write([]byte(ansi.SetWindowTitle(""))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got, want := sink.String(), ansi.SetWindowTitle("")+ansi.SetIconName(""); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// TestTitleMirrorPassthroughUntouched pins that non-title output — including
// other OSC sequences (icon names, hyperlinks, OSC 52 clipboard) and CSI-heavy
// frames — passes through byte-exact with no injection. The mirror must never
// corrupt a frame; a sequence it does not recognize is not its business.
func TestTitleMirrorPassthroughUntouched(t *testing.T) {
	t.Parallel()

	cases := []string{
		"plain text, no escapes at all",
		"\x1b[31mred\x1b[0m and \x1b[1;2H cursor moves",
		"\x1b]0;osc zero\x07",                     // OSC 0 is NOT ours to double
		"\x1b]1;already an icon\x07",              // an existing OSC 1 is left alone
		"\x1b]52;c;aGVsbG8=\x07",                  // clipboard
		"\x1b]8;;https://x.test\x07L\x1b]8;;\x07", // hyperlink
		"\x1b]2",                  // truncated introducer at end of stream
		"\x1b]2;never terminated", // unterminated: nothing to mirror
	}
	for _, in := range cases {
		sink := &mirrorSink{}
		w := NewTitleMirror(sink)
		if _, err := w.Write([]byte(in)); err != nil {
			t.Fatalf("%q: Write: %v", in, err)
		}
		if got := sink.String(); got != in {
			t.Errorf("input %q: output = %q, want byte-identical passthrough", in, got)
		}
	}
}

// TestTitleMirrorAbortedSequenceResynchronizes pins the malformed-payload arm:
// a bare ESC inside an OSC 2 payload aborts the sequence (no mirror), and the
// scanner re-synchronizes so a following well-formed title is still mirrored —
// including when the aborting ESC itself begins the next introducer.
func TestTitleMirrorAbortedSequenceResynchronizes(t *testing.T) {
	t.Parallel()

	sink := &mirrorSink{}
	w := NewTitleMirror(sink)
	in := "\x1b]2;broken\x1b[31m" + ansi.SetWindowTitle("recovered")
	if _, err := w.Write([]byte(in)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got, want := sink.String(), in+ansi.SetIconName("recovered"); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}

	// The aborting ESC starting a fresh introducer immediately.
	sink2 := &mirrorSink{}
	w2 := NewTitleMirror(sink2)
	in2 := "\x1b]2;broken" + ansi.SetWindowTitle("second")
	if _, err := w2.Write([]byte(in2)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got, want := sink2.String(), in2+ansi.SetIconName("second"); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// TestTitleMirrorOverflowIsNotMirrored pins the payload bound: a runaway OSC 2
// far past any real title is passed through but NOT buffered or mirrored, and
// the scanner still re-synchronizes on the terminator.
func TestTitleMirrorOverflowIsNotMirrored(t *testing.T) {
	t.Parallel()

	sink := &mirrorSink{}
	w := NewTitleMirror(sink)
	huge := "\x1b]2;" + strings.Repeat("a", titleMirrorPayloadCap+100) + "\x07"
	if _, err := w.Write([]byte(huge + ansi.SetWindowTitle("sane"))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := huge + ansi.SetWindowTitle("sane") + ansi.SetIconName("sane")
	if got := sink.String(); got != want {
		t.Errorf("overflow not handled: got %d bytes, want %d; tail %q", len(sink.String()), len(want), tail(sink.String(), 60))
	}
}

// TestTitleMirrorForwardsTermFile pins the detection contract: Fd and Close
// forward to the wrapped file, because Bubble Tea's TTY detection and
// colorprofile.Detect type-assert term.File on the output — a wrapper that
// hid the descriptor would silently degrade color rendering for every user.
func TestTitleMirrorForwardsTermFile(t *testing.T) {
	t.Parallel()

	sink := &mirrorSink{}
	w := NewTitleMirror(sink)
	if got := w.Fd(); got != 42 {
		t.Errorf("Fd() = %d, want the wrapped file's 42", got)
	}
	if err := w.Close(); err != nil || !sink.closed {
		t.Errorf("Close() = %v, closed=%v; want forwarded close", err, sink.closed)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
