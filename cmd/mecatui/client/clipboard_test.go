package client

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
)

// recordingRunner is an injected clipboard runner that returns canned bytes keyed
// by the argv it is invoked with, recording every argv so a test can assert which
// backend command actually ran (NOT a shell). A key is the space-joined argv; a
// missing key returns errMissing (modelling a command that produced nothing).
type recordingRunner struct {
	out  map[string][]byte
	errs map[string]error
	got  [][]string
}

var errMissing = errors.New("no canned output")

func (r *recordingRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	argv := append([]string{name}, args...)
	r.got = append(r.got, argv)
	key := join(argv)
	if err, ok := r.errs[key]; ok {
		return nil, err
	}
	if out, ok := r.out[key]; ok {
		return out, nil
	}
	return nil, errMissing
}

func join(argv []string) string {
	var b bytes.Buffer
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(a)
	}
	return b.String()
}

func (r *recordingRunner) ran(argv ...string) bool {
	for _, g := range r.got {
		if reflect.DeepEqual(g, argv) {
			return true
		}
	}
	return false
}

// clipWith builds a shellClipboard with an injected runner and a lookPath/getenv
// pair selecting a backend. have lists the binaries on PATH; env is the
// environment.
func clipWith(runner *recordingRunner, have map[string]bool, env map[string]string) *shellClipboard {
	return &shellClipboard{
		run: runner.run,
		lookPath: func(bin string) (string, error) {
			if have[bin] {
				return "/usr/bin/" + bin, nil
			}
			return "", exec.ErrNotFound
		},
		getenv:  func(k string) string { return env[k] },
		timeout: 0, // no timeout in tests (the injected runner never blocks)
	}
}

// TestClipboardWaylandImage: a Wayland clipboard advertising an image type fetches
// the PNG via the EXACT wl-paste argv (not a shell) and returns image/png + bytes.
func TestClipboardWaylandImage(t *testing.T) {
	png := readFixturePNG(t)
	r := &recordingRunner{out: map[string][]byte{
		"wl-paste --list-types":                  []byte("text/plain\nimage/png\n"),
		"wl-paste --no-newline --type image/png": png,
	}}
	cb := clipWith(r, map[string]bool{"wl-paste": true}, map[string]string{"WAYLAND_DISPLAY": "wayland-0"})

	mime, data, err := cb.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if mime != "image/png" {
		t.Errorf("mime = %q, want image/png", mime)
	}
	if !bytes.Equal(data, png) {
		t.Errorf("data mismatch: got %d bytes, want %d", len(data), len(png))
	}
	if !r.ran("wl-paste", "--no-newline", "--type", "image/png") {
		t.Errorf("did not fetch image via the wl-paste argv; calls=%v", r.got)
	}
}

// TestClipboardX11Image: an X11 clipboard whose TARGETS list an image type fetches
// the PNG via the EXACT xclip argv and returns image/png + bytes.
func TestClipboardX11Image(t *testing.T) {
	png := readFixturePNG(t)
	r := &recordingRunner{out: map[string][]byte{
		"xclip -selection clipboard -t TARGETS -o":   []byte("TARGETS\nimage/png\nUTF8_STRING\n"),
		"xclip -selection clipboard -t image/png -o": png,
	}}
	cb := clipWith(r, map[string]bool{"xclip": true}, map[string]string{"DISPLAY": ":0"})

	mime, data, err := cb.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if mime != "image/png" || !bytes.Equal(data, png) {
		t.Errorf("mime=%q data=%d bytes, want image/png + %d bytes", mime, len(data), len(png))
	}
	if !r.ran("xclip", "-selection", "clipboard", "-t", "image/png", "-o") {
		t.Errorf("did not fetch image via the xclip argv; calls=%v", r.got)
	}
}

// TestClipboardMacImage: macOS pngpaste (no type-listing) fetches the image
// unconditionally via `pngpaste -` and the output sniffs as an image.
func TestClipboardMacImage(t *testing.T) {
	png := readFixturePNG(t)
	r := &recordingRunner{out: map[string][]byte{"pngpaste -": png}}
	cb := clipWith(r, map[string]bool{"pngpaste": true, "pbpaste": true}, nil)

	mime, data, err := cb.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if mime != "image/png" || !bytes.Equal(data, png) {
		t.Errorf("mime=%q data=%d, want image/png + %d", mime, len(data), len(png))
	}
	if !r.ran("pngpaste", "-") {
		t.Errorf("did not run pngpaste -; calls=%v", r.got)
	}
}

