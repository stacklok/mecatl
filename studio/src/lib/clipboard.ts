import { toast } from "sonner";

/** Why a clipboard write did not happen. */
export type ClipboardFailure = "unavailable" | "blocked";

/**
 * The user-facing sentence for a failed copy. `unavailable` means the page
 * has no async Clipboard API at all (an external-mode Studio served over
 * plain http, or a browser without it); `blocked` means the API exists and
 * the browser refused the write (a permissions policy, or no user gesture).
 */
export function clipboardFailureMessage(reason: ClipboardFailure): string {
  switch (reason) {
    case "unavailable":
      return "Clipboard unavailable — select the ID and copy it manually";
    case "blocked":
      return "Couldn't copy — clipboard blocked";
  }
}

/**
 * Writes `text` to the clipboard without reporting: the outcome is the
 * caller's to present. Never throws — a missing API and a refused write both
 * come back as a named reason.
 */
export async function writeClipboardText(
  text: string,
): Promise<{ ok: true } | { ok: false; reason: ClipboardFailure }> {
  const clipboard =
    typeof navigator === "undefined" ? undefined : navigator.clipboard;
  if (!clipboard || typeof clipboard.writeText !== "function") {
    return { ok: false, reason: "unavailable" };
  }
  try {
    await clipboard.writeText(text);
    return { ok: true };
  } catch {
    return { ok: false, reason: "blocked" };
  }
}

/**
 * Copies `text` and tells the user what happened: `"<label> copied"` on
 * success, otherwise the honest failure sentence (the clipboard is absent on
 * a plain-http page, or the browser blocked the write). Resolves to whether
 * the copy landed, so a caller can flip a "Copied" affordance only when it
 * did. The ONE copy-with-feedback policy for ids and other short values —
 * a new copy button reaches for this rather than `navigator.clipboard`.
 */
export async function copyToClipboard(
  text: string,
  label: string,
): Promise<boolean> {
  const outcome = await writeClipboardText(text);
  if (outcome.ok) {
    toast.success(`${label} copied`);
    return true;
  }
  toast.error(clipboardFailureMessage(outcome.reason));
  return false;
}
