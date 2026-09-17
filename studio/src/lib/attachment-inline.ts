/**
 * Client-local attachments for the composer (the TUI's `@file` / `attach:`
 * parity): images and audio cross the wire as inline media parts, gated on
 * the session's resolved input modalities; every other file is inlined into
 * the prompt text as a delimited block the model reads like pasted text.
 *
 * The size ceilings and the human wording for the SDK's prompt validation
 * live here so the composer (stage time), the send path (wire time) and the
 * tests share one vocabulary. The media limits are the SDK's own constants —
 * a bump there moves the messages here.
 */

import {
  MAX_MEDIA_PART_BYTES,
  MAX_PROMPT_MEDIA_BYTES,
  MAX_PROMPT_MEDIA_PARTS,
  PromptValidationError,
} from "@stacklok-oss/mecatl-sdk";

/** The two media modalities a session may accept as prompt parts. */
export interface MediaCapabilities {
  image: boolean;
  audio: boolean;
}

/** Where one staged file goes: a media part, inlined text, or nowhere. */
export type AttachmentClass =
  | { kind: "image" }
  | { kind: "audio" }
  | { kind: "text" }
  | { kind: "rejected"; reason: string };

/** A text file above this is refused rather than inlined (TUI parity). */
export const MAX_INLINE_TEXT_BYTES = 256 * 1024;
/** All inlined text in one message, together. */
export const MAX_INLINE_TEXT_TOTAL_BYTES = 512 * 1024;

const KIB = 1024;
const MIB = 1024 * 1024;

/** "256 KiB" / "10 MiB" — whole units only, the limits are round. */
function formatBytes(bytes: number): string {
  if (bytes >= MIB && bytes % MIB === 0) return `${bytes / MIB} MiB`;
  if (bytes >= KIB && bytes % KIB === 0) return `${bytes / KIB} KiB`;
  if (bytes >= MIB) return `${(bytes / MIB).toFixed(1)} MiB`;
  if (bytes >= KIB) return `${Math.ceil(bytes / KIB)} KiB`;
  return `${bytes} bytes`;
}

/**
 * The media gate for one chat: the session's own echo (`session_capabilities`,
 * the per-session resolved modalities) when the daemon stamps one, else the
 * deployment's compatibility capabilities. Strict `=== true` on both — the
 * SDK refuses a part whose capability is not echoed true, so the client
 * never claims more than the wire will accept.
 */
export function resolveMediaCapabilities(
  session: MediaCapabilities | null | undefined,
  server: Record<string, unknown>,
): MediaCapabilities {
  if (session) {
    return { image: session.image === true, audio: session.audio === true };
  }
  return { image: server.image === true, audio: server.audio === true };
}

/**
 * Decides how one staged file would travel. `image/*` and `audio/*` need the
 * matching capability; anything else is a text candidate (its bytes are
 * sniffed when read). Audio is never re-encoded, so an oversized audio file
 * is refused here; images are downscaled on send, so their size is not.
 */
export function classifyAttachment(
  file: Pick<File, "name" | "type" | "size">,
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
    if (!capabilities.audio) {
      return {
        kind: "rejected",
        reason: `${file.name}: this model does not accept audio input.`,
      };
    }
    if (file.size > MAX_MEDIA_PART_BYTES) {
      return { kind: "rejected", reason: mediaPartTooLarge(file.name) };
    }
    return { kind: "audio" };
  }
  if (file.size > MAX_INLINE_TEXT_BYTES) {
    return { kind: "rejected", reason: textTooLarge(file.name) };
  }
  return { kind: "text" };
}

function textTooLarge(name: string): string {
  return `${name}: too large to inline — ${formatBytes(MAX_INLINE_TEXT_BYTES)} max per text file.`;
}

function mediaPartTooLarge(name?: string): string {
  const subject = name ? `${name}: each` : "Each";
  return `${subject} image or audio part must be under ${formatBytes(MAX_MEDIA_PART_BYTES)}.`;
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
    throw new Error(
      `${file.name}: binary files can't be attached — only images, audio and text.`,
    );
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
 * total. The prompt itself is never counted or altered.
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
    out += `\n\n--- attached file: ${name} ---\n${file.text}\n--- end of ${name} ---`;
  }
  return out;
}

