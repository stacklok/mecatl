// SPDX-License-Identifier: Apache-2.0

import { localFileKind } from "../chat/local-file-preview";

export type WriterReference = { name: string; content: string };
export const maxWriterReferenceBytes = 8_000;
export const maxWriterReferences = 3;
export const maxWriterReferenceTotalBytes = 16_000;
const encoder = new TextEncoder();

export async function readWriterReference(file: File): Promise<WriterReference> {
  const kind = localFileKind(file.name, file.type);
  if (
    !["text", "markdown", "code"].includes(kind) ||
    !file.name.trim() ||
    file.name.length > 120 ||
    [...file.name].some((char) => {
      const code = char.codePointAt(0) ?? 0;
      return (
        code < 32 ||
        code === 127 ||
        (code >= 0xd800 && code <= 0xdfff) ||
        char === "/" ||
        char === "\\"
      );
    })
  )
    throw new Error("Choose a named text, Markdown, or source-code file (not an image or PDF).");
  if (file.size === 0 || file.size > maxWriterReferenceBytes)
    throw new Error("Reference must be nonempty and at most 8,000 bytes.");
  const bytes = await file.arrayBuffer();
  if (bytes.byteLength === 0 || bytes.byteLength > maxWriterReferenceBytes)
    throw new Error("Reference must be nonempty and at most 8,000 bytes.");
  let content: string;
  try {
    content = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    throw new Error("Reference must be valid UTF-8 text.");
  }
  if (
    !content ||
    [...content].some((char) => {
      const code = char.codePointAt(0) ?? 0;
      return (code < 32 && code !== 9 && code !== 10 && code !== 13) || code === 127;
    }) ||
    encoder.encode(content).length > maxWriterReferenceBytes
  )
    throw new Error("Reference contains binary/control data or exceeds 8,000 bytes.");
  return { name: file.name, content };
}

export function acceptWriterReference(
  existing: WriterReference[],
  file: WriterReference,
): string | undefined {
  if (existing.length >= maxWriterReferences) return "Only three reference files can be attached.";
  if (existing.some((item) => item.name === file.name))
    return "A reference with this name is already attached.";
  if (
    existing.reduce((sum, item) => sum + encoder.encode(item.content).length, 0) +
      encoder.encode(file.content).length >
    maxWriterReferenceTotalBytes
  )
    return "Reference contents exceed the 16,000-byte total limit.";
  return undefined;
}
