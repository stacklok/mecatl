// SPDX-License-Identifier: Apache-2.0

export const maxWriterDocumentBytes = 400_000;

export async function readWriterDocument(file: File): Promise<string> {
  if (
    !/\.(?:md|markdown|txt)$/i.test(file.name) ||
    (file.type !== "" && !["text/plain", "text/markdown", "text/x-markdown"].includes(file.type))
  )
    throw new Error("Choose a Markdown (.md, .markdown) or plain-text (.txt) file.");
  if (file.size > maxWriterDocumentBytes) throw new Error("Document exceeds 400,000 UTF-8 bytes.");
  const bytes = await file.arrayBuffer();
  if (bytes.byteLength > maxWriterDocumentBytes)
    throw new Error("Document exceeds 400,000 UTF-8 bytes.");
  let content: string;
  try {
    content = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    throw new Error("Document must be valid UTF-8 text.");
  }
  if (
    [...content].some((char) => {
      const code = char.codePointAt(0) ?? 0;
      return (
        (code < 32 && code !== 9 && code !== 10 && code !== 13) || (code >= 127 && code <= 159)
      );
    })
  )
    throw new Error("Document contains binary or control data.");
  if (content.length > 100_000) throw new Error("Document exceeds 100,000 characters.");
  return content;
}
