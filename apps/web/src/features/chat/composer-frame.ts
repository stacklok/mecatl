// SPDX-License-Identifier: Apache-2.0

import type { SessionPermissionMode } from "../../lib/permission-mode";

/**
 * The composer box's border and ring, from the prototype (`stack-08`), kept
 * pure so the precedence is testable without mounting the composer.
 *
 * One state wins at a time, in this order:
 *
 * 1. A file dragged over the box: the drop target must read as one.
 * 2. Typed text while a run is live: it will queue or steer, not send.
 * 3. A draft's permission mode: Plan is blue, Accept edits green, Manual plain.
 * 4. The plain border.
 */
export function composerFrameClass(input: {
  hasText: boolean;
  isDragOver: boolean;
  isStreaming: boolean;
  /** A draft's mode. Omit on a live chat, which never tints. */
  mode?: SessionPermissionMode;
}): string {
  if (input.isDragOver) return "border-brand bg-brand/5 dark:bg-brand/10 ring-2 ring-brand/20";
  if (input.isStreaming && input.hasText) return "border-warning shadow-warning/10";
  if (input.mode === "plan") return "border-info/50 ring-1 ring-info/20";
  if (input.mode === "acceptEdits") return "border-success/50 ring-1 ring-success/20";
  return "border-zinc-300 dark:border-zinc-700";
}
