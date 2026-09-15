package ui

import (
	"bytes"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// titleMirrorPayloadCap bounds the OSC 2 payload the mirror will buffer while
// scanning for the terminator. The composed window title is well under 200
// bytes (windowTitleRunes caps the title segment at 40 runes); a payload past
// this cap is not ours, so the mirror stops accumulating and lets the sequence
// pass through unmirrored rather than growing without bound.
const titleMirrorPayloadCap = 1024

// NewTitleMirror wraps the terminal output so that every complete window-title
// sequence (OSC 2, `ESC ] 2 ; title BEL|ST`) that passes through is immediately
// followed by an icon-name twin (OSC 1) carrying the byte-identical payload.
//
// Why: Bubble Tea's tea.View.WindowTitle is emitted as OSC 2 only, which sets
// the TITLE BAR. Terminals that honour xterm's title/icon split — iTerm2 —
// label their TABS from the icon name (OSC 1) instead, and fall back to the
// job name when no icon name was ever set: every mecatui tab reads a uniform
// "mecatui" while the real per-conversation title lands in a title bar that a
// maximized tabbed window hides (issue #1460). Terminals that do not
// distinguish the two already showed the OSC 2 title in the tab; the OSC 1
// twin carries the same bytes, so they either ignore it or re-set what is
// already displayed.
//
// Why HERE, at the output writer, rather than in the Update loop: the renderer
// already owns exactly the right emission discipline — it writes OSC 2 only
// when the title actually CHANGED (never per frame) and clears it with an
// empty payload on exit. Mirroring at the byte boundary inherits both for
// free, keeps the two labels provably identical (the payload is copied, never
// recomposed), and touches no reducer: the Update command stream that the UI's
// tests treat as the complete set of side effects is byte-identical with and
// without the mirror. The sequence being mirrored is composed by windowTitle,
// which already sanitized prompt-derived text (C0/ESC/DEL stripped), so the
// copied payload cannot smuggle a terminator.
//
// The wrapper implements term.File — Read/Close/Fd forward to the wrapped
// file — because BOTH Bubble Tea's TTY detection (p.output.(term.File) in
// initInput) and colorprofile.Detect assert that interface to find the real
// descriptor. A plain io.Writer wrapper would make the output look like a
// non-terminal and silently degrade color rendering for every user; forwarding
// Fd() preserves detection exactly.
//
// The composition root decides whether to wrap at all: --terminal-title=off
// simply does not install the mirror, so "off" leaves the icon name untouched
// (whatever the shell set survives) rather than stamping a static app name.
func NewTitleMirror(f term.File) term.File {
	return &titleMirror{f: f}
}

// titleMirror is a stateful pass-through scanner. It never withholds or alters
// the bytes it is given — output is always input plus injected OSC 1 twins —
// so a malformed or truncated sequence degrades to "no mirror", never to
// corrupted frames. Scanner state survives Write boundaries: the renderer
// flushes whole frames, but nothing guarantees a title sequence is not split
// across two Writes.
type titleMirror struct {
	f term.File

	// prefix counts the matched bytes of the OSC 2 introducer "\x1b]2;"
	// (0..3; 4 == inPayload). Reset to 0 on any mismatch.
	prefix int
	// inPayload is true between a complete introducer and its terminator.
	inPayload bool
	// esc is true when the last payload byte was a bare ESC — the possible
	// first half of the two-byte ST terminator "\x1b\\".
	esc bool
	// payload accumulates the title bytes, bounded by titleMirrorPayloadCap.
	payload []byte
	// overflow marks a payload past the cap: keep scanning for the terminator
	// so the state machine re-synchronizes, but do not mirror.
	overflow bool
}

var osc2Prefix = [...]byte{0x1b, ']', '2', ';'}

func (t *titleMirror) Read(p []byte) (int, error) { return t.f.Read(p) }
func (t *titleMirror) Close() error               { return t.f.Close() }
func (t *titleMirror) Fd() uintptr                { return t.f.Fd() }

// Write scans p for complete OSC 2 sequences and writes p through with an
// OSC 1 twin injected immediately after each terminator. Injection points sit
// on sequence boundaries, so the terminal's parser sees two complete,
// well-formed sequences back to back.
func (t *titleMirror) Write(p []byte) (int, error) {
	// Fast path: no scanner state carried in and no ESC in this chunk means no
	// sequence starts, continues, or ends here — pass through untouched.
	if t.prefix == 0 && !t.inPayload && bytes.IndexByte(p, 0x1b) < 0 {
		return t.f.Write(p)
	}

	var out bytes.Buffer
	out.Grow(len(p) + 64)
	for _, b := range p {
		out.WriteByte(b)
		t.scan(b, &out)
	}

	n, err := t.f.Write(out.Bytes())
	if err != nil {
		// Report at most len(p) consumed: the injected bytes are not the
		// caller's. Precision is moot — a failed terminal write ends the TUI.
		return min(n, len(p)), err
	}
	return len(p), nil
}

// scan advances the state machine by one byte; on a completed OSC 2 sequence
// it appends the OSC 1 twin to out.
func (t *titleMirror) scan(b byte, out *bytes.Buffer) {
	if !t.inPayload {
		switch b {
		case osc2Prefix[t.prefix]:
			if t.prefix++; t.prefix == len(osc2Prefix) {
				t.startPayload()
			}
		case osc2Prefix[0]:
			// A mismatch that is itself ESC may begin a new introducer.
			t.prefix = 1
		default:
			t.prefix = 0
		}
		return
	}

	// In payload: look for BEL or the two-byte ST ("\x1b\\").
	switch {
	case b == 0x07:
		t.finishPayload(out)
	case t.esc && b == '\\':
		t.finishPayload(out)
	case t.esc:
		// A bare ESC not followed by '\' aborts the sequence (the payload
		// cannot legally contain ESC). The aborting ESC may itself start a
		// fresh introducer; the current byte b is re-examined against it.
		t.reset()
		if b == osc2Prefix[1] {
			t.prefix = 2
		}
	case b == 0x1b:
		t.esc = true
	default:
		if len(t.payload) < titleMirrorPayloadCap {
			t.payload = append(t.payload, b)
		} else {
			t.overflow = true
		}
	}
}

func (t *titleMirror) startPayload() {
	t.inPayload = true
	t.esc = false
	t.overflow = false
	t.payload = t.payload[:0]
}

func (t *titleMirror) finishPayload(out *bytes.Buffer) {
	if !t.overflow {
		// The empty payload is mirrored too: the renderer clears the window
		// title with SetWindowTitle("") on exit, and the icon name must not
		// outlive it as a stale tab label.
		out.WriteString(ansi.SetIconName(string(t.payload)))
	}
	t.reset()
}

func (t *titleMirror) reset() {
	t.inPayload = false
	t.esc = false
	t.overflow = false
	t.prefix = 0
	t.payload = t.payload[:0]
}
