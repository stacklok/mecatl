// SPDX-License-Identifier: Apache-2.0

import type { CreateScheduleData, ListSchedulesResponse } from "@mecatl-studio/contracts/generated";
import {
  compileSchedulePhrase,
  looksLikeCron,
  oneShotInstant,
  type SchedulePhraseResult,
  toLocalDateTimeInput,
} from "./schedule-phrase";

export type Schedule = ListSchedulesResponse["items"][number];
export type ScheduleBody = CreateScheduleData["body"];

export interface FormValue {
  allowWrites: boolean;
  cron: string;
  maxFires: string;
  name: string;
  oneShotAt: string;
  /** The schedule's original one-shot instant, re-sent unchanged while the wall clock is unedited. */
  oneShotOriginal: string;
  oneShotMaxRetries: string;
  oneShotRetry: boolean;
  profile: "all" | "noFilesystem";
  prompt: string;
  timezone: string;
  triggerKind: "cron" | "once";
  /**
   * The permission mode a writing schedule runs in. `plan` is only ever
   * loaded from a stored writing schedule, so saving it unchanged keeps it.
   */
  writeMode: Schedule["mode"];
}

export function valueFromSchedule(schedule?: Schedule): FormValue {
  const timezone =
    schedule?.trigger.kind === "cron" ? schedule.trigger.timezone : browserTimezone();
  return {
    allowWrites: schedule?.mutating ?? false,
    cron: schedule?.trigger.kind === "cron" ? schedule.trigger.expression : "0 9 * * *",
    maxFires: String(schedule?.maxFires ?? 0),
    name: schedule?.name ?? "",
    oneShotAt: schedule?.trigger.kind === "once" ? toLocalInput(schedule.trigger.at) : "",
    oneShotOriginal: schedule?.trigger.kind === "once" ? schedule.trigger.at : "",
    oneShotMaxRetries: String(schedule?.oneShotMaxRetries || 3),
    oneShotRetry: schedule?.oneShotRetry ?? false,
    profile: schedule?.profile ?? "all",
    prompt: schedule?.prompt ?? "",
    timezone,
    triggerKind: schedule?.trigger.kind ?? "cron",
    // A read-only schedule is always `plan`; the mode offered once writes are
    // switched on is the new-schedule default.
    writeMode: schedule?.mutating ? schedule.mode : "acceptEdits",
  };
}

/**
 * The form fields that make up a schedule's trigger: exactly what the server
 * compares when it refuses to change the trigger of an existing schedule
 * (the kind, the cron expression and timezone, or the one-shot instant). The
 * run limits (`maxFires`, the one-shot retry) are not part of it.
 */
export const TRIGGER_FIELDS = [
  "triggerKind",
  "cron",
  "timezone",
  "oneShotAt",
  "oneShotOriginal",
] as const satisfies ReadonlyArray<keyof FormValue>;

export type TriggerPatch = Partial<Pick<FormValue, (typeof TRIGGER_FIELDS)[number]>>;

/**
 * Whether the form may change the trigger. Only a new schedule's can be
 * edited: the daemon keeps an existing schedule's stored next run on update,
 * so changing when it runs means creating a new schedule.
 */
export function isTriggerEditable(schedule?: Schedule): boolean {
  return schedule === undefined;
}

/**
 * Applies a trigger edit (kind switch, cron builder, timezone, one-shot date
 * and time, or a compiled phrase). An existing schedule's trigger is locked,
 * so the edit is dropped and the value is returned unchanged.
 */
export function withTriggerPatch(
  value: FormValue,
  patch: TriggerPatch,
  schedule?: Schedule,
): FormValue {
  return isTriggerEditable(schedule) ? { ...value, ...patch } : value;
}

/**
 * The value the form renders. For an existing schedule the trigger fields are
 * always re-derived from the stored trigger, whatever the form state holds, so
 * the displayed trigger can never differ from the one that is sent.
 */
export function displayedValue(value: FormValue, schedule?: Schedule): FormValue {
  if (isTriggerEditable(schedule)) return value;
  const stored = valueFromSchedule(schedule);
  return {
    ...value,
    cron: stored.cron,
    oneShotAt: stored.oneShotAt,
    oneShotOriginal: stored.oneShotOriginal,
    timezone: stored.timezone,
    triggerKind: stored.triggerKind,
  };
}

/**
 * The trigger the form sends. An existing schedule's trigger cannot be edited
 * (the daemon keeps its stored next run), so it is sent exactly as stored.
 */
export function triggerFromValue(value: FormValue, schedule?: Schedule): ScheduleBody["trigger"] {
  if (schedule) return schedule.trigger;
  return value.triggerKind === "cron"
    ? { expression: value.cron.trim(), kind: "cron", timezone: value.timezone.trim() }
    : { at: oneShotInstant(value.oneShotAt, value.oneShotOriginal) ?? "", kind: "once" };
}

/** The request body for the form. */
export function bodyFromValue(value: FormValue, schedule?: Schedule): ScheduleBody {
  const trigger = triggerFromValue(value, schedule);
  return {
    maxFires: Math.max(0, Number(value.maxFires) || 0),
    mode: value.allowWrites ? value.writeMode : "plan",
    mutating: value.allowWrites,
    name: value.name.trim(),
    oneShotMaxRetries: value.oneShotRetry ? Math.max(0, Number(value.oneShotMaxRetries) || 0) : 0,
    oneShotRetry: trigger.kind === "once" && value.oneShotRetry,
    profile: value.profile,
    prompt: value.prompt.trim(),
    trigger,
  };
}

/** What the phrase input last produced — drives the line under it. */
export type PhraseOutcome =
  | SchedulePhraseResult
  | { cron: string; kind: "raw-cron" }
  | { kind: "none" };

/**
 * Compiles the phrase input into the trigger edit it implies, if any. The
 * patch still goes through {@link withTriggerPatch}, so a phrase cannot move
 * a locked trigger either.
 */
export function phraseTriggerPatch(
  text: string,
  now?: Date,
): { outcome: PhraseOutcome; patch: TriggerPatch | null } {
  const compiled = compileSchedulePhrase(text, now);
  if (compiled?.kind === "cron") {
    return { outcome: compiled, patch: { cron: compiled.cron, triggerKind: "cron" } };
  }
  if (compiled?.kind === "one-shot") {
    return {
      outcome: compiled,
      patch: { oneShotAt: toLocalDateTimeInput(compiled.at), triggerKind: "once" },
    };
  }
  if (looksLikeCron(text)) {
    // The unmatched five-field fallback: it IS the cron, verbatim; the
    // builder lands on Custom (or the shape it happens to parse as).
    const cron = text.trim();
    return { outcome: { cron, kind: "raw-cron" }, patch: { cron, triggerKind: "cron" } };
  }
  return { outcome: { kind: "none" }, patch: null };
}

function browserTimezone() {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

function toLocalInput(value: string) {
  const date = new Date(value);
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}
