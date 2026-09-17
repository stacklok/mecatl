import { Editor } from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import { afterEach, describe, expect, it, vi } from "vitest";
import { composerText } from "./composer-mentions";
import {
  applyComposerPaste,
  classifyPaste,
  PASTE_CHAR_THRESHOLD,
  PASTE_LINE_THRESHOLD,
  type PasteDataTransfer,
  PastePlaceholder,
  pasteDecision,
  pasteLineCount,
  pasteMarker,
  pasteTitle,
  readPasteClipboard,
  visibleComposerLength,
} from "./composer-paste";

const png = (name = "shot.png") =>
  new File([new Uint8Array([137, 80, 78, 71])], name, { type: "image/png" });

const lines = (n: number) =>
  Array.from({ length: n }, (_, i) => `line ${i + 1}`).join("\n");

describe("classifyPaste", () => {
  it("stages at the character threshold, alone", () => {
    expect(
      classifyPaste({
        text: "x".repeat(PASTE_CHAR_THRESHOLD - 1),
        existingLength: 0,
      }),
    ).toBe("inline");
    expect(
      classifyPaste({
        text: "x".repeat(PASTE_CHAR_THRESHOLD),
        existingLength: 0,
      }),
    ).toBe("stage");
  });

  it("counts the text already in the composer (cumulative)", () => {
    expect(classifyPaste({ text: "x".repeat(1500), existingLength: 600 })).toBe(
      "stage",
    );
    expect(classifyPaste({ text: "x".repeat(1500), existingLength: 400 })).toBe(
      "inline",
    );
  });

  it("stages at the line threshold whatever the length", () => {
    expect(
      classifyPaste({
        text: lines(PASTE_LINE_THRESHOLD - 1),
        existingLength: 0,
      }),
    ).toBe("inline");
    expect(
      classifyPaste({ text: lines(PASTE_LINE_THRESHOLD), existingLength: 0 }),
    ).toBe("stage");
  });

  it("never stages an empty paste, even on a full composer", () => {
    expect(classifyPaste({ text: "", existingLength: 5000 })).toBe("inline");
  });
});

describe("pasteLineCount", () => {
  it("counts newline-separated lines; a trailing newline adds none", () => {
    expect(pasteLineCount("")).toBe(0);
    expect(pasteLineCount("one")).toBe(1);
    expect(pasteLineCount("one\ntwo")).toBe(2);
    expect(pasteLineCount("one\ntwo\n")).toBe(2);
  });
});

describe("pasteMarker / pasteTitle", () => {
  it("labels the N-th paste", () => {
    expect(pasteMarker(3)).toBe("[Pasted text #3]");
  });

  it("describes size and the first line, cutting a long one", () => {
    expect(pasteTitle("hello\nworld")).toBe("11 characters, 2 lines: hello");
    expect(pasteTitle("x")).toBe("1 characters, 1 line: x");
    const long = "a".repeat(100);
    expect(pasteTitle(long)).toBe(`100 characters, 1 line: ${"a".repeat(80)}…`);
    expect(pasteTitle("")).toBe("0 characters, 0 lines");
  });
});

describe("readPasteClipboard", () => {
  const dt = (over: Partial<PasteDataTransfer>): PasteDataTransfer => ({
    files: [],
    items: [],
    getData: () => "",
    ...over,
  });

  it("reads files and the text/plain flavour, folding CRLF", () => {
    const file = png();
    const clip = readPasteClipboard(
      dt({
        files: [file],
        getData: (f) => (f === "text/plain" ? "a\r\nb\rc" : ""),
      }),
    );
    expect(clip.files).toEqual([file]);
    expect(clip.text).toBe("a\nb\nc");
  });

  it("falls back to file items when `files` is empty", () => {
    const file = png("item.png");
    const clip = readPasteClipboard(
      dt({
        items: [
          { kind: "string", getAsFile: () => null },
          { kind: "file", getAsFile: () => file },
        ],
      }),
    );
    expect(clip.files).toEqual([file]);
  });

  it("is empty for a missing DataTransfer", () => {
    expect(readPasteClipboard(null)).toEqual({ files: [], text: "" });
  });
});

