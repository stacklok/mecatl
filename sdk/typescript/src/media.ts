import { PromptValidationError, type PromptValidationReason } from "./errors.js";

/** Maximum inline bytes in one image or audio part. @public */
export const MAX_MEDIA_PART_BYTES = 10 << 20;

/** Maximum inline media bytes in one prompt. @public */
export const MAX_PROMPT_MEDIA_BYTES = 20 << 20;

/** Maximum image and audio parts in one prompt. @public */
export const MAX_PROMPT_MEDIA_PARTS = 16;

/** The source accepted by imagePart() and audioPart(). Exactly one field is required. @public */
export interface MediaPartSource {
  /** Inline media bytes. */
  bytes?: Uint8Array;
  /** Absolute HTTPS media URL. */
  url?: string;
}

/** Options accepted by imagePart() and audioPart(). @public */
export interface MediaPartOptions extends MediaPartSource {
  /** Media type beginning with `image/` or `audio/` for the selected helper. */
  mimeType: string;
}

/** A text segment in a structured prompt. @public */
export interface TextPromptPart {
  readonly kind: "text";
  readonly text: string;
}

/** An image in a structured prompt. @public */
export interface ImagePromptPart {
  readonly bytes?: Uint8Array;
  readonly kind: "image";
  readonly mimeType: string;
  readonly url?: string;
}

/** Audio in a structured prompt. @public */
export interface AudioPromptPart {
  readonly bytes?: Uint8Array;
  readonly kind: "audio";
  readonly mimeType: string;
  readonly url?: string;
}

/** One segment accepted by Session.run(). @public */
export type PromptPart = TextPromptPart | ImagePromptPart | AudioPromptPart;

/** A backwards-compatible string prompt or structured text/media parts. @public */
export type PromptInput = string | readonly PromptPart[];

/**
 * Constructs a text segment for a structured prompt.
 *
 * @param text - Text to send in this prompt segment.
 * @returns A text prompt part.
 * @public
 */
export function textPart(text: string): TextPromptPart {
  return { kind: "text", text };
}

/**
 * Constructs an image part from inline bytes or an HTTPS URL.
 *
 * @param options - Image source and MIME type.
 * @returns A validated image prompt part.
 * @throws `PromptValidationError` when the source, MIME type, or size is invalid.
 * @public
 */
export function imagePart(options: MediaPartOptions): ImagePromptPart {
  return mediaPart("image", options);
}

/**
 * Constructs an audio part from inline bytes or an HTTPS URL.
 *
 * @param options - Audio source and MIME type.
 * @returns A validated audio prompt part.
 * @throws `PromptValidationError` when the source, MIME type, or size is invalid.
 * @public
 */
export function audioPart(options: MediaPartOptions): AudioPromptPart {
  return mediaPart("audio", options);
}

/**
 * Constructs an image part from a browser Blob or File.
 *
 * @param blob - Browser media value to read.
 * @param mimeType - Image MIME type. Defaults to the Blob's type.
 * @returns A validated image prompt part containing the Blob's bytes.
 * @throws `PromptValidationError` when the MIME type or size is invalid.
 * @public
 */
export async function imagePartFromBlob(
  blob: Blob,
  mimeType: string = blob.type,
): Promise<ImagePromptPart> {
  return imagePart({ bytes: new Uint8Array(await blob.arrayBuffer()), mimeType });
}

/**
 * Constructs an audio part from a browser Blob or File.
 *
 * @param blob - Browser media value to read.
 * @param mimeType - Audio MIME type. Defaults to the Blob's type.
 * @returns A validated audio prompt part containing the Blob's bytes.
 * @throws `PromptValidationError` when the MIME type or size is invalid.
 * @public
 */
export async function audioPartFromBlob(
  blob: Blob,
  mimeType: string = blob.type,
): Promise<AudioPromptPart> {
  return audioPart({ bytes: new Uint8Array(await blob.arrayBuffer()), mimeType });
}

type MediaKind = "audio" | "image";
type MediaPromptPart = AudioPromptPart | ImagePromptPart;

