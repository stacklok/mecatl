/**
 * The chat route's arrival prompt — the web analogue of `mecatui -p/--prompt`
 * and `--prompt-file`. `/workspace/chat?prompt=<text>` opens a new chat with
 * the composer pre-filled; `/workspace/chat/<sessionId>?prompt=<text>` does
 * the same in an open chat. Adding `&send=1` asks for the prompt to be sent
 * as the next turn: the workspace puts the exact text in front of the user
 * as a one-click confirmation, and after that click the chat is an ordinary
 * interactive chat. The PWA share target (`app/manifest.ts`) lands here too,
 * prefill-only, as `prompt` (the shared text), `title` and `url`.
 *
 * Why `send=1` never sends without a click: unlike a CLI flag under the
 * operator's control, a URL is drive-by reachable — any web page, e-mail or
 * chat message can carry a link, and under posture auto/yolo a prompt is
 * tool execution in the user's workspace. A same-origin referrer would not
 * prove intent either (a link rendered in a chat message is same-origin),
 * so the gate is a visible confirmation, always. A leading-`/` prompt is a
 * command (`/clear` mints a successor and parks the queue), so it stays
 * prefill-only whatever `send` says — the composer's palette shows what it
 * would do, and Enter runs it.
 */

export interface ChatSeed {
  /** The text to put in the composer (or to send after confirmation). */
  prompt: string;
  /** `send=1` on a non-command prompt: offer the one-click send. */
  autoSend: boolean;
}

/** Longest arrival prompt honoured, in UTF-16 code units (32 KiB). */
export const MAX_CHAT_SEED_CHARS = 32 * 1024;

/** The shape Next hands a page as `searchParams` (repeated keys are arrays). */
export type ChatSeedParams = Record<string, string | string[] | undefined>;

/** The first value of a possibly repeated query key, "" when absent. */
function firstValue(value: string | string[] | undefined): string {
  if (Array.isArray(value)) return value[0] ?? "";
  return value ?? "";
}

/**
 * Drop C0 control characters and DEL, keeping `\n` and `\t` (the two the
 * composer renders). A URL carries `%00` or `%1B` verbatim and a share can
 * pass anything; the composer is a contenteditable, and a NUL or an escape
 * sequence in a prompt is never something the user meant to send. CRLF
 * folds to LF because `\r` goes.
 */
function stripControls(text: string): string {
  let out = "";
  for (const ch of text) {
    const code = ch.codePointAt(0) ?? 0;
    const control =
      (code < 0x20 && code !== 0x0a && code !== 0x09) || code === 0x7f;
    if (!control) out += ch;
  }
  return out;
}

/** Truncate to the cap without leaving a dangling lead surrogate. */
function clamp(text: string): string {
  if (text.length <= MAX_CHAT_SEED_CHARS) return text;
  let cut = text.slice(0, MAX_CHAT_SEED_CHARS);
  const last = cut.charCodeAt(cut.length - 1);
  if (last >= 0xd800 && last <= 0xdbff) cut = cut.slice(0, -1);
  return cut;
}

/**
 * Resolve the route's query into a seed, or null when there is nothing to
 * seed. Only the first value of a repeated key counts; control characters
 * other than newline and tab are dropped; whitespace-only text is nothing.
 * A share's `title` and `url` join the text on their own lines
 * (a part the text already contains is not repeated), so a page shared from
 * a phone arrives as "Title\nhttps://…" rather than as a bare URL.
 */
export function resolveChatSeed(
  params: ChatSeedParams | null | undefined,
): ChatSeed | null {
  if (!params) return null;
  const text = stripControls(firstValue(params.prompt)).trim();
  const title = stripControls(firstValue(params.title)).trim();
  const url = stripControls(firstValue(params.url)).trim();
  const parts: string[] = [];
  if (title && !text.includes(title)) parts.push(title);
  if (text) parts.push(text);
  if (url && !text.includes(url) && url !== title) parts.push(url);
  const prompt = clamp(parts.join("\n"));
  if (prompt === "") return null;
  return {
    prompt,
    autoSend: firstValue(params.send) === "1" && !isCommandSeed(prompt),
  };
}

/** A seed that would run as a slash command if sent — prefill-only. */
export function isCommandSeed(prompt: string): boolean {
  return prompt.startsWith("/");
}