describe("pasteDecision", () => {
  it("attaches when files arrive with no text (a screenshot)", () => {
    expect(pasteDecision({ files: [png()], text: "" }, 0)).toBe("attach");
    expect(pasteDecision({ files: [png()], text: "  \n" }, 0)).toBe("attach");
  });

  it("prefers text over a rendition image (Excel, Word)", () => {
    expect(pasteDecision({ files: [png()], text: "A1\tB1" }, 0)).toBe(
      "default",
    );
    expect(
      pasteDecision(
        { files: [png()], text: "x".repeat(PASTE_CHAR_THRESHOLD) },
        0,
      ),
    ).toBe("stage");
  });

  it("stages a large text, defaults a small one or nothing", () => {
    expect(
      pasteDecision({ files: [], text: "x".repeat(PASTE_CHAR_THRESHOLD) }, 0),
    ).toBe("stage");
    expect(pasteDecision({ files: [], text: "x".repeat(1500) }, 600)).toBe(
      "stage",
    );
    expect(pasteDecision({ files: [], text: "hello" }, 0)).toBe("default");
    expect(pasteDecision({ files: [], text: "" }, 0)).toBe("default");
  });
});

/**
 * The chip inside a real TipTap editor (jsdom): the same StarterKit shape the
 * composer uses plus the placeholder node, so `composerText` expansion, the
 * DOM marker, deletion and the HTML round trip are proven against the actual
 * schema rather than a stub.
 */
