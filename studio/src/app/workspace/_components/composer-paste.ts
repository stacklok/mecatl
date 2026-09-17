import {
  type Editor,
  mergeAttributes,
  Node as TiptapNode,
} from "@tiptap/react";

/**
 * Paste handling for the TipTap composer — the web half of the TUI's `ctrl+v`
 * (docs/tui.md "ctrl+v — paste a clipboard image" and "Large text pastes").
 *
 * Three outcomes, decided once per paste event by `pasteDecision`:
 *
 * - **attach** — the clipboard carries files and no text (a screenshot, an
 *   image copied from a page or an editor, a file copied in the OS): the files
 *   go through the composer's staging gate (`stageFiles`, the same path the
 *   "+" picker and drag-and-drop use, so the session's modality/size limits
 *   apply). Files that arrive WITH text are a rendition of that text (Excel,
 *   Numbers and Word all add a PNG of the copied cells or paragraphs), so the
 *   text wins and no image is attached behind the user's back.
 * - **stage** — a LARGE text paste (`classifyPaste`) does not enter the
 *   document literally. It becomes one atomic `[Pasted text #N]` chip
 *   (`PastePlaceholder`) whose `renderText` is the full payload, so
 *   `composerText` — the one serializer send, steer and queue read — expands
 *   it in place with no second send path. Backspace removes the whole chip
 *   (and with it that paste), exactly like a mention chip.
 * - **default** — everything else is left to ProseMirror's own paste, so a
 *   small paste stays byte-identical to the literal-insert behaviour.
 *
 * The TUI's pasted-media-PATH staging and its wl-paste/xclip/pbpaste tool
 * chain are terminal mechanics with no browser analogue: the paste event
 * hands the browser the image bytes directly.
 */

/** A paste of at least this many characters — alone, or together with the
 *  text already in the composer — is staged behind a placeholder. */
export const PASTE_CHAR_THRESHOLD = 2000;
/** A paste of at least this many lines is staged, whatever its length. */
export const PASTE_LINE_THRESHOLD = 30;

/** The placeholder node's schema name. */
const PASTE_PLACEHOLDER_NAME = "pastePlaceholder";

/** The chip's look: the mention chip's shape (composer-mentions.ts
 *  `CHIP_CLASS`) in a distinct muted tint, so a staged paste is never
 *  mistaken for an `@agent` chip. */
const PASTE_CHIP_CLASS =
  "rounded px-1 py-0.5 bg-sky-500/15 text-sky-800 dark:text-sky-300";

/** Longest first line shown in the chip's tooltip before it is cut. */
const TITLE_PREVIEW_CHARS = 80;

export type PasteClass = "inline" | "stage";

/** Number of lines in a paste: `\n`-separated, a trailing newline adds none. */
export function pasteLineCount(text: string): number {
  if (text === "") return 0;
  const body = text.endsWith("\n") ? text.slice(0, -1) : text;
  return body.split("\n").length;
}

/**
 * Whether a text paste is inserted literally or staged. Staging happens at
 * `PASTE_CHAR_THRESHOLD` characters — counting the text already in the
 * composer too, so repeated medium pastes cannot rebuild a huge draft (only
 * the incoming paste is ever staged; typed text never converts) — or at
 * `PASTE_LINE_THRESHOLD` lines regardless of length.
 */
export function classifyPaste({
  text,
  existingLength,
}: {
  text: string;
  existingLength: number;
}): PasteClass {
  if (text.length === 0) return "inline";
  if (existingLength + text.length >= PASTE_CHAR_THRESHOLD) return "stage";
  if (pasteLineCount(text) >= PASTE_LINE_THRESHOLD) return "stage";
  return "inline";
}

/** The visible chip label for the N-th staged paste of a draft. */
export function pasteMarker(n: number): string {
  return `[Pasted text #${n}]`;
}

/** The chip's tooltip: size, then the first line (cut when long). */
export function pasteTitle(text: string): string {
  const lines = pasteLineCount(text);
  const firstLine = text.split("\n", 1)[0] ?? "";
  const preview =
    firstLine.length > TITLE_PREVIEW_CHARS
      ? `${firstLine.slice(0, TITLE_PREVIEW_CHARS)}…`
      : firstLine;
  const size = `${text.length.toLocaleString("en-US")} characters, ${lines} ${lines === 1 ? "line" : "lines"}`;
  return preview ? `${size}: ${preview}` : size;
}

/** What one paste event carries, read once from its DataTransfer. */
export interface PasteClipboard {
  readonly files: readonly File[];
  /** The `text/plain` half, line endings normalised to `\n`. */
  readonly text: string;
}

/** The subset of DataTransfer the reader touches (duck-typed for tests). */
export interface PasteDataTransfer {
  readonly files?: ArrayLike<File> | null;
  readonly items?: ArrayLike<{
    readonly kind: string;
    getAsFile(): File | null;
  }> | null;
  getData(format: string): string;
}

