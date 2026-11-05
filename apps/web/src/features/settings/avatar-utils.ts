// SPDX-License-Identifier: Apache-2.0

/**
 * Returns the avatar value only when it is an inline image data URL, the form
 * the picker produces, so a stored value can never become another URL scheme.
 */
export function safeAvatarSrc(value: string) {
  return value.startsWith("data:image/") ? value : "";
}

export function scaledAvatarSize(width: number, height: number, maximum = 512) {
  if (width <= 0 || height <= 0 || maximum <= 0) return { height: 1, width: 1 };
  const scale = Math.min(1, maximum / Math.max(width, height));
  return {
    height: Math.max(1, Math.round(height * scale)),
    width: Math.max(1, Math.round(width * scale)),
  };
}
