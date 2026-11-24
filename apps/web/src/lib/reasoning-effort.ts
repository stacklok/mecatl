// SPDX-License-Identifier: Apache-2.0

/** Mirrors `effortLevelSchema` in `contracts/src/schemas/chat.ts`, in ascending order. */
export type EffortLevel = "high" | "low" | "max" | "medium" | "xhigh";

/** Mirrors `reasoningEffortSchema` in `contracts/src/schemas/chat.ts`. "default" is Auto. */
export type ReasoningEffort = "default" | EffortLevel;

const EFFORT_LEVELS: EffortLevel[] = ["low", "medium", "high", "xhigh", "max"];

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
 * reports no reasoning support, on a server without per-model effort levels.
 * The daemon still accepts a tier, so the note hedges rather than hiding the
 * choice.
 */
export const NO_REASONING_SUPPORT_NOTE =
  "This model reports no reasoning support — a tier may be ignored.";

export const NO_EFFORT_LEVELS_NOTE = "This model has no effort levels.";

export const UNKNOWN_EFFORT_LEVELS_NOTE =
  "This model's effort levels are unknown; the provider may refuse a level.";

/** A model's inventory entry, as far as the Effort choices need it. */
export interface EffortModelInfo {
  /** Absent on a server without per-model effort levels. */
  effortLevels?: { known: boolean; levels: EffortLevel[] };
  reasoning?: boolean;
}

/** What the Effort menu offers for one model, and an optional explanation under it. */
export interface EffortChoices {
  /** Auto first, then the levels offered, in ascending order. */
  options: { label: string; value: ReasoningEffort }[];
  note?: string;
  /** The server reports per-model levels but doesn't know this model's: listing models again may learn them. */
  levelsUnknown: boolean;
}

/**
 * The level a request for `requested` carries on a model whose levels are
 * known: the requested level when listed, else the highest listed level below
 * it, else none ("default"). The rule `ModelInfo.effort_levels` documents;
 * used only to predict what a draft will send, since a session reports it.
 */
export function effortSentFor(requested: ReasoningEffort, levels: EffortLevel[]): ReasoningEffort {
  if (requested === "default" || levels.includes(requested)) return requested;
  const below = EFFORT_LEVELS.slice(0, EFFORT_LEVELS.indexOf(requested)).filter((level) =>
    levels.includes(level),
  );
  return below.at(-1) ?? "default";
}

/**
 * Decides what the Effort menu offers, like mecatui's `effortChoicesFor`.
 *
 * - An unknown model (none chosen, or missing from the inventory): every tier, no claim.
 * - A server without per-model levels: every tier, with the old caveat when
 *   the model reports no reasoning support.
 * - Levels unknown: every tier, and the provider may refuse one.
 * - Levels known: Auto plus those levels. None means Auto alone; a requested
 *   level the model lacks is named with the level requests use instead, the
 *   session's `applied` echo when given, else the prediction for a draft.
 */
export function effortChoicesFor(
  model: EffortModelInfo | undefined,
  requested: ReasoningEffort,
  applied?: ReasoningEffort,
): EffortChoices {
  const every = { levelsUnknown: false, options: EFFORT_OPTIONS };
  if (!model) return every;
  const levels = model.effortLevels;
  if (!levels) {
    return model.reasoning === false ? { ...every, note: NO_REASONING_SUPPORT_NOTE } : every;
  }
  if (!levels.known) {
    return { levelsUnknown: true, note: UNKNOWN_EFFORT_LEVELS_NOTE, options: EFFORT_OPTIONS };
  }
  const options = EFFORT_OPTIONS.filter(
    (option) => option.value === "default" || levels.levels.includes(option.value),
  );
  if (options.length === 1) {
    return { levelsUnknown: false, note: NO_EFFORT_LEVELS_NOTE, options };
  }
  if (requested === "default" || levels.levels.includes(requested)) {
    return { levelsUnknown: false, options };
  }
  const sent = applied ?? effortSentFor(requested, levels.levels);
  return {
    levelsUnknown: false,
    note: `This model doesn't accept ${effortLabel(requested)}; requests use ${
      sent === "default" ? "the provider default" : effortLabel(sent)
    }.`,
    options,
  };
}

/** A session detail's model, as far as its effort goes. */
export interface SessionEffort {
  reasoningEffort: ReasoningEffort;
  requestedReasoningEffort?: ReasoningEffort;
}

/**
 * The effort a session asks for, which the pill shows and every fork carries.
 * A server without per-model levels reports only the applied level.
 */
export function requestedEffort(model: SessionEffort | undefined): ReasoningEffort {
  return model?.requestedReasoningEffort ?? model?.reasoningEffort ?? "default";
}

/** Finds a model's inventory entry by its exact provider and model IDs. */
export function findModel<T extends { id: string; providerId: string }>(
  inventory: readonly T[] | undefined,
  model: { id: string; providerId: string } | undefined,
): T | undefined {
  if (!model) return undefined;
  return inventory?.find(
    (candidate) => candidate.id === model.id && candidate.providerId === model.providerId,
  );
}
