/**
 * Which tool profile each chat Studio minted was created with — browser-local
 * memory (localStorage), because the daemon fixes `profile` at create and
 * exposes it on no snapshot or inventory row afterwards. A live chat's Mode
 * menu reads it back as a display-only "Tools" line; a chat created by
 * another client (the TUI, a schedule fire) is simply UNKNOWN here and shows
 * no line — the memory never guesses.
 *
 * Bounded (the newest `MAX_REMEMBERED` ids win) and fail-soft: storage that
 * is disabled, full or holding junk reads as "unknown" and writes as a no-op.
 */

import {
  normalizeToolProfile,
  type SessionToolProfile,
} from "@/lib/tool-profile";

const STORAGE_KEY = "mecatl-studio.session-tool-profiles";
export const MAX_REMEMBERED = 200;

type Entries = [sessionId: string, profile: SessionToolProfile][];

function read(): Entries {
  if (typeof window === "undefined") return [];
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    const entries: Entries = [];
    for (const item of parsed) {
      if (!Array.isArray(item) || typeof item[0] !== "string" || !item[0])
        continue;
      entries.push([item[0], normalizeToolProfile(item[1])]);
    }
    return entries;
  } catch {
    return [];
  }
}

function write(entries: Entries): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(entries));
  } catch {
    // Storage disabled or full — the chat just reads as "unknown" later.
  }
}

/** Records the profile `sessionId` was created with (newest entry last). */
export function rememberSessionProfile(
  sessionId: string,
  profile: SessionToolProfile,
): void {
  if (!sessionId) return;
  const entries = read().filter(([id]) => id !== sessionId);
  entries.push([sessionId, normalizeToolProfile(profile)]);
  write(entries.slice(-MAX_REMEMBERED));
}

/**
 * The profile `sessionId` was created with, or null when Studio never
 * minted it (or the memory was evicted/cleared) — null is "unknown", never
 * a default.
 */
export function recallSessionProfile(
  sessionId: string,
): SessionToolProfile | null {
  if (!sessionId) return null;
  const hit = read().find(([id]) => id === sessionId);
  return hit ? hit[1] : null;
}
