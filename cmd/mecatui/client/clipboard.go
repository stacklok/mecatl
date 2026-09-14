package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Clipboard reads AND writes the OS clipboard. Read returns the clipboard's
// content as a (mime, data) pair: an image/* mime when the clipboard holds an
// image (which the UI stages as an inline media part), or a text mime (text/plain)
// when it holds text (which the UI inserts into the textarea). The mime tells the
// caller which branch to take. Write and WritePrimary are the BEST-EFFORT
// shell-clipboard fallbacks behind the in-app text-selection copy: the UI's copy
// path is OSC52 (tea.SetClipboard for the clipboard, tea.SetPrimaryClipboard for
// the X11/Wayland primary selection), and these mirror the same payload into the
// platform clipboard binaries so the copy still lands (incl. middle-click paste) on
// terminals that don't honour OSC52.
// A Write error is non-fatal — OSC52 is the primary and the copy is considered to
// have succeeded if either path works — so the UI must NOT surface it loudly. A
// nil Clipboard on Deps cleanly disables BOTH ctrl+v read and the shell write (the
// same convention as a nil MCP/Cmds collaborator); the OSC52 copy still runs. The
// UI imports client, so the interface + its sentinels live here, not in the ui
// package.
type Clipboard interface {
	Read(ctx context.Context) (mime string, data []byte, err error)
	// ReadPrimary reads the PRIMARY selection (the X11/Wayland select-to-copy
	// buffer, pasted by middle-click) as text. It returns ErrNoClipboardTool when
	// no backend binary exists OR the platform has no primary selection at all
	// (macOS/Windows) — the caller is expected to fall back to the OSC52 primary
	// read (tea.ReadPrimaryClipboard) in that case — and ErrEmptyClipboard when a
	// backend exists but the primary selection is empty (both wl-paste and xclip
	// exit non-zero on an empty selection, so a subprocess error maps here too).
	ReadPrimary(ctx context.Context) (string, error)
	// Write copies data (of the given mime, e.g. "text/plain") into the OS
	// clipboard via the platform binary. It is best-effort; a missing backend or a
	// failed subprocess returns an error the caller is expected to treat as a muted
	// non-event, never a transcript error.
	Write(ctx context.Context, mime string, data []byte) error
	// WritePrimary copies text into the X11/Wayland PRIMARY selection (the
	// select-to-copy buffer pasted by middle-click), so text selected INSIDE
	// mecatui behaves like a native terminal selection and can be middle-click
	// pasted elsewhere. It is the shell twin of the OSC52 primary write
	// (tea.SetPrimaryClipboard), best-effort with the same muted-error contract as
	// Write. Platforms with no primary selection (macOS/Windows) or no backend
	// return ErrNoClipboardTool.
	WritePrimary(ctx context.Context, data []byte) error
}

var (
	// ErrNoClipboardTool reports that NO backend clipboard binary exists (no
	// wl-paste / xclip / pbpaste / pngpaste / powershell). The UI surfaces an
	// actionable "install wl-clipboard / xclip" hint rather than a generic error.
	ErrNoClipboardTool = errors.New("no clipboard tool available")
	// ErrEmptyClipboard reports that a backend exists but the clipboard holds
	// neither a usable image nor any text. The UI shows a benign "clipboard is
	// empty" status (no transcript error).
	ErrEmptyClipboard = errors.New("clipboard is empty")
)

// clipboardTimeout bounds each clipboard subprocess so a wedged backend (e.g. a
// hung wl-paste) can never block the UI's command goroutine indefinitely.
const clipboardTimeout = 3 * time.Second

// binXclip is the X11 clipboard binary used for both the read (list/fetch) and the
// write (`-i`) paths; named once so the read/write argv share one spelling.
const binXclip = "xclip"

// binWlPaste is the Wayland clipboard read binary; named once so the probe and
// the four read argvs (types/image/text/primary) share one spelling.
const binWlPaste = "wl-paste"

// argSelection is xclip's selection-choosing flag (`-selection clipboard` /
// `-selection primary`), shared by every xclip argv.
const argSelection = "-selection"

// shellClipboard is the production Clipboard: it shells out to the platform's
// clipboard binary. os/exec is allowed in the client package (it already does
// os/net/http in media.go) but FORBIDDEN in the ui/domain/port layers — hence
// this lives here behind the proto-free Clipboard interface the ui consumes. The
// runner/lookPath/getenv funcs are INJECTED so tests drive it fully offline with
// canned image AND text bytes, never touching a real clipboard or subprocess.
type shellClipboard struct {
	run      func(ctx context.Context, name string, args ...string) ([]byte, error)
	lookPath func(string) (string, error)
	getenv   func(string) string
	timeout  time.Duration
	// runStdin feeds stdin to a backend (the copy WRITE path). Injected like run so
	// tests drive the shell write offline. Returns the subprocess error, if any.
	runStdin func(ctx context.Context, stdin []byte, name string, args ...string) error
}

