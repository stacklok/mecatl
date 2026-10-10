// SPDX-License-Identifier: Apache-2.0

/**
 * Client-local, non-image attachments for the composer, ported from the
 * prototype (`stack-08`). They are limited to what Studio's BFF carries today:
 *
 * - Images travel as before, in `startRunRequestSchema`'s `images[]`, gated on
 *   the model's image capability and the limits in `local-file-preview.ts`.
 * - Small text files are inlined into the prompt text as a delimited block the
 *   model reads like pasted text. No schema change is needed, and they show in
 *   the transcript because they are part of the sent prompt.
 * - Audio is always rejected: the run request has no shape for it.
 * - Anything else (binary content, oversized text) is rejected with a reason
 *   the composer shows inline.
 */

/** The one media modality this repo's wire format actually carries. Kept as
 *  an object (not a bare boolean) so a future audio wire-up is an additive
 *  change here rather than a call-site rewrite. */
export interface MediaCapabilities {
  image: boolean;
}

/** Where one staged file goes: an image media part, inlined text, or
 *  nowhere. */
export type AttachmentClass =
  | { kind: "image" }
  | { kind: "text" }
  | { kind: "rejected"; reason: string };

/** A text file above this is refused rather than inlined. */
export const MAX_INLINE_TEXT_BYTES = 256 * 1024;
/** All inlined text in one message, together. */
export const MAX_INLINE_TEXT_TOTAL_BYTES = 512 * 1024;

const KIB = 1024;
const MIB = 1024 * 1024;

/** "256 KiB" / "10 MiB" — whole units only, the limits above are round. */
function formatBytes(bytes: number): string {
  if (bytes >= MIB && bytes % MIB === 0) return `${bytes / MIB} MiB`;
  if (bytes >= KIB && bytes % KIB === 0) return `${bytes / KIB} KiB`;
  if (bytes >= MIB) return `${(bytes / MIB).toFixed(1)} MiB`;
  if (bytes >= KIB) return `${Math.ceil(bytes / KIB)} KiB`;
  return `${bytes} bytes`;
}

function textTooLarge(name: string): string {
  return `${name}: too large to inline — ${formatBytes(MAX_INLINE_TEXT_BYTES)} max per text file.`;
}

/**
 * Decides how one staged file would travel. `image/*` needs the image
 * capability; `audio/*` is always rejected (see the module doc); anything
 * else is a text candidate (its bytes are sniffed for binary content when
 * actually read, in `readTextAttachment`).
 */
export function classifyAttachment(
  file: Pick<File, "name" | "size" | "type">,
  capabilities: MediaCapabilities,
): AttachmentClass {
  const type = file.type.toLowerCase();
  if (type.startsWith("image/")) {
    return capabilities.image
      ? { kind: "image" }
      : {
          kind: "rejected",
          reason: `${file.name}: this model does not accept image input.`,
        };
  }
  if (type.startsWith("audio/")) {
    return {
      kind: "rejected",
      reason: `${file.name}: audio attachments are not supported yet.`,
    };
  }
  if (file.size > MAX_INLINE_TEXT_BYTES) {
    return { kind: "rejected", reason: textTooLarge(file.name) };
  }
  return { kind: "text" };
}

const NUL = String.fromCharCode(0);
/** What a UTF-8 decode leaves where binary bytes were. */
const REPLACEMENT_CHAR = 0xfffd;
const BINARY_RATIO = 0.1;

/** A C0/C1 control character other than tab, newline and carriage return,
 *  DEL, or the replacement character — the marks of a binary decode. */
function isSuspiciousCodePoint(code: number): boolean {
  if (code === 0x09 || code === 0x0a || code === 0x0d) return false;
  if (code < 0x20 || code === 0x7f) return true;
  if (code >= 0x80 && code < 0xa0) return true;
  return code === REPLACEMENT_CHAR;
}

/** True when decoded content reads as binary: a NUL byte, or more than 10%
 *  control / replacement characters. Empty content is text. */
export function isBinaryText(text: string): boolean {
  if (text.length === 0) return false;
  if (text.includes(NUL)) return true;
  let suspicious = 0;
  for (const char of text) {
    if (isSuspiciousCodePoint(char.codePointAt(0) ?? 0)) suspicious += 1;
  }
  return suspicious / text.length > BINARY_RATIO;
}

/**
 * Reads a text candidate for inlining. Rejects (with the user-facing reason
 * as the Error message) a file over the per-file ceiling before reading it,
 * and binary content after.
 */
export async function readTextAttachment(file: File): Promise<string> {
  if (file.size > MAX_INLINE_TEXT_BYTES) {
    throw new Error(textTooLarge(file.name));
  }
  const text = await file.text();
  if (isBinaryText(text)) {
    throw new Error(`${file.name}: binary files can't be attached — only images and text.`);
  }
  return text;
}

/** Control characters (which include every ASCII/NEL line terminator) and
 *  the Unicode line/paragraph separators. */
const NAME_BREAKERS = /[\p{Cc}\p{Zl}\p{Zp}]+/gu;

/**
 * A file name safe to print inside the model-visible delimiter: line
 * terminators and control characters are collapsed to spaces so a hostile
 * name can never close the block early or forge a header. Empty names
 * become "attachment".
 */
export function sanitizeAttachmentName(name: string): string {
  const cleaned = name.replace(NAME_BREAKERS, " ").replace(/\s+/g, " ").trim();
  return cleaned || "attachment";
}

const encoder = new TextEncoder();

/**
 * Appends each text attachment to the prompt as a delimited block:
 *
 *     --- attached file: <name> ---
 *     <text>
 *     --- end of <name> ---
 *
 * Throws (user-facing message) when the inlined text exceeds the per-message
 * total. The prompt itself is never counted or altered — this is why text
 * attachments need no `startRunRequest` schema change: they become part of
 * the same `prompt` string the daemon already accepts, and round-trip
 * through the transcript exactly as typed text does.
 */
export function inlineTextAttachments(
  prompt: string,
  files: readonly { name: string; text: string }[],
): string {
  if (files.length === 0) return prompt;
  let total = 0;
  let out = prompt;
  for (const file of files) {
    total += encoder.encode(file.text).byteLength;
    if (total > MAX_INLINE_TEXT_TOTAL_BYTES) {
      throw new Error(
        `Attached text comes to ${formatBytes(total)} — ${formatBytes(MAX_INLINE_TEXT_TOTAL_BYTES)} of inlined text per message max. Remove a file or send it separately.`,
      );
    }
    const name = sanitizeAttachmentName(file.name);
    const block = `--- attached file: ${name} ---\n${file.text}\n--- end of ${name} ---`;
    out = out ? `${out}\n\n${block}` : block;
  }
  return out;
}
