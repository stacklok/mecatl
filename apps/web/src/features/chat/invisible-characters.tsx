// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from "react";
import { Badge } from "@/components/ui/badge";
import { ASK_ARGS_PARSE_LIMIT } from "./ask-args";

/**
 * The characters an approval view shows as badges. Each one renders as nothing
 * or changes how the text around it reads, so the text a reviewer reads could
 * differ from the text that runs:
 * - the bidirectional controls (U+202A–U+202E, U+2066–U+2069) and the implicit
 *   marks (U+200E, U+200F, U+061C);
 * - the zero-width characters (U+200B–U+200D, U+FEFF), the word joiner
 *   (U+2060), and the invisible operators (U+2061–U+2064);
 * - the tag characters (U+E0000–U+E007F).
 * The variation selectors (U+FE00–U+FE0F) stay out, so emoji keep their look.
 */
const INVISIBLE =
  /[\u061C\u200B-\u200F\u202A-\u202E\u2060-\u2064\u2066-\u2069\uFEFF\u{E0000}-\u{E007F}]/u;
// The u flag reads a tag character's surrogate pair as one code point, so it
// gets one badge.
const EVERY_INVISIBLE = new RegExp(INVISIBLE.source, "gu");

/** Whether `text` holds a character `VisibleText` shows as a badge. */
export function hasInvisibleCharacters(text: string): boolean {
  return INVISIBLE.test(text);
}

/**
 * Whether an ask's raw `args` carry an invisible character, as sent or decoded
 * from a JSON escape such as `\u202e`: the formatted views show decoded text.
 */
export function argsHaveInvisibleCharacters(args: string): boolean {
  if (hasInvisibleCharacters(args)) return true;
  if (args.length > ASK_ARGS_PARSE_LIMIT) return false;
  try {
    return hasInvisibleCharacters(JSON.stringify(JSON.parse(args)));
  } catch {
    return false;
  }
}

/** "U+202E" for one character. */
function codePoint(character: string): string {
  return `U+${(character.codePointAt(0) ?? 0).toString(16).toUpperCase().padStart(4, "0")}`;
}

/**
 * `text` with each invisible character shown as a badge that names its code
 * point, such as ⟨U+202E⟩. The badge keeps the character itself as its only
 * text and draws the label with CSS, so selecting and copying still yields the
 * exact original text. The badge's own box keeps a bidirectional control from
 * reordering the text around it. Text without one renders unchanged.
 */
export function VisibleText({ text }: { text: string }): ReactNode {
  const parts: ReactNode[] = [];
  let start = 0;
  for (const match of text.matchAll(EVERY_INVISIBLE)) {
    const character = match[0];
    if (match.index > start) parts.push(text.slice(start, match.index));
    const code = codePoint(character);
    parts.push(
      <Badge
        aria-label={`invisible character ${code}`}
        className="mx-px gap-0 px-1 py-0 leading-none before:content-[attr(data-label)]"
        data-label={`⟨${code}⟩`}
        key={match.index}
        role="img"
        variant="warning"
      >
        {character}
      </Badge>,
    );
    start = match.index + character.length;
  }
  if (parts.length === 0) return text;
  if (start < text.length) parts.push(text.slice(start));
  return parts;
}