// NewClipboard wires the real backend: exec.CommandContext(...).Output(), the real
// PATH lookup + environment, and the 3s timeout.
func NewClipboard() Clipboard {
	return &shellClipboard{
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output() //nolint:gosec // fixed backend binary names selected by capability probe; args are constant.
		},
		lookPath: exec.LookPath,
		getenv:   os.Getenv,
		timeout:  clipboardTimeout,
		runStdin: func(ctx context.Context, stdin []byte, name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed backend binary names selected by capability probe; args are constant.
			cmd.Stdin = bytes.NewReader(stdin)
			return cmd.Run()
		},
	}
}

// backend names the resolved clipboard backend and the exact argv to (a) list the
// clipboard's available types, (b) fetch a specific image type, and (c) fetch the
// clipboard text. A nil listImage/fetchImage means "this backend has no
// list-then-fetch image path" (macOS pngpaste fetches unconditionally); imageArgs
// is then the unconditional image fetch.
type backend struct {
	name string
	// listTypesArgs lists the MIME/target types currently on the clipboard. Empty
	// when the backend can't enumerate (macOS); the caller then tries imageArgs
	// directly and treats a non-image / error as "no image".
	listTypesArgs []string
	// imageArgs fetches the clipboard image bytes (PNG-preferred).
	imageArgs []string
	// textArgs fetches the clipboard text.
	textArgs []string
	// primaryTextArgs fetches the PRIMARY selection text (the X11/Wayland
	// middle-click buffer). Empty when the platform has no primary selection
	// (macOS/Windows); ReadPrimary then reports ErrNoClipboardTool so the UI
	// falls back to the OSC52 primary read.
	primaryTextArgs []string
	// writeTextArgs WRITES stdin to the clipboard (the copy fallback). Empty when
	// the backend has no shell write path; Write then no-ops with an error.
	writeTextArgs []string
	// writePrimaryTextArgs WRITES stdin to the PRIMARY selection (the middle-click
	// buffer). Empty on platforms with no primary selection (macOS/Windows) or when
	// the backend has no primary write path; WritePrimary then reports
	// ErrNoClipboardTool.
	writePrimaryTextArgs []string
}

// selectBackend probes the environment + PATH for a usable clipboard backend, in
// the documented precedence: Wayland (wl-paste) → X11 (xclip) → macOS
// (pngpaste/pbpaste) → Windows (powershell). It returns ErrNoClipboardTool when no
// backend binary exists.
func (c *shellClipboard) selectBackend() (backend, error) {
	has := func(bin string) bool {
		_, err := c.lookPath(bin)
		return err == nil
	}
	switch {
	case c.getenv("WAYLAND_DISPLAY") != "" && has(binWlPaste):
		return backend{
			name:                 binWlPaste,
			listTypesArgs:        []string{binWlPaste, "--list-types"},
			imageArgs:            []string{binWlPaste, "--no-newline", "--type", "image/png"},
			textArgs:             []string{binWlPaste, "--no-newline"},
			primaryTextArgs:      []string{binWlPaste, "--primary", "--no-newline"},
			writeTextArgs:        writeArgsFor(c.lookPath, "wl-copy"),
			writePrimaryTextArgs: primaryWriteArgsFor(c.lookPath, "wl-copy", "--primary"),
		}, nil
	case c.getenv("DISPLAY") != "" && has(binXclip):
		return backend{
			name:                 binXclip,
			listTypesArgs:        []string{binXclip, argSelection, "clipboard", "-t", "TARGETS", "-o"},
			imageArgs:            []string{binXclip, argSelection, "clipboard", "-t", "image/png", "-o"},
			textArgs:             []string{binXclip, argSelection, "clipboard", "-o"},
			primaryTextArgs:      []string{binXclip, argSelection, "primary", "-o"},
			writeTextArgs:        []string{binXclip, argSelection, "clipboard", "-i"},
			writePrimaryTextArgs: []string{binXclip, argSelection, "primary", "-i"},
		}, nil
	case has("pngpaste") || has("pbpaste"):
		// macOS. pngpaste fetches a clipboard image to stdout ("-"); it has no
		// type-listing, so listTypesArgs is empty and a non-image clipboard makes
		// pngpaste fail/empty (treated as "no image"). pbpaste fetches the text.
		// NOTE: pngpaste reads the «class PNGf» pasteboard flavour; Chromium/Electron
		// apps copy images as the "public.png" flavour, which pngpaste MISSES — so a
		// screenshot copied from Chrome may not paste as an image (it falls back to
		// text/empty). This is a known macOS gap with no shell-only fix.
		b := backend{name: "pbpaste", textArgs: []string{"pbpaste"}}
		if has("pngpaste") {
			b.imageArgs = []string{"pngpaste", "-"}
		}
		// pbcopy is the macOS clipboard WRITE binary (it always ships with pbpaste).
		b.writeTextArgs = writeArgsFor(c.lookPath, "pbcopy")
		return b, nil
	case has("powershell"):
		// Windows. Get-Clipboard yields text; GetImage() + the PNG stream yields the
		// image bytes (base64-free via Console.OpenStandardOutput). argv stays direct
		// (-Command with a constant script), never `sh -c`.
		return backend{
			name:      "powershell",
			imageArgs: []string{"powershell", "-NoProfile", "-Command", winImageScript},
			textArgs:  []string{"powershell", "-NoProfile", "-Command", "Get-Clipboard -Raw"},
			// clip.exe reads stdin and sets the clipboard; it ships on every Windows.
			writeTextArgs: writeArgsFor(c.lookPath, "clip"),
		}, nil
	default:
		return backend{}, ErrNoClipboardTool
	}
}