/** Decoded byte length of standard base64 (padding-aware). */
export function base64ByteLength(data: string): number {
  const trimmed = data.replace(/\s+/g, "");
  if (trimmed.length === 0) return 0;
  let padding = 0;
  if (trimmed.endsWith("==")) padding = 2;
  else if (trimmed.endsWith("=")) padding = 1;
  return Math.floor((trimmed.length * 3) / 4) - padding;
}

/** Standard base64 of raw bytes, browser-safe (no Buffer), chunked so a
 *  multi-megabyte part never blows the call stack. */
export function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  const CHUNK = 0x8000;
  for (let index = 0; index < bytes.length; index += CHUNK) {
    binary += String.fromCharCode(...bytes.subarray(index, index + CHUNK));
  }
  return btoa(binary);
}

/**
 * Pre-flight over the staged media parts, in friendlier words than the SDK
 * (which enforces the same three ceilings when the prompt is built): the
 * part count, each part's bytes, and the per-message media total. Returns
 * the first problem, or null when the parts fit.
 */
export function checkMediaLimits(
  parts: readonly { kind: string; data: string; name?: string }[],
): string | null {
  const media = parts.filter(
    (part) => part.kind === "image" || part.kind === "audio",
  );
  if (media.length > MAX_PROMPT_MEDIA_PARTS) {
    return `${MAX_PROMPT_MEDIA_PARTS} media parts max per message — you attached ${media.length}.`;
  }
  let total = 0;
  for (const part of media) {
    const bytes = base64ByteLength(part.data);
    if (bytes > MAX_MEDIA_PART_BYTES) return mediaPartTooLarge(part.name);
    total += bytes;
  }
  if (total > MAX_PROMPT_MEDIA_BYTES) {
    return `${formatBytes(MAX_PROMPT_MEDIA_BYTES)} of media per message max — these come to ${formatBytes(total)}.`;
  }
  return null;
}

/** The kind named in an SDK capability/size message, when it names one. */
function mediaKindIn(message: string): "image" | "audio" | null {
  const match = /\b(image|audio)\b/i.exec(message);
  return match ? (match[1].toLowerCase() as "image" | "audio") : null;
}

/**
 * Turns the SDK's `PromptValidationError` (thrown when the prompt is built,
 * before any bytes leave the browser) into the same wording the pre-flight
 * uses. Covers every `PromptValidationReason`. Null for any other error, so
 * a caller can fall through to its usual message.
 */
export function mapPromptValidationError(error: unknown): string | null {
  if (!(error instanceof PromptValidationError)) return null;
  const message = error.message;
  switch (error.reason) {
    case "capability": {
      const kind = mediaKindIn(message);
      return kind
        ? `This model does not accept ${kind} input — remove the ${kind} attachment or pick a model that supports it.`
        : "This model does not accept image or audio input — remove the media attachments or pick a model that supports them.";
    }
    case "mime_type": {
      const kind = mediaKindIn(message);
      return kind
        ? `That file is not ${kind === "audio" ? "audio" : "an image"} the daemon recognises (its type must start with ${kind}/).`
        : "That file's type is not a media type the daemon recognises.";
    }
    case "size":
      if (/media parts/i.test(message)) {
        return `${MAX_PROMPT_MEDIA_PARTS} media parts max per message.`;
      }
      if (/inline media bytes/i.test(message)) {
        return `${formatBytes(MAX_PROMPT_MEDIA_BYTES)} of media per message max.`;
      }
      return mediaPartTooLarge();
    case "source_xor":
      return "Each media attachment needs exactly one source — inline bytes or an https URL.";
    case "url":
      return "A media URL must be an absolute https URL.";
    case "prompt":
      return "The message needs some text or an attachment.";
    default:
      return message;
  }
}