describe("PastePlaceholder in an editor", () => {
  let editor: Editor | null = null;
  afterEach(() => {
    editor?.destroy();
    editor = null;
  });

  const make = () => {
    editor = new Editor({
      extensions: [
        StarterKit.configure({
          heading: false,
          bold: false,
          italic: false,
          strike: false,
          code: false,
          codeBlock: false,
          blockquote: false,
          bulletList: false,
          orderedList: false,
          listItem: false,
          horizontalRule: false,
          link: false,
          underline: false,
        }),
        PastePlaceholder,
      ],
      content: "",
    });
    return editor;
  };

  const counter = () => {
    let n = 0;
    return () => ++n;
  };

  it("stages a large paste as a chip whose composerText is the full payload", () => {
    const ed = make();
    const stageFiles = vi.fn();
    const payload = lines(PASTE_LINE_THRESHOLD);
    const outcome = applyComposerPaste(
      ed,
      { files: [], text: payload },
      { stageFiles, nextNumber: counter() },
    );
    expect(outcome).toBe("stage");
    expect(stageFiles).not.toHaveBeenCalled();
    // Send/steer/queue read this: the chip expands in place.
    expect(composerText(ed)).toBe(payload);
    // The DOM shows the marker, not the payload.
    const html = ed.getHTML();
    expect(html).toContain('data-paste-placeholder="1"');
    expect(html).toContain("[Pasted text #1]");
    expect(ed.view.dom.textContent).toBe("[Pasted text #1]");
    expect(
      ed.view.dom.querySelector("span[data-paste-placeholder]"),
    ).toHaveAttribute(
      "title",
      expect.stringContaining(`${PASTE_LINE_THRESHOLD} lines: line 1`),
    );
    // The user sees 16 characters, not the payload.
    expect(visibleComposerLength(ed)).toBe(pasteMarker(1).length);
  });

  it("numbers chips from the caller and expands them in document order", () => {
    const ed = make();
    const next = counter();
    const stageFiles = vi.fn();
    applyComposerPaste(
      ed,
      { files: [], text: "x".repeat(PASTE_CHAR_THRESHOLD) },
      { stageFiles, nextNumber: next },
    );
    ed.commands.insertContent(" and then ");
    applyComposerPaste(
      ed,
      { files: [], text: "y".repeat(PASTE_CHAR_THRESHOLD) },
      { stageFiles, nextNumber: next },
    );
    expect(ed.view.dom.textContent).toBe(
      "[Pasted text #1] and then [Pasted text #2]",
    );
    expect(composerText(ed)).toBe(
      `${"x".repeat(PASTE_CHAR_THRESHOLD)} and then ${"y".repeat(PASTE_CHAR_THRESHOLD)}`,
    );
  });

  it("measures the cumulative threshold on the visible text, not the payload", () => {
    const ed = make();
    const stageFiles = vi.fn();
    const next = counter();
    applyComposerPaste(
      ed,
      { files: [], text: "x".repeat(5000) },
      { stageFiles, nextNumber: next },
    );
    // A small follow-up paste is NOT staged just because a chip is present…
    expect(
      applyComposerPaste(
        ed,
        { files: [], text: "a url" },
        { stageFiles, nextNumber: next },
      ),
    ).toBe("default");
    // …but typed text does count towards the next paste.
    ed.commands.insertContent("t".repeat(600));
    expect(
      applyComposerPaste(
        ed,
        { files: [], text: "z".repeat(1500) },
        { stageFiles, nextNumber: next },
      ),
    ).toBe("stage");
    expect(ed.view.dom.textContent).toBe(
      `[Pasted text #1]${"t".repeat(600)}[Pasted text #2]`,
    );
  });

  it("replaces the active selection with the chip", () => {
    const ed = make();
    ed.commands.setContent("<p>keep DROP keep</p>");
    // Select "DROP" (positions are 1-based inside the paragraph).
    ed.commands.setTextSelection({ from: 6, to: 10 });
    applyComposerPaste(
      ed,
      { files: [], text: lines(PASTE_LINE_THRESHOLD) },
      { stageFiles: vi.fn(), nextNumber: counter() },
    );
    expect(ed.view.dom.textContent).toBe("keep [Pasted text #1] keep");
    expect(composerText(ed)).toBe(`keep ${lines(PASTE_LINE_THRESHOLD)} keep`);
  });

  it("drops the whole paste when the chip is deleted, and on clearContent", () => {
    const ed = make();
    applyComposerPaste(
      ed,
      { files: [], text: "x".repeat(PASTE_CHAR_THRESHOLD) },
      { stageFiles: vi.fn(), nextNumber: counter() },
    );
    // The chip is one atom: a single delete of its range removes the paste.
    ed.commands.deleteRange({ from: 1, to: 2 });
    expect(composerText(ed)).toBe("");
    applyComposerPaste(
      ed,
      { files: [], text: "x".repeat(PASTE_CHAR_THRESHOLD) },
      { stageFiles: vi.fn(), nextNumber: counter() },
    );
    ed.commands.clearContent();
    expect(composerText(ed)).toBe("");
    expect(ed.isEmpty).toBe(true);
  });

  it("round-trips through HTML so an in-editor cut/paste keeps the payload", () => {
    const ed = make();
    ed.commands.setContent(
      '<p>before <span data-paste-placeholder="4" data-paste-text="hello&#10;world">[Pasted text #4]</span></p>',
    );
    expect(composerText(ed)).toBe("before hello\nworld");
    expect(ed.view.dom.textContent).toBe("before [Pasted text #4]");
  });

  it("hands clipboard files to the staging gate and leaves the document alone", () => {
    const ed = make();
    ed.commands.setContent("<p>draft</p>");
    const stageFiles = vi.fn();
    const file = png();
    expect(
      applyComposerPaste(
        ed,
        { files: [file], text: "" },
        { stageFiles, nextNumber: counter() },
      ),
    ).toBe("attach");
    expect(stageFiles).toHaveBeenCalledWith([file]);
    expect(composerText(ed)).toBe("draft");
  });

  it("leaves a small text paste to the editor's default", () => {
    const ed = make();
    const stageFiles = vi.fn();
    expect(
      applyComposerPaste(
        ed,
        { files: [], text: "just a line" },
        { stageFiles, nextNumber: counter() },
      ),
    ).toBe("default");
    expect(stageFiles).not.toHaveBeenCalled();
    expect(ed.isEmpty).toBe(true);
  });
});
