/**
 * Turns the composer's typed text plus its staged files into what one send
 * (or mid-run steer) carries: the wire text with every text file inlined as
 * a delimited block, the image/audio media parts, and the message-bubble
 * attachments (a data: URL for media, the inlined content for text so the
 * chip previews it). Pure apart from reading the files — the image encoder
 * is injected because it needs a canvas.
 */

import {
  type AttachmentClass,
  bytesToBase64,
  checkMediaLimits,
  classifyAttachment,
  inlineTextAttachments,
  type MediaCapabilities,
  readTextAttachment,
} from "@/lib/attachment-inline";
import type { PromptPart } from "@/lib/harness/client";
import type { Attachment } from "./types";

export interface PromptPayload {
  /** The text the daemon receives (typed text + inlined text files). */
  text: string;
  /** Image/audio parts, in staging order. */
  parts: PromptPart[];
  /** Chips for the user bubble, in staging order; undefined without files. */
  attachments: Attachment[] | undefined;
}

export type PromptPayloadResult =
  | ({ ok: true } & PromptPayload)
  | { ok: false; error: string };

/**
 * Builds the payload, refusing the whole send on the first problem so
 * nothing partial reaches the daemon: a file the session's modalities
 * reject, an unreadable/binary/oversized text file, or media over the SDK's
 * ceilings. The typed text is never altered — inlined blocks are appended.
 */
export async function buildPromptPayload(input: {
  text: string;
  files: readonly File[];
  capabilities: MediaCapabilities;
  encodeImage: (file: File) => Promise<PromptPart>;
}): Promise<PromptPayloadResult> {
  const { text, files, capabilities, encodeImage } = input;
  if (files.length === 0) {
    return { ok: true, text, parts: [], attachments: undefined };
  }

  // Classify everything before reading anything: a refusal names the file
  // and costs no decode work.
  const classes: AttachmentClass[] = [];
  for (const file of files) {
    const verdict = classifyAttachment(file, capabilities);
    if (verdict.kind === "rejected")
      return { ok: false, error: verdict.reason };
    classes.push(verdict);
  }

  const parts: PromptPart[] = [];
  /** The staged file each part came from (parts skip text files). */
  const partNames: string[] = [];
  const attachments: Attachment[] = [];
  const inlined: { name: string; text: string }[] = [];
  try {
    for (const [index, file] of files.entries()) {
      const verdict = classes[index];
      if (verdict?.kind === "image") {
        const part = await encodeImage(file);
        parts.push(part);
        partNames.push(file.name);
        attachments.push({
          name: file.name,
          type: file.type,
          url: `data:${part.mime_type};base64,${part.data}`,
        });
      } else if (verdict?.kind === "audio") {
        const part: PromptPart = {
          kind: "audio",
          mime_type: file.type,
          data: bytesToBase64(new Uint8Array(await file.arrayBuffer())),
        };
        parts.push(part);
        partNames.push(file.name);
        attachments.push({
          name: file.name,
          type: file.type,
          url: `data:${part.mime_type};base64,${part.data}`,
        });
      } else {
        const content = await readTextAttachment(file);
        inlined.push({ name: file.name, text: content });
        attachments.push({
          name: file.name,
          type: file.type || "text/plain",
          content,
        });
      }
    }
    const limit = checkMediaLimits(
      parts.map((part, index) => ({ ...part, name: partNames[index] })),
    );
    if (limit) return { ok: false, error: limit };
    return {
      ok: true,
      text: inlineTextAttachments(text, inlined),
      parts,
      attachments,
    };
  } catch (caught) {
    return {
      ok: false,
      error: caught instanceof Error ? caught.message : String(caught),
    };
  }
}
