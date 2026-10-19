// SPDX-License-Identifier: Apache-2.0

/** Mirrors `reasoningEffortSchema` in `contracts/src/schemas/chat.ts`. */
export type ReasoningEffort = "default" | "high" | "low" | "max" | "medium" | "xhigh";

export const EFFORT_OPTIONS: { label: string; value: ReasoningEffort }[] = [
  { label: "Auto", value: "default" },
  { label: "Low", value: "low" },
  { label: "Medium", value: "medium" },
  { label: "High", value: "high" },
  { label: "Extra high", value: "xhigh" },
  { label: "Max", value: "max" },
];

export function effortLabel(value: string): string {
  return EFFORT_OPTIONS.find((option) => option.value === value)?.label ?? "Auto";
}

/**
 * Shown under the Effort choices when the selected model's inventory entry
 * reports no reasoning support. The daemon still accepts a tier, so the note
 * hedges rather than hiding the choice.
 */
export const NO_REASONING_SUPPORT_NOTE =
  "This model reports no reasoning support — a tier may be ignored.";
