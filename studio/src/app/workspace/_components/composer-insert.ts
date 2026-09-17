import type { Editor, JSONContent } from "@tiptap/react";

/**
 * Appending text to the composer WITHOUT replacing what the user already
 * typed — the insertion path behind the MCP prompt and resource pickers. The
 * TUI rewrites its whole input (`insertIntoInput` → `prompt.Rewrite`); the
 * web composer keeps the draft and adds the text after it, separated by a
 * blank line, so a half-written message is never lost to a picker. The
 * inserted text is REVIEWED, never sent: nothing here touches the send path.
 */

/**
 * The paragraph nodes for plain text — one per line, an empty paragraph for
 * a blank line (the shape `setComposerText` builds, so an inserted block
 * reads back byte-identically through `composerText`).
 */
export function composerParagraphs(text: string): JSONContent[] {
  return text.split("\n").map((line) => ({
    type: "paragraph",
    content: line ? [{ type: "text", text: line }] : [],
  }));
}

/**
 * Appends `text` to the composer: an empty composer takes it as the whole
 * draft; a non-empty one gets a blank paragraph then the text after its
 * current content. The caret lands at the end, ready for the user to edit
 * and press Enter.
 */
export function appendComposerText(editor: Editor, text: string): void {
  const paragraphs = composerParagraphs(text);
  if (editor.isEmpty) {
    editor
      .chain()
      .setContent({ type: "doc", content: paragraphs })
      .focus("end")
      .run();
    return;
  }
  const end = editor.state.doc.content.size;
  editor
    .chain()
    .insertContentAt(end, [{ type: "paragraph", content: [] }, ...paragraphs])
    .focus("end")
    .run();
}
