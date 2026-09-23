# ADR 0026 — Clipboard Image Paste

- Status: Accepted
- Date: 2026
- Scope: ctrl+v clipboard read in mecatui, the shell-out strategy, and the marker-reconcile staging model

## Context

mecatui needed a way to paste images from the OS clipboard into the prompt so they could ride the existing media attachment send path. There is no portable, pure-Go clipboard read that works on Wayland; the common library requires cgo and X11 headers and does not support wl-clipboard at all. Adding a cgo dependency that still fails on the most common modern Linux desktop was not acceptable.

## Decision

The reader shells out to the platform's clipboard binary using direct argv — never via a shell — so there is no shell-injection surface. It tries an image first and falls back to text, returning the mime type so the caller can take the right branch. The reader lives in the client package behind a proto-free interface the ui consumes, keeping os/exec out of the domain. Staged images are keyed by a monotonic marker inserted into the textarea; submitPrompt reconciles by presence, strips markers, and builds the media part through the shared cap-gate and size-cap choke point. Later extensions — middle-click PRIMARY paste and large-paste placeholders — ride the same staging machinery.

## Consequences

Shipped. The macOS Chromium/Electron gap (pngpaste does not see the public.png UTI) is a known accepted limitation with no shell-only fix. Current behaviour is in docs/architecture.md. Status is in [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md). The injected-runner seam keeps all tests offline with no real subprocess.

---

`ctrl+v` in the mecatui prompt reads the OS clipboard and, when it holds an image,
stages it as an inline media attachment that rides the existing `Prompt.parts` send
path. This note records the non-obvious decisions.

## Why shell out (no cgo, no `golang.design/x/clipboard`)

There is no portable, pure-Go clipboard read that works on **Wayland**. The common
library (`golang.design/x/clipboard`) needs cgo + X11 headers and does not support
Wayland's `wl-clipboard` protocol at all; on a Wayland session it reads nothing.
Rather than add a cgo dependency that still fails on the most common modern Linux
desktop, the reader **shells out** to the platform's clipboard binary:

| Platform | image | text |
|---|---|---|
| Wayland | `wl-paste --list-types` → `wl-paste --no-newline --type image/png` | `wl-paste --no-newline` |
| X11 | `xclip -selection clipboard -t TARGETS -o` → `... -t image/png -o` | `xclip -selection clipboard -o` |
| macOS | `pngpaste -` | `pbpaste` |
| Windows | PowerShell `Clipboard.GetImage()` → PNG stream | `Get-Clipboard -Raw` |

argv is always **direct** — never `sh -c` — so there is no shell-injection surface
(the binary names are fixed by the capability probe; the args are constant). `os/exec`
is allowed in the `client` package (it already does `os`/`net/http`) but FORBIDDEN in
`ui`/domain/`port`/`agent`, so the reader lives in `cmd/mecatui/client/clipboard.go`
behind the proto-free `client.Clipboard` interface the `ui` consumes.

## Image-first, text-fallback

`Read` tries an image first (list the clipboard's types, fetch only if an `image/*`
type is present; macOS `pngpaste` fetches unconditionally and the output is sniffed),
and falls back to fetching clipboard **text** when there is no image. The returned
mime tells the caller which branch to take. This keeps `ctrl+v` useful as a plain
text paste even on a model that takes no images (image staging is cap-gated; text is
not). A genuinely empty clipboard is `ErrEmptyClipboard`; no backend binary at all is
`ErrNoClipboardTool` (which drives an actionable install hint).

## The injected-runner testability seam

`shellClipboard` takes its `run`/`lookPath`/`getenv`/`timeout` as struct fields.
`NewClipboard()` wires the real `exec.CommandContext(...).Output()`, `exec.LookPath`,
`os.Getenv`, and a 3s timeout; tests inject a recording runner that returns canned
image **and** text bytes keyed by argv, asserting the exact backend argv was invoked
(proving it is not a shell) entirely offline — no real clipboard, no subprocess.

## `[Image #N]` — reconcile at submit, never live-renumber

A staged image is keyed by a literal `[Image #N]` marker inserted into the textarea;
`N` is monotonic and **never reused**. The model does NOT renumber markers as the user
edits (deleting `#1` leaves `#2` as `#2`, a documented gap). Instead `submitPrompt`
**reconciles by presence**: it builds a part for every marker that still survives in
the sent text (`survivingMarkers`, ordered ascending by `N` → parts in display order),
strips the markers from the sent text, and clears the staged set. This makes the paste
handler O(1) and gives a natural "delete the marker to drop the attachment" gesture.
The proto part is (re)built only at submit via `client.StageClipboardImage`, which
shares the single `buildMediaPart` cap-gate + size-cap choke point with `@`-mentions,
so a cap that flipped or an oversize blob loud-rejects on the same path.

## macOS Chromium/Electron gap

`pngpaste` reads the `«class PNGf»` pasteboard flavour. Chromium/Electron apps copy
images as the `public.png` flavour, which `pngpaste` does not see — so an image copied
from Chrome may paste as text/empty rather than as an image. There is no shell-only fix
(it needs an `NSPasteboard` read of the `public.png` UTI); documented as a known gap.

## Since extended

Two later paste features ride the same staging machinery this note describes:

- **Middle-click PRIMARY paste** — `Clipboard.ReadPrimary` reads the X11/Wayland
  PRIMARY selection (the select-to-copy buffer) as text, and the ui pastes it on
  middle-click with an OSC52 fallback (`cmd/mecatui/client/clipboard.go`,
  `cmd/mecatui/ui/clipboard.go`). Same injected-runner seam, same direct-argv
  (never `sh -c`) discipline.
- **Large-paste `[Pasted text #N]` placeholders** — a huge pasted text stages
  behind a `[Pasted text #N]` marker in the textarea instead of flooding it,
  reconciled at submit exactly like `[Image #N]` (surviving markers expand,
  deleted markers drop the staged text).

Nothing above changes: `ctrl+v` image-first/text-fallback, the marker-reconcile
model, and the `buildMediaPart` choke point are as described.


---

*Part of the [design docs](../design/README.md). Related: [mecatui — UX discoverability design (Option C: wire capabilities)](0025-ux-discoverability.md).*
