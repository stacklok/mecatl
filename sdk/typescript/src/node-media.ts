import { readFile } from "node:fs/promises";

import { type AudioPromptPart, audioPart, type ImagePromptPart, imagePart } from "./media.js";

/** Read a Node.js or Bun path into an image prompt part. @public */
export async function imagePartFromPath(
  path: string | URL,
  mimeType: string,
): Promise<ImagePromptPart> {
  const bytes = await readFile(path);
  return imagePart({ bytes, mimeType });
}

/** Read a Node.js or Bun path into an audio prompt part. @public */
export async function audioPartFromPath(
  path: string | URL,
  mimeType: string,
): Promise<AudioPromptPart> {
  const bytes = await readFile(path);
  return audioPart({ bytes, mimeType });
}
