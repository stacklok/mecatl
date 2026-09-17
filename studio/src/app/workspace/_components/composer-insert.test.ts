import { Editor } from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import { afterEach, describe, expect, it } from "vitest";
import { appendComposerText, composerParagraphs } from "./composer-insert";
import { composerText, setComposerText } from "./composer-mentions";

/**
 * The pickers' insertion path: text lands AFTER the existing draft, separated
 * by a blank line, line breaks become paragraphs, and an empty composer takes
 * the text as its whole draft. Nothing is sent — the helper only edits the
 * document.
 */

let editor: Editor | null = null;

function newEditor(): Editor {
  editor = new Editor({ extensions: [StarterKit] });
  return editor;
}

afterEach(() => {
  editor?.destroy();
  editor = null;
});

describe("composerParagraphs", () => {
  it("makes one paragraph per line, empty for a blank line", () => {
    expect(composerParagraphs("a\n\nb")).toEqual([
      { type: "paragraph", content: [{ type: "text", text: "a" }] },
      { type: "paragraph", content: [] },
      { type: "paragraph", content: [{ type: "text", text: "b" }] },
    ]);
  });
});

describe("appendComposerText", () => {
  it("fills an empty composer with the text, line breaks as paragraphs", () => {
    const ed = newEditor();
    appendComposerText(ed, "user: Summarize README.md\n\nassistant: Reading.");
    expect(composerText(ed)).toBe(
      "user: Summarize README.md\n\nassistant: Reading.",
    );
  });

  it("appends after an existing draft, separated by a blank line, keeping the draft", () => {
    const ed = newEditor();
    setComposerText(ed, "Please look at this:");
    appendComposerText(ed, "Fixture notes: line one\nline two");
    expect(composerText(ed)).toBe(
      "Please look at this:\n\nFixture notes: line one\nline two",
    );
  });

  it("appends twice in order", () => {
    const ed = newEditor();
    appendComposerText(ed, "first");
    appendComposerText(ed, "second");
    expect(composerText(ed)).toBe("first\n\nsecond");
  });
});