/**
 * Read the paste's files and plain text. Files come from `files` and, where a
 * browser only reports them as items (Safari has), from the `kind: "file"`
 * items; text is the `text/plain` flavour with CRLF folded to `\n`.
 */
export function readPasteClipboard(
  data: PasteDataTransfer | null | undefined,
): PasteClipboard {
  if (!data) return { files: [], text: "" };
  let files: File[] = data.files ? Array.from(data.files) : [];
  if (files.length === 0 && data.items) {
    for (const item of Array.from(data.items)) {
      if (item.kind !== "file") continue;
      const file = item.getAsFile();
      if (file) files.push(file);
    }
  }
  files = files.filter((f): f is File => f != null);
  const text = data.getData("text/plain").replace(/\r\n?/g, "\n");
  return { files, text };
}

export type PasteDecision = "attach" | "stage" | "default";

/**
 * Decide what one paste becomes: files with no text attach; large text is
 * staged; anything else is left to the editor's default paste. See the module
 * doc for why text beats an accompanying image.
 */
export function pasteDecision(
  clip: PasteClipboard,
  existingLength: number,
): PasteDecision {
  const hasText = clip.text.trim().length > 0;
  if (clip.files.length > 0 && !hasText) return "attach";
  if (hasText && classifyPaste({ text: clip.text, existingLength }) === "stage")
    return "stage";
  return "default";
}

/**
 * The staged-paste chip. Inline, atomic (Backspace removes the whole chip,
 * the caret never enters it), selectable. `renderText` returns the payload,
 * so `editor.getText` — and `composerText` on it — expands every chip to the
 * pasted text in place; the DOM shows only the `[Pasted text #N]` marker and
 * carries the payload as `data-paste-text`, so an in-editor cut/paste of a
 * chip (or an undo) keeps its text.
 */
export const PastePlaceholder = TiptapNode.create({
  name: PASTE_PLACEHOLDER_NAME,
  group: "inline",
  inline: true,
  atom: true,
  selectable: true,
  draggable: false,

  addAttributes() {
    return {
      n: {
        default: 0,
        parseHTML: (element: HTMLElement) =>
          Number(element.getAttribute("data-paste-placeholder")) || 0,
        renderHTML: (attributes: Record<string, unknown>) => ({
          "data-paste-placeholder": String(attributes.n),
        }),
      },
      text: {
        default: "",
        parseHTML: (element: HTMLElement) =>
          element.getAttribute("data-paste-text") ?? "",
        renderHTML: (attributes: Record<string, unknown>) => ({
          "data-paste-text": String(attributes.text),
        }),
      },
    };
  },

  parseHTML() {
    return [{ tag: "span[data-paste-placeholder]" }];
  },

  renderHTML({ node, HTMLAttributes }) {
    const text = String(node.attrs.text);
    return [
      "span",
      mergeAttributes(HTMLAttributes, {
        class: PASTE_CHIP_CLASS,
        title: pasteTitle(text),
        contenteditable: "false",
      }),
      pasteMarker(Number(node.attrs.n)),
    ];
  },

  renderText({ node }) {
    return String(node.attrs.text);
  },
});

/**
 * The length of the composer as the USER sees it: chips count as their
 * `[Pasted text #N]` marker, not their payload. This is the "current input"
 * of the cumulative threshold — measured on the expanded text, every paste
 * after a staged one would be staged too, however small.
 */
export function visibleComposerLength(editor: Editor): number {
  let hidden = 0;
  editor.state.doc.descendants((node) => {
    if (node.type.name === PASTE_PLACEHOLDER_NAME) {
      hidden +=
        String(node.attrs.text).length -
        pasteMarker(Number(node.attrs.n)).length;
    }
    return true;
  });
  return editor.getText({ blockSeparator: "\n" }).length - hidden;
}

/**
 * Apply one paste to the composer and report what it became. `attach` hands
 * the files to the caller's staging gate; `stage` replaces the selection (or
 * inserts at the caret) with a chip numbered by `nextNumber`; `default` does
 * nothing, so the caller lets the editor's own paste run.
 */
export function applyComposerPaste(
  editor: Editor,
  clip: PasteClipboard,
  opts: {
    stageFiles: (files: File[]) => void;
    nextNumber: () => number;
  },
): PasteDecision {
  const decision = pasteDecision(clip, visibleComposerLength(editor));
  if (decision === "attach") {
    opts.stageFiles([...clip.files]);
  } else if (decision === "stage") {
    editor
      .chain()
      .insertContent({
        type: PASTE_PLACEHOLDER_NAME,
        attrs: { n: opts.nextNumber(), text: clip.text },
      })
      .run();
  }
  return decision;
}