// TestClipboardTextFallback: no image on the clipboard (the type list has no
// image type) → the text path IS taken and ("text/plain", bytes) is returned. This
// is the INVERSE of the old "never shells out for text" guard: per the spec's
// image-first, text-fallback contract, text MUST be fetched when no image exists.
func TestClipboardTextFallback(t *testing.T) {
	r := &recordingRunner{out: map[string][]byte{
		"wl-paste --list-types": []byte("text/plain;charset=utf-8\nUTF8_STRING\n"),
		"wl-paste --no-newline": []byte("hello clipboard text"),
	}}
	cb := clipWith(r, map[string]bool{"wl-paste": true}, map[string]string{"WAYLAND_DISPLAY": "wayland-0"})

	mime, data, err := cb.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if mime != "text/plain" {
		t.Errorf("mime = %q, want text/plain", mime)
	}
	if string(data) != "hello clipboard text" {
		t.Errorf("data = %q, want the clipboard text", data)
	}
	// The text fetch argv WAS invoked (the inverted guard).
	if !r.ran("wl-paste", "--no-newline") {
		t.Errorf("text path not taken; calls=%v", r.got)
	}
	// The image fetch was NOT invoked (no image type advertised).
	if r.ran("wl-paste", "--no-newline", "--type", "image/png") {
		t.Errorf("fetched an image when none was advertised; calls=%v", r.got)
	}
}

// TestClipboardMacTextFallback: macOS with pngpaste returning nothing (empty
// pasteboard image, e.g. a text copy) falls back to pbpaste text.
func TestClipboardMacTextFallback(t *testing.T) {
	r := &recordingRunner{
		errs: map[string]error{"pngpaste -": errMissing}, // no image on the pasteboard
		out:  map[string][]byte{"pbpaste": []byte("just some text")},
	}
	cb := clipWith(r, map[string]bool{"pngpaste": true, "pbpaste": true}, nil)

	mime, data, err := cb.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if mime != "text/plain" || string(data) != "just some text" {
		t.Errorf("mime=%q data=%q, want text/plain + the text", mime, data)
	}
	if !r.ran("pbpaste") {
		t.Errorf("pbpaste text path not taken; calls=%v", r.got)
	}
}

// TestClipboardNoTool: no backend binary on PATH → ErrNoClipboardTool and NO run
// calls (the probe must short-circuit before shelling out).
func TestClipboardNoTool(t *testing.T) {
	r := &recordingRunner{}
	cb := clipWith(r, map[string]bool{}, nil)

	_, _, err := cb.Read(context.Background())
	if !errors.Is(err, ErrNoClipboardTool) {
		t.Fatalf("err = %v, want ErrNoClipboardTool", err)
	}
	if len(r.got) != 0 {
		t.Errorf("ran %v, want no subprocess calls when no backend exists", r.got)
	}
}

// TestClipboardEmpty: a backend exists but the clipboard holds neither image nor
// text → ErrEmptyClipboard (a benign empty, not a tool-missing error).
func TestClipboardEmpty(t *testing.T) {
	r := &recordingRunner{out: map[string][]byte{
		"wl-paste --list-types": []byte("text/plain\n"),
		"wl-paste --no-newline": {}, // empty text
	}}
	cb := clipWith(r, map[string]bool{"wl-paste": true}, map[string]string{"WAYLAND_DISPLAY": "wayland-0"})

	_, _, err := cb.Read(context.Background())
	if !errors.Is(err, ErrEmptyClipboard) {
		t.Fatalf("err = %v, want ErrEmptyClipboard", err)
	}
}

