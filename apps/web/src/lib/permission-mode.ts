// SPDX-License-Identifier: Apache-2.0

/** Mirrors `sessionModeSchema` in `contracts/src/schemas/chat.ts`. */
export type SessionPermissionMode = "acceptEdits" | "default" | "plan";

/**
 * Mirrors `sessionReportedModeSchema`: what a live chat reports. Auto and yolo
 * come from a chat created outside Studio. Studio shows them but cannot set
 * them, so a chat in either has no Mode control here.
 */
export type SessionReportedMode = SessionPermissionMode | "auto" | "yolo";

/** Whether Studio's Mode control can show and change this mode. */
export function isSelectableMode(
  mode: SessionReportedMode | undefined,
): mode is SessionPermissionMode {
  return mode === "default" || mode === "plan" || mode === "acceptEdits";
}

export interface PermissionModeOption {
  description: string;
  /** The posture dot before the title: none for Manual, green for Accept edits, blue for Plan. */
  dotClassName: string;
  title: string;
  value: SessionPermissionMode;
}

/**
 * The composer's Mode options, ported from the prototype (`stack-08`): the
 * three postures `createSession` and `setSessionMode` accept, each with the
 * line shown under its title. The desktop pill and the phone sheet read this
 * one table.
 */
export const MODE_OPTIONS: PermissionModeOption[] = [
  {
    description: "Always ask before making changes.",
    dotClassName: "bg-muted-foreground/50",
    title: "Manual",
    value: "default",
  },
  {
    description: "Automatically accept all file edits.",
    dotClassName: "bg-success",
    title: "Accept edits",
    value: "acceptEdits",
  },
  {
    description: "Create a plan before making changes.",
    dotClassName: "bg-info",
    title: "Plan",
    value: "plan",
  },
];

export function modeTitle(mode: string | undefined): string {
  if (mode === "auto") return "Auto";
  if (mode === "yolo") return "YOLO";
  return MODE_OPTIONS.find((option) => option.value === mode)?.title ?? "Manual";
}