// writeArgsFor returns the single-binary write argv (a bare {bin}) when bin is on
// PATH, or nil when it is not — so a backend whose READ binary exists but whose
// WRITE binary (wl-copy / pbcopy / clip) does not simply has no shell write path
// and Write no-ops with an error (OSC52 still carries the copy).
func writeArgsFor(lookPath func(string) (string, error), bin string) []string {
	if _, err := lookPath(bin); err != nil {
		return nil
	}
	return []string{bin}
}

// primaryWriteArgsFor returns the write argv for the PRIMARY selection ({bin} plus
// the supplied primary-targeting args, e.g. wl-copy --primary) when bin is on PATH,
// or nil when it is not — the primary twin of writeArgsFor.
func primaryWriteArgsFor(lookPath func(string) (string, error), bin string, args ...string) []string {
	if _, err := lookPath(bin); err != nil {
		return nil
	}
	return append([]string{bin}, args...)
}

// Write implements Clipboard's best-effort shell-clipboard WRITE: it resolves the
// platform backend, pipes data to its write binary's stdin, and returns any
// subprocess/backend error. It is the FALLBACK behind the UI's OSC52 copy — the UI
// batches it alongside tea.SetClipboard and treats a returned error as a muted
// non-event, so a missing wl-copy/xclip/pbcopy/clip never surfaces loudly. A
// no-backend environment is ErrNoClipboardTool; a backend without a write path is a
// plain "no clipboard write backend" error. The payload is size-capped at
// maxMediaBytes (parity with the read path) BEFORE any subprocess is spawned, so a
// pathological selection can't pipe an unbounded blob into the clipboard binary.
func (c *shellClipboard) Write(ctx context.Context, _ string, data []byte) error {
	return c.writeSelection(ctx, data, func(b backend) []string { return b.writeTextArgs })
}

// WritePrimary implements Clipboard's best-effort PRIMARY-selection write (the
// shell twin of tea.SetPrimaryClipboard): it pipes data to the backend's primary
// write binary (wl-copy --primary / xclip -selection primary -i). Platforms with
// no primary selection (macOS/Windows) resolve to an empty argv and return
// ErrNoClipboardTool so the caller treats it as a muted non-event.
func (c *shellClipboard) WritePrimary(ctx context.Context, data []byte) error {
	return c.writeSelection(ctx, data, func(b backend) []string { return b.writePrimaryTextArgs })
}

// writeSelection is the shared body of Write/WritePrimary: resolve the backend,
// size-cap the payload, and pipe it to the selected argv's stdin under the
// clipboard timeout. argsFor picks the clipboard vs primary argv.
func (c *shellClipboard) writeSelection(ctx context.Context, data []byte, argsFor func(backend) []string) error {
	if len(data) > maxMediaBytes {
		return fmt.Errorf("clipboard write payload is %d bytes, over the %d-byte limit", len(data), maxMediaBytes)
	}
	b, err := c.selectBackend()
	if err != nil {
		return err
	}
	args := argsFor(b)
	if len(args) == 0 {
		return errors.New("no clipboard write backend")
	}
	if to := c.timeout; to > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, to)
		defer cancel()
	}
	return c.runStdin(ctx, data, args[0], args[1:]...)
}

// winImageScript fetches a clipboard image as raw PNG bytes on stdout. Kept as a
// constant so the argv stays direct (no `sh -c`); emits nothing when no image.
const winImageScript = `Add-Type -AssemblyName System.Windows.Forms; ` +
	`$img = [System.Windows.Forms.Clipboard]::GetImage(); ` +
	`if ($img -ne $null) { $ms = New-Object System.IO.MemoryStream; ` +
	`$img.Save($ms, [System.Drawing.Imaging.ImageFormat]::Png); ` +
	`$out = [System.Console]::OpenStandardOutput(); $ms.WriteTo($out); $out.Flush() }`