// TestClipboardOversizeImage: a clipboard image over the per-file cap is a hard
// error (loud, never silently dropped), not a text fallback.
func TestClipboardOversizeImage(t *testing.T) {
	big := make([]byte, maxMediaBytes+1)
	copy(big, readFixturePNG(t)) // PNG signature → sniffs as image/png
	r := &recordingRunner{out: map[string][]byte{
		"wl-paste --list-types":                  []byte("image/png\n"),
		"wl-paste --no-newline --type image/png": big,
	}}
	cb := clipWith(r, map[string]bool{"wl-paste": true}, map[string]string{"WAYLAND_DISPLAY": "wayland-0"})

	_, _, err := cb.Read(context.Background())
	if err == nil {
		t.Fatal("want oversize error")
	}
	if errors.Is(err, ErrEmptyClipboard) || errors.Is(err, ErrNoClipboardTool) {
		t.Errorf("err = %v, want a distinct oversize error (not empty/no-tool)", err)
	}
}

// TestClipboardBackendPrecedence: Wayland wins over X11 when both env+binaries are
// present (the documented precedence). Asserted via which list-types argv ran.
func TestClipboardBackendPrecedence(t *testing.T) {
	r := &recordingRunner{out: map[string][]byte{
		"wl-paste --list-types": []byte("text/plain\n"),
		"wl-paste --no-newline": []byte("wayland text"),
	}}
	cb := clipWith(r,
		map[string]bool{"wl-paste": true, "xclip": true},
		map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"},
	)
	if _, _, err := cb.Read(context.Background()); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !r.ran("wl-paste", "--list-types") {
		t.Errorf("Wayland backend not selected first; calls=%v", r.got)
	}
	if r.ran("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o") {
		t.Errorf("X11 was used despite Wayland being available; calls=%v", r.got)
	}
}

// TestClipboardWindowsImage: with no WAYLAND/DISPLAY env and powershell on PATH,
// the Windows backend fetches the image via the EXACT powershell argv (the
// winImageScript -Command), and the canned PNG is returned as image/png.
func TestClipboardWindowsImage(t *testing.T) {
	png := readFixturePNG(t)
	r := &recordingRunner{out: map[string][]byte{
		join([]string{"powershell", "-NoProfile", "-Command", winImageScript}): png,
	}}
	cb := clipWith(r, map[string]bool{"powershell": true}, nil) // no WAYLAND/DISPLAY

	mime, data, err := cb.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if mime != "image/png" || !bytes.Equal(data, png) {
		t.Errorf("mime=%q data=%d, want image/png + %d", mime, len(data), len(png))
	}
	if !r.ran("powershell", "-NoProfile", "-Command", winImageScript) {
		t.Errorf("did not fetch image via the powershell argv; calls=%v", r.got)
	}
}

// TestClipboardWindowsTextFallback: powershell with no clipboard image (the image
// script yields nothing) falls back to Get-Clipboard text.
func TestClipboardWindowsTextFallback(t *testing.T) {
	r := &recordingRunner{out: map[string][]byte{
		join([]string{"powershell", "-NoProfile", "-Command", "Get-Clipboard -Raw"}): []byte("windows clipboard text"),
	}}
	cb := clipWith(r, map[string]bool{"powershell": true}, nil)

	mime, data, err := cb.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if mime != "text/plain" || string(data) != "windows clipboard text" {
		t.Errorf("mime=%q data=%q, want text/plain + the text", mime, data)
	}
	if !r.ran("powershell", "-NoProfile", "-Command", "Get-Clipboard -Raw") {
		t.Errorf("text path not taken; calls=%v", r.got)
	}
}

// TestHasImageTypeSpaceSeparated exercises the space-separated TARGETS fallback in
// hasImageType (some xclip builds return a space-joined TARGETS line rather than
// one-per-line), so the bytes.Contains(listing, "image/") branch is covered.
func TestHasImageTypeSpaceSeparated(t *testing.T) {
	if !hasImageType([]byte("TARGETS UTF8_STRING image/png text/plain")) {
		t.Error("space-separated TARGETS with image/png should report an image type")
	}
	if hasImageType([]byte("TARGETS UTF8_STRING text/plain")) {
		t.Error("no image type present should report false")
	}
	if !hasImageType([]byte("text/plain\nimage/jpeg\n")) {
		t.Error("newline-separated listing with image/jpeg should report an image type")
	}
}

