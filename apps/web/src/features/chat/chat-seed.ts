// SPDX-License-Identifier: Apache-2.0

const maxSeedCodeUnits = 32 * 1024;

export interface ChatSeed {
  requiresConfirmation: boolean;
  text: string;
}

/** Parse a browser-only arrival seed and return the URL the router should replace it with. */
export function consumeChatSeed(arrival: URL): { seed?: ChatSeed; url: URL } {
  const url = new URL(arrival);
  const raw = url.searchParams.get("prompt");
  const requiresConfirmation = url.searchParams.get("send") === "1";
  url.searchParams.delete("prompt");
  url.searchParams.delete("send");
  if (raw === null) return { url };

  let sanitized = "";
  for (const char of raw) {
    const code = char.codePointAt(0) ?? 0;
    if ((code < 32 && code !== 9 && code !== 10) || code === 127) continue;
    sanitized += char;
  }
  const clean = sanitized.trim();
  let text = clean.slice(0, maxSeedCodeUnits);
  const last = text.charCodeAt(text.length - 1);
  if (last >= 0xd800 && last <= 0xdbff) text = text.slice(0, -1);
  return text ? { seed: { requiresConfirmation, text }, url } : { url };
}
