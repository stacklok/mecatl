import Mention from "@tiptap/extension-mention";
import { PluginKey } from "@tiptap/pm/state";
import type { Editor } from "@tiptap/react";
import type { SuggestionOptions } from "@tiptap/suggestion";

/**
 * Atomic `@agent` / `/skill` chips for the TipTap composer.
 *
 * Each is a Mention node (an atom, so Backspace removes the whole chip in one
 * press) rendered as the same green "connected" pill the old overlay painted.
 * `renderText` serializes a chip back to `@handle` / `/name`, so `editor.getText`
 * reconstructs the exact plain-text message the model receives.
 *
 * Autocomplete is delegated to the caller: the caller supplies the filtered
 * items per keystroke and a `render` that drives its own menu UI (we reuse the
 * existing full-width popover rather than TipTap's default), keeping the
 * on-screen behaviour identical to the pre-TipTap composer.
 */

/** One row in the autocomplete menu (and the data an inserted chip carries). */
export interface ComposerMenuItem {
  /** Text inserted after the trigger char — the agent handle or command name. */
  readonly id: string;
  /** Chip label (same as `id`; the trigger char is added by `renderText`). */
  readonly label: string;
  /** Primary text shown in the menu row. */
  readonly primary: string;
  /** Muted description shown in the menu row. */
  readonly secondary: string;
}

/** The trigger kind, so the caller can render agent vs command rows. */
export type ComposerMentionKind = "agent" | "command";

/** `render` factory: given the kind, return a TipTap suggestion renderer. */
type MakeRender = (
  kind: ComposerMentionKind,
) => SuggestionOptions<ComposerMenuItem>["render"];

const CHIP_CLASS =
  "rounded px-1 py-0.5 bg-emerald-500/15 text-emerald-700 dark:text-emerald-400";

function mentionNode(
  name: string,
  char: string,
  pluginKeyName: string,
  items: (query: string) => ComposerMenuItem[],
  makeRender: MakeRender,
  kind: ComposerMentionKind,
  allow?: SuggestionOptions<ComposerMenuItem>["allow"],
) {
  return Mention.extend({ name }).configure({
    HTMLAttributes: { class: CHIP_CLASS, "data-composer-mention": name },
    // `id` is what renderText appends after the trigger char.
    renderText: ({ node }) => `${char}${node.attrs.label ?? node.attrs.id}`,
    suggestion: {
      char,
      pluginKey: new PluginKey(pluginKeyName),
      items: ({ query }) => items(query),
      allow,
      render: makeRender(kind),
    },
  });
}

/**
 * Build the two mention extensions. `agentItems` / `commandItems` filter the
 * live lists for the current query; `makeRender` wires each suggestion to the
 * caller's menu. Commands only trigger at the very start of the message
 * (`range.from === 1`), matching the old "slash menu only when the message is a
 * single /token" rule; agents trigger anywhere after whitespace.
 */
export function createComposerMentions(opts: {
  agentItems: (query: string) => ComposerMenuItem[];
  commandItems: (query: string) => ComposerMenuItem[];
  makeRender: MakeRender;
}) {
  return [
    mentionNode(
      "agentMention",
      "@",
      "composerAgentMention",
      opts.agentItems,
      opts.makeRender,
      "agent",
    ),
    mentionNode(
      "commandMention",
      "/",
      "composerCommandMention",
      opts.commandItems,
      opts.makeRender,
      "command",
      ({ range }) => range.from === 1,
    ),
  ];
}

/** Serialize the editor to the plain-text message (chips → `@handle`/`/name`). */
export function composerText(editor: Editor): string {
  return editor.getText({ blockSeparator: "\n" }).trim();
}

/** Replace the whole document with plain text (voice input, quoted replies). */
export function setComposerText(editor: Editor, text: string) {
  const content = text.split("\n").map((line) => ({
    type: "paragraph",
    content: line ? [{ type: "text", text: line }] : [],
  }));
  editor.commands.setContent({ type: "doc", content });
}