// stdinRecorder is an injected runStdin that records the (argv, stdin) it was
// invoked with so a Write test can assert the EXACT backend command + payload (not
// a shell), and returns a canned error keyed by the space-joined argv.
type stdinRecorder struct {
	got   [][]string
	stdin [][]byte
	errs  map[string]error
}

func (r *stdinRecorder) run(_ context.Context, stdin []byte, name string, args ...string) error {
	argv := append([]string{name}, args...)
	r.got = append(r.got, argv)
	r.stdin = append(r.stdin, append([]byte(nil), stdin...))
	return r.errs[join(argv)]
}

// clipWriteWith builds a shellClipboard wired for the WRITE path: a read runner
// (unused by Write but required by selectBackend's struct), an injected runStdin
// recorder, and the lookPath/getenv pair selecting a backend.
func clipWriteWith(stdin *stdinRecorder, have map[string]bool, env map[string]string) *shellClipboard {
	cb := clipWith(&recordingRunner{}, have, env)
	cb.runStdin = stdin.run
	return cb
}

// TestClipboardWriteWayland: a Wayland environment with wl-copy on PATH writes the
// payload to the EXACT wl-copy argv (stdin), not a shell.
func TestClipboardWriteWayland(t *testing.T) {
	rec := &stdinRecorder{}
	cb := clipWriteWith(rec, map[string]bool{"wl-paste": true, "wl-copy": true}, map[string]string{"WAYLAND_DISPLAY": "wayland-0"})

	if err := cb.Write(context.Background(), "text/plain", []byte("hello copy")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(rec.got) != 1 || !reflect.DeepEqual(rec.got[0], []string{"wl-copy"}) {
		t.Fatalf("write argv = %v, want a single [wl-copy]", rec.got)
	}
	if !bytes.Equal(rec.stdin[0], []byte("hello copy")) {
		t.Errorf("stdin = %q, want the payload", rec.stdin[0])
	}
}

// TestClipboardWriteX11: an X11 environment writes via `xclip -selection clipboard
// -i` with the payload on stdin.
func TestClipboardWriteX11(t *testing.T) {
	rec := &stdinRecorder{}
	cb := clipWriteWith(rec, map[string]bool{"xclip": true}, map[string]string{"DISPLAY": ":0"})

	if err := cb.Write(context.Background(), "text/plain", []byte("x11 payload")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(rec.got) != 1 || !reflect.DeepEqual(rec.got[0], []string{"xclip", "-selection", "clipboard", "-i"}) {
		t.Fatalf("write argv = %v, want xclip -selection clipboard -i", rec.got)
	}
	if !bytes.Equal(rec.stdin[0], []byte("x11 payload")) {
		t.Errorf("stdin = %q, want the payload", rec.stdin[0])
	}
}

// TestClipboardWriteNoBackend: with no clipboard binary at all, Write returns
// ErrNoClipboardTool (the caller swallows it — OSC52 still carried the copy).
func TestClipboardWriteNoBackend(t *testing.T) {
	rec := &stdinRecorder{}
	cb := clipWriteWith(rec, map[string]bool{}, map[string]string{})

	if err := cb.Write(context.Background(), "text/plain", []byte("x")); !errors.Is(err, ErrNoClipboardTool) {
		t.Errorf("Write err = %v, want ErrNoClipboardTool", err)
	}
	if len(rec.got) != 0 {
		t.Errorf("no backend should run nothing, ran %v", rec.got)
	}
}

// TestClipboardWriteReadBinaryButNoWriteBinary: a Wayland environment where
// wl-paste exists but wl-copy does NOT yields a "no write backend" error (and runs
// nothing) — the READ path is still usable, the WRITE path simply has no binary.
func TestClipboardWriteReadBinaryButNoWriteBinary(t *testing.T) {
	rec := &stdinRecorder{}
	cb := clipWriteWith(rec, map[string]bool{"wl-paste": true}, map[string]string{"WAYLAND_DISPLAY": "wayland-0"})

	if err := cb.Write(context.Background(), "text/plain", []byte("x")); err == nil {
		t.Error("Write with no wl-copy should error")
	}
	if len(rec.got) != 0 {
		t.Errorf("missing write binary should run nothing, ran %v", rec.got)
	}
}

// TestClipboardWriteBackendError: a backend error from the subprocess is returned
// verbatim (the UI treats it as a muted non-event).
func TestClipboardWriteBackendError(t *testing.T) {
	boom := errors.New("subprocess failed")
	rec := &stdinRecorder{errs: map[string]error{"pbcopy": boom}}
	cb := clipWriteWith(rec, map[string]bool{"pbpaste": true, "pbcopy": true}, map[string]string{})

	if err := cb.Write(context.Background(), "text/plain", []byte("mac payload")); !errors.Is(err, boom) {
		t.Errorf("Write err = %v, want the subprocess error", err)
	}
}

// TestClipboardWriteOverCap: a payload over maxMediaBytes returns an error and does
// NOT spawn the subprocess (parity with the read-path size cap).
func TestClipboardWriteOverCap(t *testing.T) {
	rec := &stdinRecorder{}
	cb := clipWriteWith(rec, map[string]bool{"xclip": true}, map[string]string{"DISPLAY": ":0"})

	big := make([]byte, maxMediaBytes+1)
	if err := cb.Write(context.Background(), "text/plain", big); err == nil {
		t.Error("an over-cap Write should error")
	}
	if len(rec.got) != 0 {
		t.Errorf("an over-cap Write must not spawn the subprocess, ran %v", rec.got)
	}
}

// TestClipboardWritePrimaryWayland: a Wayland environment with wl-copy on PATH
// writes the payload to the PRIMARY selection via `wl-copy --primary` (stdin), so a
// mecatui selection is middle-click pasteable.
func TestClipboardWritePrimaryWayland(t *testing.T) {
	rec := &stdinRecorder{}
	cb := clipWriteWith(rec, map[string]bool{"wl-paste": true, "wl-copy": true}, map[string]string{"WAYLAND_DISPLAY": "wayland-0"})

	if err := cb.WritePrimary(context.Background(), []byte("primary copy")); err != nil {
		t.Fatalf("WritePrimary: %v", err)
	}
	if len(rec.got) != 1 || !reflect.DeepEqual(rec.got[0], []string{"wl-copy", "--primary"}) {
		t.Fatalf("primary write argv = %v, want [wl-copy --primary]", rec.got)
	}
	if !bytes.Equal(rec.stdin[0], []byte("primary copy")) {
		t.Errorf("stdin = %q, want the payload", rec.stdin[0])
	}
}

// TestClipboardWritePrimaryX11: an X11 environment writes the PRIMARY selection via
// `xclip -selection primary -i` with the payload on stdin.
func TestClipboardWritePrimaryX11(t *testing.T) {
	rec := &stdinRecorder{}
	cb := clipWriteWith(rec, map[string]bool{"xclip": true}, map[string]string{"DISPLAY": ":0"})

	if err := cb.WritePrimary(context.Background(), []byte("x11 primary")); err != nil {
		t.Fatalf("WritePrimary: %v", err)
	}
	if len(rec.got) != 1 || !reflect.DeepEqual(rec.got[0], []string{"xclip", "-selection", "primary", "-i"}) {
		t.Fatalf("primary write argv = %v, want xclip -selection primary -i", rec.got)
	}
	if !bytes.Equal(rec.stdin[0], []byte("x11 primary")) {
		t.Errorf("stdin = %q, want the payload", rec.stdin[0])
	}
}

// TestClipboardWritePrimaryMacNoPrimarySelection: macOS has no primary selection,
// so WritePrimary resolves to an empty argv and runs nothing (a no-op error the UI
// swallows — the OSC52 primary write still ran).
func TestClipboardWritePrimaryMacNoPrimarySelection(t *testing.T) {
	rec := &stdinRecorder{}
	cb := clipWriteWith(rec, map[string]bool{"pbpaste": true, "pbcopy": true}, map[string]string{})

	if err := cb.WritePrimary(context.Background(), []byte("x")); err == nil {
		t.Error("WritePrimary on macOS (no primary selection) should error")
	}
	if len(rec.got) != 0 {
		t.Errorf("no primary write path should run nothing, ran %v", rec.got)
	}
}

// TestClipboardWritePrimaryNoBackend: with no clipboard binary at all, WritePrimary
// returns ErrNoClipboardTool (the caller swallows it — OSC52 carried the copy).
func TestClipboardWritePrimaryNoBackend(t *testing.T) {
	rec := &stdinRecorder{}
	cb := clipWriteWith(rec, map[string]bool{}, map[string]string{})

	if err := cb.WritePrimary(context.Background(), []byte("x")); !errors.Is(err, ErrNoClipboardTool) {
		t.Errorf("WritePrimary err = %v, want ErrNoClipboardTool", err)
	}
	if len(rec.got) != 0 {
		t.Errorf("no backend should run nothing, ran %v", rec.got)
	}
}

// TestClipboardPrimaryWayland: a Wayland environment reads the primary selection
// via the EXACT `wl-paste --primary --no-newline` argv (not a shell).
func TestClipboardPrimaryWayland(t *testing.T) {
	r := &recordingRunner{out: map[string][]byte{
		"wl-paste --primary --no-newline": []byte("primary sel"),
	}}
	cb := clipWith(r, map[string]bool{"wl-paste": true}, map[string]string{"WAYLAND_DISPLAY": "wayland-0"})

	text, err := cb.ReadPrimary(context.Background())
	if err != nil {
		t.Fatalf("ReadPrimary: %v", err)
	}
	if text != "primary sel" {
		t.Errorf("text = %q, want %q", text, "primary sel")
	}
	if !r.ran("wl-paste", "--primary", "--no-newline") {
		t.Errorf("expected the wl-paste primary argv, ran %v", r.got)
	}
}

// TestClipboardPrimaryX11: an X11 environment reads the primary selection via the
// EXACT `xclip -selection primary -o` argv.
func TestClipboardPrimaryX11(t *testing.T) {
	r := &recordingRunner{out: map[string][]byte{
		"xclip -selection primary -o": []byte("x11 primary"),
	}}
	cb := clipWith(r, map[string]bool{"xclip": true}, map[string]string{"DISPLAY": ":0"})

	text, err := cb.ReadPrimary(context.Background())
	if err != nil {
		t.Fatalf("ReadPrimary: %v", err)
	}
	if text != "x11 primary" {
		t.Errorf("text = %q, want %q", text, "x11 primary")
	}
	if !r.ran("xclip", "-selection", "primary", "-o") {
		t.Errorf("expected the xclip primary argv, ran %v", r.got)
	}
}

// TestClipboardPrimaryMacNoPrimarySelection: macOS has no primary selection —
// ReadPrimary reports ErrNoClipboardTool (the UI's cue to fall back to the OSC52
// primary read) and spawns NO subprocess.
func TestClipboardPrimaryMacNoPrimarySelection(t *testing.T) {
	r := &recordingRunner{}
	cb := clipWith(r, map[string]bool{"pbpaste": true}, nil)

	if _, err := cb.ReadPrimary(context.Background()); !errors.Is(err, ErrNoClipboardTool) {
		t.Errorf("ReadPrimary err = %v, want ErrNoClipboardTool", err)
	}
	if len(r.got) != 0 {
		t.Errorf("no subprocess should run on a primary-less platform, ran %v", r.got)
	}
}

// TestClipboardPrimaryNoTool: no backend binary at all → ErrNoClipboardTool.
func TestClipboardPrimaryNoTool(t *testing.T) {
	r := &recordingRunner{}
	cb := clipWith(r, nil, nil)

	if _, err := cb.ReadPrimary(context.Background()); !errors.Is(err, ErrNoClipboardTool) {
		t.Errorf("ReadPrimary err = %v, want ErrNoClipboardTool", err)
	}
}

// TestClipboardPrimaryEmpty: a backend exists but the primary selection is empty
// (wl-paste/xclip exit non-zero on an empty selection — the runner has no canned
// output for the argv) → ErrEmptyClipboard.
func TestClipboardPrimaryEmpty(t *testing.T) {
	r := &recordingRunner{}
	cb := clipWith(r, map[string]bool{"wl-paste": true}, map[string]string{"WAYLAND_DISPLAY": "wayland-0"})

	if _, err := cb.ReadPrimary(context.Background()); !errors.Is(err, ErrEmptyClipboard) {
		t.Errorf("ReadPrimary err = %v, want ErrEmptyClipboard", err)
	}
}
