// SPDX-License-Identifier: Apache-2.0

const avatarImageTypes = {
  "image/gif": "image/gif",
  "image/jpeg": "image/jpeg",
  "image/png": "image/png",
  "image/webp": "image/webp",
} as const;

/**
 * Decodes a stored avatar, an inline base64 image data URL as the picker
 * produces, into image bytes. Anything else yields undefined. Rendering the
 * bytes through an object URL means the stored text never becomes markup or a
 * URL the page navigates to.
 */
export function avatarImageBlob(value: string): Blob | undefined {
  const match = /^data:(image\/[a-z]+);base64,([A-Za-z0-9+/]+={0,2})$/.exec(value);
  const [, mediaType = "", payload = ""] = match ?? [];
  const type = avatarImageTypes[mediaType as keyof typeof avatarImageTypes];
  if (!type) return undefined;
  let binary: string;
  try {
    binary = atob(payload);
  } catch {
    return undefined;
  }
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index);
  return new Blob([bytes], { type });
}

export function scaledAvatarSize(width: number, height: number, maximum = 512) {
  if (width <= 0 || height <= 0 || maximum <= 0) return { height: 1, width: 1 };
  const scale = Math.min(1, maximum / Math.max(width, height));
  return {
    height: Math.max(1, Math.round(height * scale)),
    width: Math.max(1, Math.round(width * scale)),
  };
}
