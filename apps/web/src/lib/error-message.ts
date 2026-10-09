// SPDX-License-Identifier: Apache-2.0

/**
 * The shared RFC 9457 error reader: an application `problem+json` body's
 * `detail`, an `Error`'s message, or a generic fallback for anything else (an
 * abort, a thrown string).
 *
 * Consolidates the identical copies that used to live in the chat, knowledge,
 * schedules, and memory settings features. A caller whose fallback sentence
 * differs (storage settings, the schedule form) keeps its own reader.
 */
export function errorMessage(error: unknown): string {
  if (typeof error === "object" && error !== null && "detail" in error) {
    return String(error.detail);
  }
  return error instanceof Error ? error.message : "The request could not be completed.";
}

/**
 * A daemon refusal rendered verbatim inline on a page (D7-43): a
 * `whitespace-pre-wrap` block so a multi-line refusal keeps its line breaks,
 * on a soft destructive card. Shared so the skills/memory/learning restyle
 * passes apply it consistently instead of each hand-rolling the string, the
 * way `schedules-workspace.tsx`/`schedule-detail.tsx`/`schedule-dialog.tsx`
 * each did in Phase 7.
 */
export const REFUSAL_BANNER_CLASS =
  "whitespace-pre-wrap rounded-lg border border-destructive/30 bg-destructive/5 px-4 py-3 text-sm text-destructive";

/** The same refusal banner, narrower (`px-3 py-2`) for use inside a dialog. */
export const REFUSAL_BANNER_DIALOG_CLASS =
  "whitespace-pre-wrap rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive";