function invalid(reason: PromptValidationReason, message: string): never {
  throw new PromptValidationError(reason, message);
}

function mediaPart(kind: "image", options: MediaPartOptions): ImagePromptPart;
function mediaPart(kind: "audio", options: MediaPartOptions): AudioPromptPart;
function mediaPart(kind: MediaKind, options: MediaPartOptions): MediaPromptPart {
  const hasBytes = options.bytes instanceof Uint8Array && options.bytes.byteLength > 0;
  const hasURL = typeof options.url === "string" && options.url.length > 0;
  if (hasBytes === hasURL) {
    invalid("source_xor", `${kind} part requires exactly one of bytes or url`);
  }
  validateMIME(kind, options.mimeType);
  if (hasBytes) {
    const bytes = options.bytes;
    if (bytes === undefined) invalid("source_xor", `${kind} part bytes are required`);
    if (bytes.byteLength > MAX_MEDIA_PART_BYTES) {
      invalid(
        "size",
        `${kind} part is ${bytes.byteLength} bytes; limit is ${MAX_MEDIA_PART_BYTES}`,
      );
    }
    return { bytes: bytes.slice(), kind, mimeType: options.mimeType };
  }
  validateHTTPS(options.url);
  return { kind, mimeType: options.mimeType, url: options.url };
}

function validateMIME(kind: MediaKind, mimeType: unknown): asserts mimeType is string {
  if (typeof mimeType !== "string" || !mimeType.toLowerCase().startsWith(`${kind}/`)) {
    invalid("mime_type", `${kind} part MIME type must start with ${kind}/`);
  }
}

function validateHTTPS(rawURL: unknown): asserts rawURL is string {
  if (typeof rawURL !== "string") invalid("source_xor", "media URL is required");
  let parsed: URL;
  try {
    parsed = new URL(rawURL);
  } catch {
    invalid("url", "media URL must be an absolute HTTPS URL");
  }
  if (parsed.protocol !== "https:") {
    invalid("url", "media URL must use HTTPS");
  }
}

export interface EncodedPrompt {
  readonly media: readonly MediaPromptPart[];
  readonly text: string;
}

export interface PromptCapabilities {
  readonly audio: boolean;
  readonly image: boolean;
}

export function encodePrompt(
  prompt: PromptInput,
  capabilities: PromptCapabilities | undefined,
): EncodedPrompt {
  if (typeof prompt === "string") return { media: [], text: prompt };
  if (!Array.isArray(prompt)) invalid("prompt", "prompt must be a string or an array of parts");

  const text: string[] = [];
  const media: MediaPromptPart[] = [];
  let totalBytes = 0;
  for (const part of prompt as readonly PromptPart[]) {
    if (part?.kind === "text") {
      if (typeof part.text !== "string") invalid("prompt", "text part must contain text");
      text.push(part.text);
      continue;
    }
    if (part?.kind !== "image" && part?.kind !== "audio") {
      invalid("prompt", "prompt contains an unknown part kind");
    }
    const checked = part.kind === "image" ? mediaPart("image", part) : mediaPart("audio", part);
    if (capabilities?.[checked.kind] !== true) {
      invalid(
        "capability",
        capabilities === undefined
          ? `session did not echo ${checked.kind} capability`
          : `session capability rejects ${checked.kind} input`,
      );
    }
    media.push(checked);
    totalBytes += checked.bytes?.byteLength ?? 0;
  }
  if (media.length > MAX_PROMPT_MEDIA_PARTS) {
    invalid("size", `prompt has ${media.length} media parts; limit is ${MAX_PROMPT_MEDIA_PARTS}`);
  }
  if (totalBytes > MAX_PROMPT_MEDIA_BYTES) {
    invalid(
      "size",
      `prompt has ${totalBytes} inline media bytes; limit is ${MAX_PROMPT_MEDIA_BYTES}`,
    );
  }
  const flattened = text.join("\n");
  if (flattened.length === 0 && media.length === 0) {
    invalid("prompt", "prompt must contain text or media");
  }
  return { media, text: flattened };
}