// Read implements Clipboard. It tries an image first (per the spec's image-first,
// text-fallback contract): if the clipboard holds an image type it fetches and
// returns it as image/png; otherwise it fetches the clipboard text and returns it
// as text/plain. A genuinely empty clipboard (no image, no text) is
// ErrEmptyClipboard; no backend at all is ErrNoClipboardTool.
func (c *shellClipboard) Read(ctx context.Context) (string, []byte, error) {
	b, err := c.selectBackend()
	if err != nil {
		return "", nil, err
	}
	if to := c.timeout; to > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, to)
		defer cancel()
	}

	if mime, data, ok, err := c.tryImage(ctx, b); err != nil {
		return "", nil, err
	} else if ok {
		return mime, data, nil
	}

	// No image on the clipboard: fall back to text.
	if len(b.textArgs) > 0 {
		text, err := c.run(ctx, b.textArgs[0], b.textArgs[1:]...)
		if err == nil && len(text) > 0 {
			return "text/plain", text, nil
		}
	}
	return "", nil, ErrEmptyClipboard
}

// ReadPrimary implements Clipboard's PRIMARY-selection read (the middle-click
// paste buffer). Text-only by design: the primary selection is a select-to-copy
// TEXT buffer, so there is no image-first branch. A platform whose backend has no
// primary selection (macOS/Windows) — or no backend at all — is ErrNoClipboardTool,
// which the UI treats as "fall back to the OSC52 primary read". An empty selection
// (both wl-paste and xclip exit non-zero / emit nothing on one) is
// ErrEmptyClipboard. No size cap, matching the Read text-fallback path (a huge
// selection is the UI's business — it stages behind a [Pasted text #N]
// placeholder); the subprocess stays bounded by the same timeout as Read.
func (c *shellClipboard) ReadPrimary(ctx context.Context) (string, error) {
	b, err := c.selectBackend()
	if err != nil {
		return "", err
	}
	if len(b.primaryTextArgs) == 0 {
		return "", ErrNoClipboardTool // platform has no primary selection → OSC52 fallback
	}
	if to := c.timeout; to > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, to)
		defer cancel()
	}
	out, err := c.run(ctx, b.primaryTextArgs[0], b.primaryTextArgs[1:]...)
	if err != nil || len(out) == 0 {
		return "", ErrEmptyClipboard
	}
	return string(out), nil
}

// tryImage attempts to fetch a clipboard image. ok reports whether an image was
// returned. A backend that can enumerate types (Wayland/X11) is asked first and
// only fetched when an image/* type is present; a backend without enumeration
// (macOS pngpaste) is fetched unconditionally and its output sniffed. An oversize
// image is a hard error (the user pasted something too big — loud, not silent).
func (c *shellClipboard) tryImage(ctx context.Context, b backend) (string, []byte, bool, error) {
	if len(b.imageArgs) == 0 {
		return "", nil, false, nil // backend has no image path (e.g. pbpaste-only macOS)
	}
	if len(b.listTypesArgs) > 0 {
		types, err := c.run(ctx, b.listTypesArgs[0], b.listTypesArgs[1:]...)
		if err != nil || !hasImageType(types) {
			return "", nil, false, nil // can't list, or no image type → fall back to text
		}
	}
	data, err := c.run(ctx, b.imageArgs[0], b.imageArgs[1:]...)
	if err != nil || len(data) == 0 {
		return "", nil, false, nil // fetch failed / empty → treat as no image
	}
	// Defensive re-sniff: keep only genuine image bytes (a backend that emitted
	// non-image bytes when we expected an image falls through to text).
	mime := http.DetectContentType(data[:min(sniffLen, len(data))])
	if !strings.HasPrefix(mime, "image/") {
		return "", nil, false, nil
	}
	// Trade-off: exec.Output() buffers ALL of the backend's stdout before this cap
	// check, so a pathological clipboard image is fully read into RAM before being
	// rejected here. Accepted for a local, single-user TUI: the read is bounded by
	// the 3s context timeout AND this size cap, and the injected-runner seam (which
	// the tests depend on) is intentionally NOT restructured to stream+truncate.
	if len(data) > maxMediaBytes {
		return "", nil, false, fmt.Errorf("clipboard image is %d bytes, over the %d-byte limit", len(data), maxMediaBytes)
	}
	// Normalise to image/png defensively (the fetch requested PNG); the server
	// re-validates the kind regardless.
	return "image/png", data, true, nil
}

// hasImageType reports whether a wl-paste/xclip type listing (one type per line)
// advertises any image/* type.
func hasImageType(listing []byte) bool {
	for _, line := range strings.Split(string(listing), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "image/") {
			return true
		}
	}
	return bytes.Contains(listing, []byte("image/")) // defensive: space-separated TARGETS
}
