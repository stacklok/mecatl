import { readFile } from "node:fs/promises";

import { type AudioPromptPart, audioPart, type ImagePromptPart, imagePart } from "./media.js";

/**
 * Reads a Node.js or Bun path into an image prompt part.
 *
 * @param path - File path or file URL to read.
 * @param mimeType - Image MIME type for the file contents.
 * @returns A validated image prompt part containing the file's bytes.
 * @throws `PromptValidationError` when the MIME type or size is invalid.
 * @public
 */
export async function imagePartFromPath(
  path: string | URL,
  mimeType: string,
): Promise<ImagePromptPart> {
  const bytes = await readFile(path);
  return imagePart({ bytes, mimeType });
}

/**
 * Reads a Node.js or Bun path into an audio prompt part.
 *
 * @param path - File path or file URL to read.
 * @param mimeType - Audio MIME type for the file contents.
 * @returns A validated audio prompt part containing the file's bytes.
 * @throws `PromptValidationError` when the MIME type or size is invalid.
 * @public
 */
export async function audioPartFromPath(
  path: string | URL,
  mimeType: string,
): Promise<AudioPromptPart> {
  const bytes = await readFile(path);
  return audioPart({ bytes, mimeType });
}
